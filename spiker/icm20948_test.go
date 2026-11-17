/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"errors"
	"math"
	"testing"
)

// fakeChip answers as the ICM-20948's registers do: four banks behind
// REG_BANK_SEL, a reset that clears them, and peripheral 4 running a
// transaction with a magnetometer behind it when its control register is
// enabled.
type fakeChip struct {
	regs   [4][128]byte
	bank   byte
	ak     [0x40]byte
	whoAmI byte
	akGone bool // the magnetometer never acknowledges
}

func newFakeChip() *fakeChip {
	f := &fakeChip{whoAmI: whoAmIValue}
	f.regs[0][regWhoAmI] = f.whoAmI
	f.ak[akWIA2] = akWIA2Value
	return f
}

func (f *fakeChip) write(reg, value byte) error {
	if reg == regBankSel {
		f.bank = value >> 4
		return nil
	}
	f.regs[f.bank][reg] = value
	switch {
	case f.bank == 0 && reg == regPwrMgmt1 && value&pwrMgmt1Reset != 0:
		f.regs = [4][128]byte{}
		f.regs[0][regWhoAmI] = f.whoAmI
		f.bank = 0
	case f.bank == 3 && reg == regSlv4Ctrl && value&slvEnable != 0:
		addr, r := f.regs[3][regSlv4Addr], f.regs[3][regSlv4Reg]
		if f.akGone || addr&0x7F != akAddress {
			f.regs[0][regI2CMstStatus] |= mstStatusSlv4Nack
			return nil
		}
		if addr&slvRead != 0 {
			f.regs[3][regSlv4DI] = f.ak[r]
		} else {
			f.ak[r] = f.regs[3][regSlv4DO]
		}
		f.regs[0][regI2CMstStatus] |= mstStatusSlv4Done
	}
	return nil
}

func (f *fakeChip) read(reg byte, buf []byte) error {
	copy(buf, f.regs[f.bank][reg:])
	if f.bank == 0 && reg == regI2CMstStatus {
		f.regs[0][regI2CMstStatus] = 0 // cleared by reading, as the chip does
	}
	return nil
}

func (f *fakeChip) close() error { return nil }

func TestTheChipIsConfiguredAsTheDatasheetAsks(t *testing.T) {
	for _, spi := range []bool{false, true} {
		f := newFakeChip()
		if _, err := startICM20948(f, spi, 4, 500); err != nil {
			t.Fatalf("spi=%v: %v", spi, err)
		}
		checks := []struct {
			what      string
			bank, reg byte
			want      byte
		}{
			{"awake, auto clock", 0, regPwrMgmt1, 0x01},
			{"every axis on", 0, regPwrMgmt2, 0x00},
			{"no bypass", 0, regIntPinCfg, 0x00},
			{"gyroscope: 50 Hz filter, ±500 °/s", 2, regGyroConfig1, 3<<3 | 1<<1 | 1},
			{"gyroscope at 112.5 Hz", 2, regGyroSmplrtDiv, 9},
			{"accelerometer: 50 Hz filter, ±4 g", 2, regAccelConfig, 3<<3 | 1<<1 | 1},
			{"accelerometer at 112.5 Hz", 2, regAccelSmplrtDv2, 9},
			{"master at 345.6 kHz, stop between reads", 3, regI2CMstCtrl, 0x17},
			{"peripheral 0 reads the magnetometer", 3, regSlv0Addr, 0x80 | akAddress},
			{"from ST1", 3, regSlv0Reg, akST1},
			{"nine bytes", 3, regSlv0Ctrl, 0x80 | 9},
		}
		userCtrl := byte(0x20)
		if spi {
			userCtrl = 0x30 // and the I2C interface off, or the chip may fall back to it
		}
		checks = append(checks, struct {
			what      string
			bank, reg byte
			want      byte
		}{"I2C master on", 0, regUserCtrl, userCtrl})
		for _, c := range checks {
			if got := f.regs[c.bank][c.reg]; got != c.want {
				t.Errorf("spi=%v: %s: bank %d register 0x%02X is 0x%02X, want 0x%02X", spi, c.what, c.bank, c.reg, got, c.want)
			}
		}
		if f.ak[akCNTL2] != akCont100Hz {
			t.Errorf("spi=%v: the magnetometer is in mode 0x%02X, want continuous 100 Hz", spi, f.ak[akCNTL2])
		}
	}
}

func TestAnotherChipIsRefused(t *testing.T) {
	f := newFakeChip()
	f.whoAmI = 0x71 // an MPU-9250
	f.regs[0][regWhoAmI] = f.whoAmI
	if _, err := startICM20948(f, false, 4, 500); !errors.Is(err, errNotThisChip) {
		t.Errorf("an MPU-9250 was taken for an ICM-20948: %v", err)
	}
}

func TestAMissingMagnetometerIsAnError(t *testing.T) {
	f := newFakeChip()
	f.akGone = true
	if _, err := startICM20948(f, false, 4, 500); err == nil {
		t.Error("a chip whose magnetometer never answered was started")
	}
}

func TestRangesTheChipDoesNotHaveAreRefused(t *testing.T) {
	if _, err := startICM20948(newFakeChip(), false, 3, 500); err == nil {
		t.Error("±3 g was accepted")
	}
	if _, err := startICM20948(newFakeChip(), false, 4, 400); err == nil {
		t.Error("±400 °/s was accepted")
	}
}

// One g on z, ten degrees a second about x, the room-temperature reading, and a
// magnetometer whose y and z point the other way from the accelerometer's.
func TestABurstIsDecodedIntoOneFrame(t *testing.T) {
	c := &icm20948{accelLSBPerG: 8192, gyroLSBPerDegS: 65.5}
	var buf [23]byte
	put16be := func(i int, v int16) { buf[i], buf[i+1] = byte(uint16(v)>>8), byte(v) }
	put16le := func(i int, v int16) { buf[i], buf[i+1] = byte(v), byte(uint16(v)>>8) }
	put16be(4, 8192) // accel z: 1 g at ±4 g
	put16be(6, 655)  // gyro x: 10 °/s at ±500 °/s
	put16be(12, 334) // temperature: 334 / 333.87 + 21
	buf[14] = akST1DataRdy
	put16le(15, 100)  // mag x
	put16le(17, 100)  // mag y
	put16le(19, -100) // mag z
	buf[22] = 0       // ST2: no overflow

	s := c.decode(buf)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !near(s.accel[2], standardGravity) || s.accel[0] != 0 {
		t.Errorf("acceleration %v, want 9.80665 on z", s.accel)
	}
	if !near(s.gyro[0], 10*math.Pi/180) {
		t.Errorf("angular velocity %v, want 10 °/s about x", s.gyro)
	}
	if !near(s.tempC, 334/333.87+21) {
		t.Errorf("die temperature %v", s.tempC)
	}
	if !s.magGood || s.mag != [3]float64{15, -15, 15} {
		t.Errorf("magnetic field %v (good %v), want 15, -15, 15 µT in the accelerometer's axes", s.mag, s.magGood)
	}

	buf[22] = akST2Overflw
	if c.decode(buf).magGood {
		t.Error("a magnetometer reading that overflowed was kept")
	}
	buf[22], buf[14] = 0, 0
	if c.decode(buf).magGood {
		t.Error("a magnetometer reading that was not ready was kept")
	}
}
