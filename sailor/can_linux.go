//go:build linux

/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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

// can_linux.go contains the SocketCAN implementation for Linux.
// It opens a raw CAN socket bound to a named interface (e.g. "can0"),
// and provides the canPoller goroutine that listens for NMEA 2000 frames
// and periodically sends ISO Requests to wake up devices that don't broadcast.

package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"syscall"
	"time"
	"unsafe"
)

const (
	afCAN        = 29     // AF_CAN — SocketCAN address family
	canRAW       = 1      // CAN_RAW protocol
	siocgifindex = 0x8933 // IOCTL to look up interface index by name
)

// sockaddrCAN matches struct sockaddr_can (linux/can.h).
type sockaddrCAN struct {
	Family  uint16
	_       uint16
	IfIndex int32
	_       [8]byte
}

// ifreqIndex is used with SIOCGIFINDEX to resolve "can0" → kernel index.
type ifreqIndex struct {
	Name  [16]byte
	Index int32
	_     [20]byte
}

// openCAN opens a SocketCAN raw socket bound to the named interface.
// A 500 ms receive timeout is set so recvFrame never blocks indefinitely.
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

	// 500 ms receive timeout so canPoller wakes up periodically even when
	// the bus is idle, allowing context-cancellation to be honoured promptly.
	tv := syscall.Timeval{Sec: 0, Usec: 500_000}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("SO_RCVTIMEO: %w", err)
	}

	addr := sockaddrCAN{Family: afCAN, IfIndex: ifr.Index}
	_, _, errno = syscall.Syscall(syscall.SYS_BIND,
		uintptr(fd),
		uintptr(unsafe.Pointer(&addr)),
		unsafe.Sizeof(addr))
	if errno != 0 {
		syscall.Close(fd)
		return 0, fmt.Errorf("bind: %w", errno)
	}

	return fd, nil
}

// closeCAN closes a SocketCAN file descriptor.
func closeCAN(fd int) {
	if err := syscall.Close(fd); err != nil {
		log.Printf("closeCAN: %v", err)
	}
}

// sendFrame writes one CAN frame to the socket.
func sendFrame(fd int, f canFrame) error {
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&f)), unsafe.Sizeof(f))
	_, err := syscall.Write(fd, buf)
	return err
}

// recvFrame reads one CAN frame from the socket.
// Returns EAGAIN (as a syscall.Errno) if the 500 ms receive timeout fires.
func recvFrame(fd int) (canFrame, error) {
	var f canFrame
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&f)), unsafe.Sizeof(f))
	n, err := syscall.Read(fd, buf)
	if err != nil {
		return f, err
	}
	if n < int(unsafe.Sizeof(f)) {
		return f, fmt.Errorf("short read: %d/%d bytes", n, unsafe.Sizeof(f))
	}
	return f, nil
}

// isTimeout returns true if err is a socket receive timeout (EAGAIN/EWOULDBLOCK).
func isTimeout(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && (errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK)
}

// canPoller is the single goroutine that owns the CAN socket.
//
// It does two things concurrently:
//  1. Listens continuously for incoming NMEA 2000 frames; when a frame's PGN
//     matches a subscriber, decodes the requested field and forwards the value.
//  2. Sends ISO Request frames every requestInterval seconds for each unique
//     configured PGN, waking up devices that do not broadcast automatically.
//
// Because recvFrame blocks for up to 500 ms, context cancellation is checked
// after each receive attempt, giving a worst-case shutdown latency of 500 ms.
func canPoller(ctx context.Context, fd int, subs []pgnSubscription) {
	const requestInterval = 5 * time.Second
	nextRequest := time.Now() // send immediately on first iteration

	for {
		// Honour shutdown.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Periodically send ISO Requests (deduplicated per PGN).
		if time.Now().After(nextRequest) {
			seen := make(map[uint32]bool)
			for _, sub := range subs {
				if seen[sub.pgn] {
					continue
				}
				seen[sub.pgn] = true
				req := buildISORequest(sub.pgn)
				if err := sendFrame(fd, req); err != nil {
					log.Printf("canPoller: ISO request PGN %d: %v", sub.pgn, err)
				}
			}
			nextRequest = time.Now().Add(requestInterval)
		}

		// Block until a frame arrives or the 500 ms timeout fires.
		frame, err := recvFrame(fd)
		if err != nil {
			if !isTimeout(err) {
				log.Printf("canPoller: recv: %v", err)
			}
			continue // timeout or transient error — try again
		}

		// Identify the PGN carried in the extended CAN ID.
		pgn := extractPGN(frame.ID)

		// Deliver decoded field values to matching subscribers.
		for _, sub := range subs {
			if sub.pgn != pgn {
				continue
			}
			pgnDef, ok := pgnTable[pgn]
			if !ok {
				continue
			}
			fieldDef, ok := pgnDef.Fields[sub.field]
			if !ok {
				continue
			}
			val := fieldDef.Decode(frame.Data)
			if math.IsNaN(val) {
				log.Printf("canPoller: PGN %d field %q — not available", pgn, sub.field)
				continue
			}
			// Non-blocking send: keep the bus moving if assetLoop is busy.
			select {
			case sub.updateChan <- val:
			default:
			}
		}
	}
}
