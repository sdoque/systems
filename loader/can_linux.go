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

// can_linux.go is the SocketCAN implementation, the same raw-syscall shape the
// busdriver and the sailor use: no cgo, no external dependency, and a stub on
// every other platform so the system still builds on a laptop.
package main

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"
)

const (
	afCAN        = 29     // AF_CAN — SocketCAN address family
	canRAW       = 1      // CAN_RAW protocol
	siocgifindex = 0x8933 // IOCTL to look up interface index by name
)

type sockaddrCAN struct {
	Family  uint16
	_       uint16
	IfIndex int32
	_       [8]byte // addr union — unused for CAN_RAW
}

type ifreqIndex struct {
	Name  [16]byte
	Index int32
	_     [20]byte
}

// canFrame mirrors struct can_frame from <linux/can.h>.
type canFrame struct {
	ID   uint32
	DLC  uint8
	Pad  uint8
	Res0 uint8
	Res1 uint8
	Data [8]byte
}

func openCAN(ifname string) (int, error) {
	fd, err := syscall.Socket(afCAN, syscall.SOCK_RAW, canRAW)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	var ifr ifreqIndex
	copy(ifr.Name[:], ifname)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd), siocgifindex, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		syscall.Close(fd)
		return 0, fmt.Errorf("SIOCGIFINDEX for %q: %w", ifname, errno)
	}
	addr := sockaddrCAN{Family: afCAN, IfIndex: ifr.Index}
	_, _, errno = syscall.Syscall(syscall.SYS_BIND,
		uintptr(fd), uintptr(unsafe.Pointer(&addr)), unsafe.Sizeof(addr))
	if errno != 0 {
		syscall.Close(fd)
		return 0, fmt.Errorf("bind %s: %w", ifname, errno)
	}
	return fd, nil
}

func closeCAN(fd int) {
	if err := syscall.Close(fd); err != nil {
		log.Printf("closeCAN: %v", err)
	}
}

// sendCAN writes one frame. The 50 microsecond pause after each write is not
// decoration: can_dds carries the same pause with the comment that without it
// the bus is overwhelmed by rapid messages, and this system writes to five
// motors twice each per cycle.
func sendCAN(fd int, id uint32, data []byte) error {
	if len(data) > 8 {
		return fmt.Errorf("frame for 0x%03X is %d bytes; CAN carries 8", id, len(data))
	}
	f := canFrame{ID: id, DLC: uint8(len(data))}
	copy(f.Data[:], data)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&f)), unsafe.Sizeof(f))
	if _, err := syscall.Write(fd, buf); err != nil {
		return err
	}
	time.Sleep(50 * time.Microsecond)
	return nil
}
