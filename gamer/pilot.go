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
	"log"
	"math"
	"time"
)

// pilot is what the person holding the pad means, decided cycle by cycle and
// kept apart from I/O so it can be tested.
//
// The pad does not drive by default. As between two pilots, control is taken
// and given deliberately:
//
//   - holding L1 and R1 for five seconds, with both sticks centered, asks the
//     loader for control;
//   - holding L2 and R2 for five seconds gives it back;
//   - pressing L1, L2, R1 and R2 together, or any face button, stops the
//     vehicle whoever is driving it.
//
// A pad that does not have control sends nothing at all, except stops. A pad
// that has control and is lost — unplugged, flat, out of range — stops the
// vehicle; one that did not have control and is lost does nothing, because its
// absence says nothing about whoever is driving.
//
// What the pad believes about control is only a belief. The loader decides, and
// the pad learns the answer from the replies: a granted take, a confirmed
// release, or a refused setpoint. Each change of belief starts a new epoch, and
// a reply from an older epoch is ignored, so a setpoint refused before a stop
// cannot cancel control taken after it.
type pilot struct {
	stopButtons  []int
	stopChord    []int
	takeChord    []int
	releaseChord []int
	holdFor      time.Duration
	repeats      int
	deadZone     float64
	speedAxis    int
	steerAxis    int

	inControl bool
	epoch     int

	stopsOwed int

	holding   chord
	heldSince time.Time
	done      bool // this hold has had its effect; let go to hold again
	warned    bool // the sticks were not centered at the end of this hold
	// letGo is set by a stop and cleared once no shoulder button is held.
	// Without it, lifting L2 and R2 first after a four-button stop leaves L1
	// and R1 down, and five seconds later the pad would take control that
	// nobody asked for.
	letGo bool
}

type chord int

const (
	noChord chord = iota
	takeChord
	releaseChord
)

// action is what one cycle sends.
type action struct {
	stop    bool
	take    bool
	release bool
	drive   bool
	speed   float64 // fraction of full, -1..1
	steer   float64 // fraction of full, -1..1
	epoch   int     // the epoch the requests belong to
}

func (p *pilot) step(s padState, now time.Time) action {
	stopPressed := s.connected && (anyHeld(s, p.stopButtons) || allHeld(s, p.stopChord))
	lostInControl := !s.connected && p.inControl

	if stopPressed || lostInControl {
		if p.stopsOwed == 0 {
			switch {
			case lostInControl:
				log.Println("gamer: pad lost while in control — stopping the vehicle")
			default:
				log.Println("gamer: STOP")
			}
		}
		// Refreshed for as long as the stop is held, and owed a few cycles
		// after, so that one lost request does not matter.
		p.stopsOwed = p.repeats
		p.lose()
		p.holding = noChord
		p.letGo = true
	}

	a := action{}
	switch {
	case stopPressed || lostInControl:
		a.stop = true
	case p.stopsOwed > 0:
		p.stopsOwed--
		a.stop = true
	}
	if stopPressed || !s.connected {
		a.epoch = p.epoch
		return a
	}

	p.trackHold(s, now, &a)

	if p.inControl {
		a.drive = true
		a.speed, a.steer = p.sticks(s)
	}
	a.epoch = p.epoch
	return a
}

func (p *pilot) trackHold(s padState, now time.Time, a *action) {
	if p.letGo {
		if anyHeld(s, p.takeChord) || anyHeld(s, p.releaseChord) {
			return
		}
		p.letGo = false
	}
	c := noChord
	switch {
	case allHeld(s, p.takeChord) && !anyHeld(s, p.releaseChord):
		c = takeChord
	case allHeld(s, p.releaseChord) && !anyHeld(s, p.takeChord):
		c = releaseChord
	}
	if c != p.holding {
		p.holding, p.heldSince, p.done, p.warned = c, now, false, false
		switch c {
		case takeChord:
			log.Printf("gamer: hold L1+R1 for %.0f s to take control", p.holdFor.Seconds())
		case releaseChord:
			log.Printf("gamer: hold L2+R2 for %.0f s to release control", p.holdFor.Seconds())
		}
	}
	if c == noChord || p.done || now.Sub(p.heldSince) < p.holdFor {
		return
	}

	switch c {
	case takeChord:
		if p.inControl {
			p.done = true
			return
		}
		speed, steer := p.sticks(s)
		if speed != 0 || steer != 0 {
			if !p.warned {
				log.Println("gamer: not taking control with a stick pushed — center both sticks")
				p.warned = true
			}
			return // keep holding; it goes through once the sticks are centered
		}
		p.done = true
		a.take = true
		log.Println("gamer: asking for control")
	case releaseChord:
		p.done = true
		if !p.inControl {
			log.Println("gamer: nothing to release — this pad does not have control")
			return
		}
		a.release = true
		log.Println("gamer: releasing control")
	}
}

// granted is the loader's answer to a take made in epoch e.
func (p *pilot) granted(e int) {
	if e != p.epoch || p.inControl {
		return
	}
	p.inControl = true
	p.epoch++
	log.Println("gamer: in control")
}

// released is the loader's confirmation of a release made in epoch e.
func (p *pilot) released(e int) {
	if e != p.epoch || !p.inControl {
		return
	}
	p.lose()
	log.Println("gamer: control released")
}

// refused is a setpoint the loader would not take in epoch e: someone stopped
// the vehicle or took control.
func (p *pilot) refused(e int, reason string) {
	if e != p.epoch || !p.inControl {
		return
	}
	p.lose()
	log.Printf("gamer: control lost: %s", reason)
}

func (p *pilot) lose() {
	if p.inControl {
		p.inControl = false
	}
	p.epoch++
}

// sticks reads the two axes as fractions of full deflection, in the vehicle's
// convention (ISO 8855): forward and left are positive. On the wire both are
// the other way round — pushing a stick up or to the left gives a negative
// value — so both are negated.
func (p *pilot) sticks(s padState) (speed, steer float64) {
	return -shape(axisOf(s, p.speedAxis), p.deadZone), -shape(axisOf(s, p.steerAxis), p.deadZone)
}

func axisOf(s padState, i int) int16 {
	if i < 0 || i >= maxAxes {
		return 0
	}
	return s.axes[i]
}

// shape applies the dead zone and rescales what is outside it, so the output
// still starts from zero at the edge of the dead zone rather than jumping to it.
func shape(raw int16, dead float64) float64 {
	v := float64(raw) / 32767.0
	if v > 1 {
		v = 1
	}
	if v < -1 {
		v = -1
	}
	if math.Abs(v) < dead {
		return 0
	}
	return math.Copysign((math.Abs(v)-dead)/(1-dead), v)
}

func anyHeld(s padState, buttons []int) bool {
	for _, b := range buttons {
		if b >= 0 && b < maxButtons && s.buttons[b] {
			return true
		}
	}
	return false
}

// allHeld is false for an empty chord: a chord nobody configured is never
// pressed.
func allHeld(s padState, buttons []int) bool {
	if len(buttons) == 0 {
		return false
	}
	for _, b := range buttons {
		if b < 0 || b >= maxButtons || !s.buttons[b] {
			return false
		}
	}
	return true
}
