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

// The TDK InvenSense ICM-20948: a 3-axis accelerometer and gyroscope on one
// die, and an Asahi Kasei AK09916 magnetometer on a second die in the same
// package, reachable only through the first die's own I2C master.
//
// Register addresses, bit positions and scale factors are from the datasheet,
// DS-000189 revision 1.3, and agree with SparkFun's driver (ICM_20948_C.c,
// ICM_20948_REGISTERS.h). Where the two differ, the datasheet is followed: the
// room-temperature offset of the die temperature is 0 LSB (§3.4, table 5), where
// SparkFun subtracts 21.

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// The registers are in four banks, chosen by REG_BANK_SEL, which every bank
// has at the same address.
const regBankSel = 0x7F

// Bank 0
const (
	regWhoAmI       = 0x00
	regUserCtrl     = 0x03
	regLPConfig     = 0x05
	regPwrMgmt1     = 0x06
	regPwrMgmt2     = 0x07
	regIntPinCfg    = 0x0F
	regI2CMstStatus = 0x17
	regAccelXoutH   = 0x2D // then the gyroscope, the temperature and EXT_SLV_SENS_DATA
)

// Bank 2
const (
	regGyroSmplrtDiv  = 0x00
	regGyroConfig1    = 0x01
	regAccelSmplrtDv1 = 0x10
	regAccelSmplrtDv2 = 0x11
	regAccelConfig    = 0x14
)

// Bank 3: the I2C master and its peripherals. Peripheral 0 reads the
// magnetometer on every sample; peripheral 4 is for one-off transactions.
const (
	regI2CMstCtrl = 0x01
	regSlv0Addr   = 0x03
	regSlv0Reg    = 0x04
	regSlv0Ctrl   = 0x05
	regSlv4Addr   = 0x13
	regSlv4Reg    = 0x14
	regSlv4Ctrl   = 0x15
	regSlv4DO     = 0x16
	regSlv4DI     = 0x17
)

// Bits
const (
	whoAmIValue = 0xEA

	userCtrlI2CMstRst = 1 << 1
	userCtrlI2CIfDis  = 1 << 4 // SPI only: keeps the chip from falling back to I2C
	userCtrlI2CMstEn  = 1 << 5

	pwrMgmt1Reset     = 1 << 7
	pwrMgmt1AutoClk   = 0x01 // the best available clock, the gyroscope's PLL when it runs
	mstStatusSlv4Done = 1 << 6
	mstStatusSlv4Nack = 1 << 4
	slvRead           = 1 << 7 // in a peripheral's address: read rather than write
	slvEnable         = 1 << 7 // in a peripheral's control: run the transaction

	// 345.6 kHz on the internal bus, with a stop between reads (I2C_MST_P_NSR),
	// as TDK's and SparkFun's drivers set it.
	i2cMstCtrlValue = 0x17
)

// The AK09916 magnetometer, on the internal bus.
const (
	akAddress    = 0x0C
	akWIA2       = 0x01
	akWIA2Value  = 0x09
	akST1        = 0x10 // ST1, HXL..HZH, a reserved byte, ST2: nine bytes
	akCNTL2      = 0x31
	akCNTL3      = 0x32
	akReset      = 0x01
	akCont100Hz  = 0x08
	akST1DataRdy = 1 << 0
	akST2Overflw = 1 << 3

	akMicroTeslaPerLSB = 0.15
)

const (
	// The sample rate divider: 1125 Hz / (1 + 9) = 112.5 Hz for both the
	// accelerometer and the gyroscope, a little faster than this system reads.
	smplrtDiv = 9
	// DLPFCFG 3: a 3 dB bandwidth of 51.2 Hz for the gyroscope and 50.4 Hz for
	// the accelerometer, under half the sample rate, so polling at 100 Hz
	// does not fold noise above 50 Hz back into the readings.
	dlpfCfg = 3

	tempLSBPerDegree = 333.87
	standardGravity  = 9.80665 // m/s² per g
)

// bus is the chip's serial interface: I2C or SPI, the difference kept below
// this line except for one bit, I2C_IF_DIS.
type bus interface {
	// read fills buf from consecutive registers starting at reg.
	read(reg byte, buf []byte) error
	write(reg, value byte) error
	close() error
}

// icm20948 is one chip, configured.
type icm20948 struct {
	b    bus
	spi  bool
	bank byte // the bank last selected, or 0xFF when it is not known

	accelLSBPerG   float64
	gyroLSBPerDegS float64
}

// accelRanges and gyroRanges are the full scales the chip offers, with the
// value of the FS_SEL bits and the sensitivity at each (datasheet §3.1, §3.2).
var accelRanges = map[int]struct {
	sel     byte
	lsbPerG float64
}{2: {0, 16384}, 4: {1, 8192}, 8: {2, 4096}, 16: {3, 2048}}

var gyroRanges = map[int]struct {
	sel        byte
	lsbPerDegS float64
}{250: {0, 131}, 500: {1, 65.5}, 1000: {2, 32.8}, 2000: {3, 16.4}}

// rawSample is one reading of every sensor, in the chip's own axes and SI
// units. The magnetometer is already turned into the accelerometer's axes.
type rawSample struct {
	accel   [3]float64 // m/s²
	gyro    [3]float64 // rad/s
	tempC   float64
	mag     [3]float64 // µT
	magGood bool       // a fresh magnetometer reading that did not overflow
}

// errNotThisChip is a WHO_AM_I that is not an ICM-20948's.
var errNotThisChip = errors.New("not an ICM-20948")

// startICM20948 resets the chip and configures it: both sensors running
// continuously at 112.5 Hz behind a 50 Hz filter, at the full scales given,
// and the magnetometer read into the sample registers by the chip itself.
//
// The magnetometer is read through the chip's I2C master on both buses. Over
// I2C the host could reach it directly in bypass mode, but over SPI it cannot,
// and one path is simpler than two. It also means the magnetometer's reading
// arrives in the same burst as the other sensors', so the nine axes of a
// sample were taken together.
func startICM20948(b bus, spi bool, accelRangeG, gyroRangeDegS int) (*icm20948, error) {
	ar, ok := accelRanges[accelRangeG]
	if !ok {
		return nil, fmt.Errorf("the accelerometer's range is ±%d g; it can be 2, 4, 8 or 16", accelRangeG)
	}
	gr, ok := gyroRanges[gyroRangeDegS]
	if !ok {
		return nil, fmt.Errorf("the gyroscope's range is ±%d °/s; it can be 250, 500, 1000 or 2000", gyroRangeDegS)
	}
	c := &icm20948{b: b, spi: spi, bank: 0xFF, accelLSBPerG: ar.lsbPerG, gyroLSBPerDegS: gr.lsbPerDegS}

	if err := c.identify(); err != nil {
		return nil, err
	}
	// Reset, then wait the register start-up time, up to 100 ms (§3.4, table 5). The
	// reset also returns REG_BANK_SEL to bank 0 and, over SPI, re-enables the
	// I2C interface, which is why I2C_IF_DIS is set again below.
	if err := c.set(0, regPwrMgmt1, pwrMgmt1Reset); err != nil {
		return nil, fmt.Errorf("resetting: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	c.bank = 0xFF
	if err := c.identify(); err != nil {
		return nil, fmt.Errorf("after the reset: %w", err)
	}

	steps := []struct {
		bank, reg, value byte
	}{
		{0, regPwrMgmt1, pwrMgmt1AutoClk}, // awake, out of low-power mode
		{0, regPwrMgmt2, 0x00},            // every axis of both sensors on
		{0, regLPConfig, 0x00},            // continuous, not duty-cycled
		{0, regIntPinCfg, 0x00},           // no bypass: the master owns the internal bus
		{0, regUserCtrl, c.userCtrl()},
		{2, regGyroSmplrtDiv, smplrtDiv},
		{2, regGyroConfig1, dlpfCfg<<3 | gr.sel<<1 | 1}, // FCHOICE 1: the filter is used
		{2, regAccelSmplrtDv1, 0},
		{2, regAccelSmplrtDv2, smplrtDiv},
		{2, regAccelConfig, dlpfCfg<<3 | ar.sel<<1 | 1},
		{3, regI2CMstCtrl, i2cMstCtrlValue},
	}
	for _, s := range steps {
		if err := c.set(s.bank, s.reg, s.value); err != nil {
			return nil, fmt.Errorf("configuring bank %d register 0x%02X: %w", s.bank, s.reg, err)
		}
	}
	// The gyroscope takes up to 35 ms to start from sleep (§3.1, table 1).
	time.Sleep(40 * time.Millisecond)

	if err := c.startMagnetometer(); err != nil {
		return nil, fmt.Errorf("the magnetometer: %w", err)
	}
	return c, nil
}

// identify reads WHO_AM_I and says what was there instead, if it was not this
// chip: 0x00 and 0xFF are the usual answers of a bus with nothing on it.
func (c *icm20948) identify() error {
	var id [1]byte
	if err := c.get(0, regWhoAmI, id[:]); err != nil {
		return fmt.Errorf("reading WHO_AM_I: %w", err)
	}
	if id[0] != whoAmIValue {
		return fmt.Errorf("%w: WHO_AM_I is 0x%02X, not 0x%02X", errNotThisChip, id[0], whoAmIValue)
	}
	return nil
}

func (c *icm20948) userCtrl() byte {
	v := byte(userCtrlI2CMstEn)
	if c.spi {
		v |= userCtrlI2CIfDis
	}
	return v
}

// startMagnetometer resets the AK09916, checks it is there, sets it measuring
// continuously at 100 Hz and has peripheral 0 copy its nine data bytes into
// EXT_SLV_SENS_DATA on every sample.
//
// After a reset of the ICM the magnetometer sometimes does not answer its
// master at first. SparkFun's driver resets the master and asks again, and so
// does this, a few times before giving up.
func (c *icm20948) startMagnetometer() error {
	var lastErr error
	for try := 0; try < 5; try++ {
		if try > 0 {
			if err := c.set(0, regUserCtrl, c.userCtrl()|userCtrlI2CMstRst); err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := c.akWrite(akCNTL3, akReset); err != nil {
			lastErr = err
			continue
		}
		time.Sleep(10 * time.Millisecond)
		id, err := c.akRead(akWIA2)
		if err != nil {
			lastErr = err
			continue
		}
		if id != akWIA2Value {
			lastErr = fmt.Errorf("WIA2 is 0x%02X, not 0x%02X", id, akWIA2Value)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}
	if err := c.akWrite(akCNTL2, akCont100Hz); err != nil {
		return err
	}
	for _, s := range []struct{ reg, value byte }{
		{regSlv0Addr, slvRead | akAddress},
		{regSlv0Reg, akST1},
		{regSlv0Ctrl, slvEnable | 9},
	} {
		if err := c.set(3, s.reg, s.value); err != nil {
			return err
		}
	}
	return nil
}

// akWrite and akRead are one-off transactions with the magnetometer, through
// peripheral 4.
func (c *icm20948) akWrite(reg, value byte) error {
	for _, s := range []struct{ reg, value byte }{
		{regSlv4Addr, akAddress},
		{regSlv4Reg, reg},
		{regSlv4DO, value},
		{regSlv4Ctrl, slvEnable},
	} {
		if err := c.set(3, s.reg, s.value); err != nil {
			return err
		}
	}
	return c.awaitSlv4()
}

func (c *icm20948) akRead(reg byte) (byte, error) {
	for _, s := range []struct{ reg, value byte }{
		{regSlv4Addr, slvRead | akAddress},
		{regSlv4Reg, reg},
		{regSlv4Ctrl, slvEnable},
	} {
		if err := c.set(3, s.reg, s.value); err != nil {
			return 0, err
		}
	}
	if err := c.awaitSlv4(); err != nil {
		return 0, err
	}
	var di [1]byte
	if err := c.get(3, regSlv4DI, di[:]); err != nil {
		return 0, err
	}
	return di[0], nil
}

// awaitSlv4 waits for peripheral 4's transaction to finish. The master runs it
// at the next sample, so a few milliseconds; fifty is a magnetometer that is
// not answering.
func (c *icm20948) awaitSlv4() error {
	deadline := time.Now().Add(50 * time.Millisecond)
	var st [1]byte
	for {
		if err := c.get(0, regI2CMstStatus, st[:]); err != nil {
			return err
		}
		if st[0]&mstStatusSlv4Nack != 0 {
			return errors.New("the magnetometer did not acknowledge")
		}
		if st[0]&mstStatusSlv4Done != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the magnetometer did not answer within 50 ms")
		}
		time.Sleep(time.Millisecond)
	}
}

// sample reads every sensor in one burst: 14 bytes of accelerometer,
// gyroscope and temperature, and the 9 the master copied from the
// magnetometer.
func (c *icm20948) sample() (rawSample, error) {
	var buf [23]byte
	if err := c.get(0, regAccelXoutH, buf[:]); err != nil {
		return rawSample{}, err
	}
	return c.decode(buf), nil
}

// decode turns a burst into SI units in the accelerometer's axes.
//
// The ICM's registers are big-endian, the magnetometer's little-endian. The
// magnetometer's axes are not the accelerometer's: its x is the same, its y
// and z point the other way (datasheet §15, figures 12 and 13), so y and z
// change sign here and every vector a consumer sees is in one frame.
func (c *icm20948) decode(buf [23]byte) rawSample {
	be := func(i int) float64 { return float64(int16(uint16(buf[i])<<8 | uint16(buf[i+1]))) }
	le := func(i int) float64 { return float64(int16(uint16(buf[i+1])<<8 | uint16(buf[i]))) }

	var s rawSample
	for k := 0; k < 3; k++ {
		s.accel[k] = be(2*k) / c.accelLSBPerG * standardGravity
		s.gyro[k] = be(6+2*k) / c.gyroLSBPerDegS * math.Pi / 180
	}
	s.tempC = be(12)/tempLSBPerDegree + 21

	st1, st2 := buf[14], buf[22]
	s.magGood = st1&akST1DataRdy != 0 && st2&akST2Overflw == 0
	s.mag = [3]float64{
		le(15) * akMicroTeslaPerLSB,
		-le(17) * akMicroTeslaPerLSB,
		-le(19) * akMicroTeslaPerLSB,
	}
	return s
}

// set and get select the bank first, when it is not the one already selected.
func (c *icm20948) set(bank, reg, value byte) error {
	if err := c.selectBank(bank); err != nil {
		return err
	}
	return c.b.write(reg, value)
}

func (c *icm20948) get(bank, reg byte, buf []byte) error {
	if err := c.selectBank(bank); err != nil {
		return err
	}
	return c.b.read(reg, buf)
}

func (c *icm20948) selectBank(bank byte) error {
	if c.bank == bank {
		return nil
	}
	if err := c.b.write(regBankSel, bank<<4); err != nil {
		c.bank = 0xFF
		return err
	}
	c.bank = bank
	return nil
}
