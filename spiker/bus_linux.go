//go:build linux

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

// The two buses through the Linux kernel's user-space interfaces, i2c-dev and
// spidev, with the standard library's syscall package and nothing else. The
// structures and request numbers are those of <linux/i2c-dev.h>, <linux/i2c.h>
// and <linux/spi/spidev.h>.

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	i2cRdwr  = 0x0707 // I2C_RDWR: one or more messages, with repeated starts between them
	i2cMRead = 0x0001 // I2C_M_RD

	spiIOCMessage1     = 0x40206B00 // SPI_IOC_MESSAGE(1): _IOW('k', 0, char[32])
	spiIOCWrMode       = 0x40016B01 // SPI_IOC_WR_MODE
	spiIOCWrBitsPerWrd = 0x40016B03 // SPI_IOC_WR_BITS_PER_WORD
	spiIOCWrMaxSpeedHz = 0x40046B04 // SPI_IOC_WR_MAX_SPEED_HZ
)

// i2cMsg is struct i2c_msg. Go lays it out as C does on both 32- and 64-bit
// Linux: three 16-bit fields, two bytes of padding, and the pointer.
type i2cMsg struct {
	addr  uint16
	flags uint16
	len   uint16
	buf   uintptr
}

// The kernel reads these structures by layout, so a size that differs from C's
// is a transfer that sends garbage. These fail to compile instead: an array
// length that would be negative is an error. 12 bytes on 32-bit, 16 on 64-bit;
// spi_ioc_transfer is 32 on both.
var (
	_ [unsafe.Sizeof(i2cMsg{}) - (8 + unsafe.Sizeof(uintptr(0)))]byte
	_ [(8 + unsafe.Sizeof(uintptr(0))) - unsafe.Sizeof(i2cMsg{})]byte
	_ [unsafe.Sizeof(spiTransfer{}) - 32]byte
	_ [32 - unsafe.Sizeof(spiTransfer{})]byte
)

// i2cRdwrData is struct i2c_rdwr_ioctl_data.
type i2cRdwrData struct {
	msgs  uintptr
	nmsgs uint32
}

type i2cBus struct {
	f    *os.File
	addr uint16
}

func openI2C(device string, addr uint16) (bus, error) {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &i2cBus{f: f, addr: addr}, nil
}

// read writes the register address and reads the burst in one transaction,
// with a repeated start between the two as the ICM-20948's burst read is drawn
// (datasheet §6.3).
func (b *i2cBus) read(reg byte, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	r := [1]byte{reg}
	msgs := [2]i2cMsg{
		{addr: b.addr, len: 1, buf: uintptr(unsafe.Pointer(&r[0]))},
		{addr: b.addr, flags: i2cMRead, len: uint16(len(buf)), buf: uintptr(unsafe.Pointer(&buf[0]))},
	}
	err := b.transfer(msgs[:])
	runtime.KeepAlive(&r)
	runtime.KeepAlive(buf)
	return err
}

func (b *i2cBus) write(reg, value byte) error {
	w := [2]byte{reg, value}
	msgs := [1]i2cMsg{{addr: b.addr, len: 2, buf: uintptr(unsafe.Pointer(&w[0]))}}
	err := b.transfer(msgs[:])
	runtime.KeepAlive(&w)
	return err
}

func (b *i2cBus) transfer(msgs []i2cMsg) error {
	data := i2cRdwrData{msgs: uintptr(unsafe.Pointer(&msgs[0])), nmsgs: uint32(len(msgs))}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, b.f.Fd(), i2cRdwr, uintptr(unsafe.Pointer(&data)))
	runtime.KeepAlive(msgs)
	if errno != 0 {
		return fmt.Errorf("I2C transfer to 0x%02X: %w", b.addr, errno)
	}
	return nil
}

func (b *i2cBus) close() error { return b.f.Close() }

// spiTransfer is struct spi_ioc_transfer, 32 bytes. The two buffer addresses
// are 64-bit on every architecture, so the layout does not depend on one.
type spiTransfer struct {
	txBuf       uint64
	rxBuf       uint64
	len         uint32
	speedHz     uint32
	delayUsecs  uint16
	bitsPerWord uint8
	csChange    uint8
	txNbits     uint8
	rxNbits     uint8
	wordDelay   uint8
	pad         uint8
}

type spiBus struct {
	f       *os.File
	speedHz uint32
}

// openSPI opens a spidev device in mode 0, eight bits a word. The ICM-20948
// latches on the rising edge and changes on the falling one (§6.5), which is
// mode 0 or 3; mode 0 is what TDK's and SparkFun's drivers use.
func openSPI(device string, speedHz uint32) (bus, error) {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	mode, bits := uint8(0), uint8(8)
	for _, s := range []struct {
		req uintptr
		arg unsafe.Pointer
	}{
		{spiIOCWrMode, unsafe.Pointer(&mode)},
		{spiIOCWrBitsPerWrd, unsafe.Pointer(&bits)},
		{spiIOCWrMaxSpeedHz, unsafe.Pointer(&speedHz)},
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), s.req, uintptr(s.arg)); errno != 0 {
			f.Close()
			return nil, fmt.Errorf("configuring %s: %w", device, errno)
		}
	}
	return &spiBus{f: f, speedHz: speedHz}, nil
}

// read sends the register address with its top bit set, which is a read
// (§6.5), and keeps what came back after the first byte.
func (b *spiBus) read(reg byte, buf []byte) error {
	tx := make([]byte, len(buf)+1)
	rx := make([]byte, len(buf)+1)
	tx[0] = reg | 0x80
	if err := b.transfer(tx, rx); err != nil {
		return err
	}
	copy(buf, rx[1:])
	return nil
}

func (b *spiBus) write(reg, value byte) error {
	tx := []byte{reg & 0x7F, value}
	return b.transfer(tx, make([]byte, 2))
}

func (b *spiBus) transfer(tx, rx []byte) error {
	t := spiTransfer{
		txBuf:       uint64(uintptr(unsafe.Pointer(&tx[0]))),
		rxBuf:       uint64(uintptr(unsafe.Pointer(&rx[0]))),
		len:         uint32(len(tx)),
		speedHz:     b.speedHz,
		bitsPerWord: 8,
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, b.f.Fd(), spiIOCMessage1, uintptr(unsafe.Pointer(&t)))
	runtime.KeepAlive(tx)
	runtime.KeepAlive(rx)
	if errno != 0 {
		return fmt.Errorf("SPI transfer: %w", errno)
	}
	return nil
}

func (b *spiBus) close() error { return b.f.Close() }
