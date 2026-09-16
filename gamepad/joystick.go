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
	"context"
	"encoding/binary"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// The Linux joystick interface (Documentation/input/joydev) delivers one
// fixed-size record per change:
//
//	struct js_event { __u32 time; __s16 value; __u8 type; __u8 number; };
//
// It is read with a plain read(2), which is why this system needs neither cgo
// nor SDL and cross-compiles like every other one. The reference controller
// used SDL for its mapping database; the price of doing without it is that
// button and axis numbers come from the kernel driver, so they are configurable
// rather than named.
const (
	jsEventSize = 8
	jsButton    = 0x01
	jsAxis      = 0x02
	// jsInit marks the synthetic events the driver sends on open, one per
	// control, describing its current state. They are state like any other and
	// are applied, not skipped: a button held while the device is opened must
	// read as held.
	jsInit = 0x80
)

const (
	maxAxes    = 16
	maxButtons = 32
)

type jsEvent struct {
	Value  int16
	Type   uint8
	Number uint8
}

func decodeEvent(b []byte) jsEvent {
	return jsEvent{
		Value:  int16(binary.LittleEndian.Uint16(b[4:6])),
		Type:   b[6],
		Number: b[7],
	}
}

// padState is what the gamepad looks like right now.
type padState struct {
	connected bool
	axes      [maxAxes]int16
	buttons   [maxButtons]bool
}

func (s *padState) apply(e jsEvent) {
	switch e.Type &^ jsInit {
	case jsAxis:
		if int(e.Number) < maxAxes {
			s.axes[e.Number] = e.Value
		}
	case jsButton:
		if int(e.Number) < maxButtons {
			s.buttons[e.Number] = e.Value != 0
		}
	}
}

// pad owns the device and keeps padState current.
type pad struct {
	path string

	mu    sync.Mutex
	state padState
}

func (p *pad) snapshot() padState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// run reads the device until the context ends, reopening it whenever it goes
// away. A gamepad is unplugged, runs flat or walks out of Bluetooth range as a
// matter of course, and the system must outlive that: the control loop reads a
// disconnected pad as an emergency stop, and a system that exited instead would
// leave nothing to send one.
func (p *pad) run(ctx context.Context) {
	reported := false
	for ctx.Err() == nil {
		f, err := os.Open(p.path)
		if err != nil {
			if !reported {
				log.Printf("gamepad: cannot open %s: %v — will keep trying", p.path, err)
				reported = true
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		reported = false
		log.Printf("gamepad: %s opened", p.path)

		// Closing the file is what unblocks the read when the system shuts down.
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
			case <-done:
			}
			f.Close()
		}()

		p.read(f)
		close(done)

		p.mu.Lock()
		p.state = padState{}
		p.mu.Unlock()
		if ctx.Err() == nil {
			log.Printf("gamepad: %s lost", p.path)
		}
	}
}

func (p *pad) read(r io.Reader) {
	// A fresh state on every open: the driver replays the current position of
	// every control as init events, and nothing from before the disconnect may
	// survive it.
	p.mu.Lock()
	p.state = padState{connected: true}
	p.mu.Unlock()

	buf := make([]byte, jsEventSize)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		e := decodeEvent(buf)
		p.mu.Lock()
		p.state.apply(e)
		p.mu.Unlock()
	}
}
