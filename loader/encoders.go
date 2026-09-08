/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

// Reading what the machine actually did, as opposed to what it was told to do.
//
// Every constant here is taken from the artitrax can_dds bridge, which is the
// tested reference against this hardware:
//
//	can0, 500 kbit/s   motors 0x601-0x605, wheel encoders 0x18B-0x18E
//	can1, 250 kbit/s   waist angle sensor, polled at 0x700, answers at 0x701
//
// The distinction matters more than it looks. Until now this system published
// the setpoint it was holding and called it speed, which is the commanded value
// and not a measurement: a stalled motor, a slipping wheel and a working one all
// report the number that was asked for. Anything downstream that integrates it —
// odometry, and therefore a map — would be integrating a wish.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

var errCANTimeout = errors.New("no CAN frame within the timeout")

const (
	// Wheel encoders answer unsolicited, one CAN ID each, in the order the
	// motors are numbered: front left, front right, back left, back right.
	encoderBaseID = 0x18B
	encoderCount  = 4

	// 4096 counts per encoder revolution through a 20:1 gearbox.
	encoderCountsPerRev = 4096.0
	gearboxRatio        = 20.0
	countsPerOutputRev  = encoderCountsPerRev * gearboxRatio // 81 920

	// The speed field counts encoder edges in a 5 ms window.
	speedWindowMs = 5.0

	// The waist sensor is polled and answers with a 10-bit value.
	waistPollID  = 0x700
	waistReplyID = 0x701
)

// wheelReading is one wheel's own account of itself.
type wheelReading struct {
	revolutions float64 // since the encoder powered up
	rpm         float64 // of the output shaft, after the gearbox
	at          time.Time
}

// feedback is everything the machine reports back, shared by the assets that
// serve it.
type feedback struct {
	mu sync.RWMutex

	wheels [encoderCount]wheelReading

	waistRaw   int // the 10-bit sensor value, before any scaling
	waistAt    time.Time
	waistFresh bool

	staleAfter time.Duration
}

func newFeedback(staleAfter time.Duration) *feedback {
	return &feedback{staleAfter: staleAfter}
}

// decodeWheel unpacks one encoder frame.
//
// Position is a free-running 24-bit counter in bytes 0-2 and speed is a signed
// count of edges per 5 ms window in bytes 4-5, both little-endian.
func decodeWheel(f canFrame) (index int, r wheelReading, ok bool) {
	index = int(f.ID) - encoderBaseID
	if index < 0 || index >= encoderCount {
		return 0, wheelReading{}, false
	}
	if f.DLC < 6 {
		return 0, wheelReading{}, false
	}
	raw := uint32(f.Data[2])<<16 | uint32(f.Data[1])<<8 | uint32(f.Data[0])
	rawSpeed := int16(uint16(f.Data[4]) | uint16(f.Data[5])<<8)

	r.revolutions = float64(raw) / countsPerOutputRev
	r.rpm = float64(rawSpeed) * 1000 * 60 / (speedWindowMs * countsPerOutputRev)

	// The left-hand encoders are mounted facing the other way and count
	// backwards, so their sign is flipped here rather than in every consumer.
	// Indices 0 and 2 are front left and back left.
	if index%2 == 0 {
		r.revolutions = -r.revolutions
		r.rpm = -r.rpm
	}
	r.at = time.Now()
	return index, r, true
}

// waistAngle converts the raw 10-bit reading to an angle.
//
// UNVERIFIED SCALE. The reference implementation computes (raw-450)/150 and
// its README calls the result degrees, but the two cannot both be right: over
// the sensor's full 0-1023 range that expression spans about -3 to +3.8, and a
// wheel loader articulates some tens of degrees. It is much more nearly
// radians, and even that is a guess.
//
// Worse, the reference computes it in integer arithmetic — the member is an
// int and so is 150 — so it returns only -3, -2, -1, 0, 1, 2 or 3, throwing
// away every bit of a 10-bit sensor. Whatever the unit turns out to be, that
// is a bug and is not reproduced here.
//
// The scale is therefore configuration rather than a constant, and it must be
// calibrated: set the waist to a known angle, read waistRaw, and solve. Until
// that is done this system publishes the RAW value and nothing else, because a
// number with an unknown unit is worse than no number.
func waistAngle(raw int, zero float64, perUnit float64) float64 {
	return (float64(raw) - zero) * perUnit
}

//-------------------------------------The listeners

// listenEncoders folds every wheel-encoder frame on the motor bus into the
// shared feedback. It shares the bus with the motor commands, which is why it
// reads on its own socket: a reader and a writer on one socket would have to
// take turns, and the command loop must never wait for a sensor.
func (fb *feedback) listenEncoders(ctx context.Context, fd int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		f, err := recvCAN(fd, 250*time.Millisecond)
		if err != nil {
			if errors.Is(err, errCANTimeout) {
				continue
			}
			log.Printf("loader: encoder bus read failed: %v", err)
			time.Sleep(time.Second)
			continue
		}
		if i, r, ok := decodeWheel(f); ok {
			fb.mu.Lock()
			fb.wheels[i] = r
			fb.mu.Unlock()
		}
	}
}

// pollWaist asks the articulation sensor for its angle and records the answer.
func (fb *feedback) pollWaist(ctx context.Context, fd int, period time.Duration) {
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := sendCAN(fd, waistPollID, nil); err != nil {
			log.Printf("loader: cannot poll the waist sensor: %v", err)
			continue
		}
		f, err := recvCAN(fd, 200*time.Millisecond)
		if err != nil {
			if !errors.Is(err, errCANTimeout) {
				log.Printf("loader: waist sensor read failed: %v", err)
			}
			fb.mu.Lock()
			fb.waistFresh = false
			fb.mu.Unlock()
			continue
		}
		if f.ID != waistReplyID || f.DLC < 2 {
			continue
		}
		fb.mu.Lock()
		fb.waistRaw = int(f.Data[0]&0x03)<<8 | int(f.Data[1])
		fb.waistAt = time.Now()
		fb.waistFresh = true
		fb.mu.Unlock()
	}
}

//-------------------------------------Reading it back

// wheel returns one wheel's reading and whether it is recent enough to use.
func (fb *feedback) wheel(index int) (wheelReading, bool) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	if index < 0 || index >= encoderCount {
		return wheelReading{}, false
	}
	r := fb.wheels[index]
	if r.at.IsZero() || time.Since(r.at) > fb.staleAfter {
		return r, false
	}
	return r, true
}

// waist returns the raw sensor value and whether it is recent enough to use.
func (fb *feedback) waist() (int, time.Time, bool) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	if !fb.waistFresh || time.Since(fb.waistAt) > fb.staleAfter {
		return fb.waistRaw, fb.waistAt, false
	}
	return fb.waistRaw, fb.waistAt, true
}
