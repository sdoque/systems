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

//go:build linux

// can_linux.go contains the SocketCAN implementation for Linux.
// It opens a raw CAN socket, binds it to a named interface (e.g. "can0"),
// and provides the canPoller goroutine that drives OBD-II request/response
// cycles on behalf of all configured signals.

package main

import (
	"context"
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

// sockaddrCAN matches struct sockaddr_can (linux/can.h).
type sockaddrCAN struct {
	Family  uint16
	_       uint16
	IfIndex int32
	_       [8]byte // addr union — unused for CAN_RAW
}

// ifreqIndex is used with SIOCGIFINDEX to resolve "can0" → kernel index.
type ifreqIndex struct {
	Name  [16]byte
	Index int32
	_     [20]byte
}

// openCAN opens a SocketCAN raw socket bound to the named interface.
// A 500 ms receive timeout is set so recvFrame never blocks indefinitely
// when the vehicle is not responding.
func openCAN(ifname string) (int, error) {
	fd, err := syscall.Socket(afCAN, syscall.SOCK_RAW, canRAW)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}

	// Resolve interface name to kernel index.
	var ifr ifreqIndex
	copy(ifr.Name[:], ifname)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(fd), siocgifindex, uintptr(unsafe.Pointer(&ifr)))
	if errno != 0 {
		syscall.Close(fd)
		return 0, fmt.Errorf("SIOCGIFINDEX for %q: %w", ifname, errno)
	}

	// 500 ms receive timeout — recvFrame returns EAGAIN on expiry.
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
// It cycles through subs, sending an OBD-II request for each PID and
// forwarding the decoded value to the corresponding asset's updateChan.
//
// Multiple frames may arrive between a request and its response (normal on a
// live CAN bus) — the inner loop discards non-matching frames until it finds
// the one for the current PID, or until the 500 ms receive timeout fires.
func canPoller(ctx context.Context, fd int, subs []pidSubscription) {
	const pollPause = 100 * time.Millisecond // gap between consecutive PID requests

	for {
		for _, sub := range subs {
			// Check for shutdown before each PID request.
			select {
			case <-ctx.Done():
				return
			default:
			}

			req := buildOBDRequest(sub.pid)
			if err := sendFrame(fd, req); err != nil {
				log.Printf("canPoller: send PID 0x%02X: %v", sub.pid, err)
				time.Sleep(pollPause)
				continue
			}

			// Read frames until we get the matching OBD-II response for this
			// PID, or until the receive timeout fires (500 ms via SO_RCVTIMEO).
			for {
				frame, err := recvFrame(fd)
				if err != nil {
					if !isTimeout(err) {
						log.Printf("canPoller: recv: %v", err)
					}
					break // timeout or fatal error — move to next PID
				}
				val, err := decodeOBDResponse(frame, sub.pid)
				if err != nil {
					continue // not our frame — try the next one
				}
				// Non-blocking send: if the asset loop is busy we simply keep
				// the previous value rather than blocking the whole poller.
				select {
				case sub.updateChan <- val:
				default:
				}
				break
			}

			time.Sleep(pollPause)
		}
	}
}
