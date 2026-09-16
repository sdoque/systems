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
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/usecases"
)

const (
	cross, circle, triangle, square = 0, 1, 2, 3
	l1, r1, l2, r2                  = 4, 5, 6, 7
	speedAxis, steerAxis            = 4, 0
)

func newTestPilot() *pilot {
	return &pilot{
		stopButtons:  []int{cross, circle, triangle, square},
		stopChord:    []int{l1, r1, l2, r2},
		takeChord:    []int{l1, r1},
		releaseChord: []int{l2, r2},
		holdFor:      5 * time.Second,
		repeats:      3,
		deadZone:     0.10,
		speedAxis:    speedAxis,
		steerAxis:    steerAxis,
	}
}

func connected() padState { return padState{connected: true} }

func held(s padState, buttons ...int) padState {
	for _, b := range buttons {
		s.buttons[b] = true
	}
	return s
}

func stick(s padState, axis int, v int16) padState {
	s.axes[axis] = v
	return s
}

var t0 = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func at(seconds float64) time.Time {
	return t0.Add(time.Duration(seconds * float64(time.Second)))
}

// tenths lists the times from `from` to `to` seconds inclusive, a tenth apart,
// computed rather than summed: fifty additions of 0.1 come to less than five.
func tenths(from, to float64) []float64 {
	var out []float64
	for i := int(math.Round(from * 10)); i <= int(math.Round(to*10)); i++ {
		out = append(out, float64(i)/10)
	}
	return out
}

// hold presses buttons from `from` to `to` seconds in tenths, and returns the
// actions that asked for something.
func hold(p *pilot, s padState, from, to float64) (took, released int) {
	for _, x := range tenths(from, to) {
		a := p.step(s, at(x))
		if a.take {
			took++
		}
		if a.release {
			released++
		}
	}
	return
}

// takeControl runs the whole handshake: five seconds of L1+R1 and the loader's
// grant.
func takeControl(t *testing.T, p *pilot) {
	t.Helper()
	var asked action
	for _, x := range tenths(0.0, 5.05) {
		if a := p.step(held(connected(), l1, r1), at(x)); a.take {
			asked = a
		}
	}
	if !asked.take {
		t.Fatal("five seconds of L1+R1 did not ask for control")
	}
	p.granted(asked.epoch)
	if !p.inControl {
		t.Fatal("a granted take did not give the pad control")
	}
	p.step(connected(), at(6)) // let go
}

// A pad that has just been opened drives nothing and asks for nothing.
func TestStartsWithoutControl(t *testing.T) {
	p := newTestPilot()
	a := p.step(stick(connected(), speedAxis, -32767), at(0))
	if a.drive || a.take || a.stop || a.release {
		t.Fatalf("a fresh pad produced %+v", a)
	}
}

func TestTakingControlNeedsFiveSeconds(t *testing.T) {
	p := newTestPilot()
	if took, _ := hold(p, held(connected(), l1, r1), 0, 4.8); took != 0 {
		t.Fatal("control was asked for before five seconds")
	}
	if took, _ := hold(p, held(connected(), l1, r1), 4.9, 8); took != 1 {
		t.Fatalf("holding past five seconds asked for control %d times, want once", took)
	}
}

func TestLettingGoEarlyStartsAgain(t *testing.T) {
	p := newTestPilot()
	hold(p, held(connected(), l1, r1), 0, 4)
	p.step(connected(), at(4.1))
	if took, _ := hold(p, held(connected(), l1, r1), 4.2, 8); took != 0 {
		t.Fatal("two short holds added up to a take")
	}
}

func TestOneShoulderIsNotAChord(t *testing.T) {
	p := newTestPilot()
	if took, _ := hold(p, held(connected(), l1), 0, 10); took != 0 {
		t.Fatal("L1 alone took control")
	}
}

// A take with the speed stick pushed would set the vehicle off the moment
// control arrived.
func TestNoTakeWithAStickPushed(t *testing.T) {
	p := newTestPilot()
	pushed := stick(held(connected(), l1, r1), speedAxis, -20000)
	if took, _ := hold(p, pushed, 0, 8); took != 0 {
		t.Fatal("control was asked for with the speed stick pushed")
	}
	// Centering the stick while still holding completes it.
	if took, _ := hold(p, held(connected(), l1, r1), 8.1, 8.5); took != 1 {
		t.Fatal("centering the stick during a completed hold did not ask for control")
	}
}

func TestOnlyAPadInControlDrives(t *testing.T) {
	p := newTestPilot()
	if a := p.step(stick(connected(), speedAxis, -32767), at(0)); a.drive {
		t.Fatal("a pad without control drove")
	}
	takeControl(t, p)
	a := p.step(stick(connected(), speedAxis, -32767), at(7))
	if !a.drive || a.speed != 1 {
		t.Fatalf("full stick with control gave %+v, want speed 1", a)
	}
}

func TestReleasingControl(t *testing.T) {
	p := newTestPilot()
	takeControl(t, p)
	var asked action
	for _, x := range tenths(10.0, 15.05) {
		if a := p.step(held(connected(), l2, r2), at(x)); a.release {
			asked = a
		}
	}
	if !asked.release {
		t.Fatal("five seconds of L2+R2 did not release control")
	}
	if !p.inControl {
		t.Fatal("the pad gave up control before the loader confirmed")
	}
	p.released(asked.epoch)
	if p.inControl {
		t.Fatal("a confirmed release left the pad in control")
	}
	if a := p.step(stick(connected(), speedAxis, -32767), at(16)); a.drive {
		t.Fatal("the pad still drove after releasing")
	}
}

// The four shoulders stop the vehicle whoever is driving, and at once — not
// after five seconds, though they contain both chords.
func TestFourShouldersStopAtOnce(t *testing.T) {
	p := newTestPilot()
	a := p.step(held(connected(), l1, r1, l2, r2), at(0))
	if !a.stop {
		t.Fatal("L1+R1+L2+R2 did not stop a vehicle this pad does not control")
	}
	if a.take || a.release || a.drive {
		t.Fatalf("a stop also asked for %+v", a)
	}
	if took, rel := hold(p, held(connected(), l1, r1, l2, r2), 0.1, 10); took != 0 || rel != 0 {
		t.Fatal("holding the stop chord also took or released control")
	}
}

func TestAFaceButtonStopsAndTakesControlAway(t *testing.T) {
	p := newTestPilot()
	takeControl(t, p)
	a := p.step(held(stick(connected(), speedAxis, -32767), triangle), at(7))
	if !a.stop || a.drive {
		t.Fatalf("a face button while driving gave %+v, want a stop and no drive", a)
	}
	if p.inControl {
		t.Fatal("the pad kept control through its own stop")
	}
	// Stops are still sent for a few cycles after the button is let go, then
	// nothing at all.
	sent := 0
	for _, x := range tenths(7.1, 8-0.1) {
		a := p.step(stick(connected(), speedAxis, -32767), at(x))
		if a.drive {
			t.Fatal("the pad drove after its stop")
		}
		if a.stop {
			sent++
		}
	}
	if sent != 3 {
		t.Errorf("%d stops sent after the button was let go, want 3", sent)
	}
}

// After a four-shoulder stop, lifting L2 and R2 first leaves L1 and R1 down.
// That must not become a take.
func TestAStopMustBeLetGoOf(t *testing.T) {
	p := newTestPilot()
	p.step(held(connected(), l1, r1, l2, r2), at(0))
	if took, _ := hold(p, held(connected(), l1, r1), 0.1, 10); took != 0 {
		t.Fatal("the tail of a stop chord took control")
	}
	p.step(connected(), at(10.1))
	if took, _ := hold(p, held(connected(), l1, r1), 10.2, 16); took != 1 {
		t.Fatal("a fresh hold after letting go did not ask for control")
	}
}

func TestLosingThePad(t *testing.T) {
	idle := newTestPilot()
	if a := idle.step(padState{}, at(0)); a.stop {
		t.Error("a pad without control stopped the vehicle when it was lost")
	}

	p := newTestPilot()
	takeControl(t, p)
	if a := p.step(padState{}, at(7)); !a.stop {
		t.Fatal("a pad in control did not stop the vehicle when it was lost")
	}
	if p.inControl {
		t.Fatal("a lost pad kept control")
	}
	// Found again: no control, no driving, until it is taken again.
	if a := p.step(stick(connected(), speedAxis, -32767), at(8)); a.drive {
		t.Fatal("a reconnected pad drove")
	}
}

// A setpoint refused before a stop must not cancel control taken after it.
func TestStaleRefusalsAreIgnored(t *testing.T) {
	p := newTestPilot()
	takeControl(t, p)
	old := p.step(stick(connected(), speedAxis, -10000), at(7)).epoch

	p.step(held(connected(), cross), at(8)) // stop
	for _, x := range tenths(8.1, 9-0.1) {
		p.step(connected(), at(x))
	}
	var asked action
	for _, x := range tenths(9.0, 14.05) {
		if a := p.step(held(connected(), l1, r1), at(x)); a.take {
			asked = a
		}
	}
	p.granted(asked.epoch)
	if !p.inControl {
		t.Fatal("control was not retaken")
	}
	p.refused(old, "the vehicle is stopped")
	if !p.inControl {
		t.Fatal("a refusal from before the stop took away control granted after it")
	}

	current := p.step(stick(connected(), speedAxis, -10000), at(15)).epoch
	p.refused(current, "driver has control")
	if p.inControl {
		t.Fatal("a current refusal did not take control away")
	}
}

// A grant for a take made before a stop is not control.
func TestAGrantFromBeforeAStopIsIgnored(t *testing.T) {
	p := newTestPilot()
	var asked action
	for _, x := range tenths(0.0, 5.05) {
		if a := p.step(held(connected(), l1, r1), at(x)); a.take {
			asked = a
		}
	}
	p.step(held(connected(), circle), at(5.2))
	p.granted(asked.epoch)
	if p.inControl {
		t.Fatal("a grant that crossed a stop gave the pad control")
	}
}

func TestShape(t *testing.T) {
	cases := []struct {
		raw  int16
		want float64
	}{
		{0, 0},
		{3000, 0},  // inside a 10% dead zone
		{-3000, 0}, //
		{32767, 1},
		{-32767, -1},
		{-32768, -1},                           // the wire's own minimum, clamped
		{16384, (16384.0/32767.0 - 0.1) / 0.9}, // half: rescaled from the dead zone's edge
		{3600, (3600.0/32767.0 - 0.1) / 0.9},   // just outside: starts near zero, not at 0.1
	}
	for _, c := range cases {
		got := shape(c.raw, 0.10)
		if d := got - c.want; d > 1e-9 || d < -1e-9 {
			t.Errorf("shape(%d) = %.6f, want %.6f", c.raw, got, c.want)
		}
	}
}

func TestEventsDecodeIncludingInit(t *testing.T) {
	var buf bytes.Buffer
	write := func(value int16, typ, number uint8) {
		rec := make([]byte, jsEventSize)
		binary.LittleEndian.PutUint32(rec[0:4], 1234)
		binary.LittleEndian.PutUint16(rec[4:6], uint16(value))
		rec[6], rec[7] = typ, number
		buf.Write(rec)
	}
	write(1, jsButton|jsInit, 4) // held while the device was opened
	write(-32767, jsAxis, 4)
	write(1, jsButton, 5)
	write(0, jsButton, 5)

	p := &pad{}
	p.read(&buf)
	s := p.snapshot()
	if !s.connected {
		t.Error("read did not mark the pad connected")
	}
	if !s.buttons[4] {
		t.Error("a button reported by an init event was dropped")
	}
	if s.buttons[5] {
		t.Error("a pressed-then-released button reads as held")
	}
	if s.axes[4] != -32767 {
		t.Errorf("axis 4 = %d, want -32767", s.axes[4])
	}
}

func TestSetCervicePicksOneMotor(t *testing.T) {
	cer := setCervice("setpoint", "Steering", map[string][]string{"Model": {"artitrax"}},
		map[string][]string{"NodeID": {"5"}}, nil)
	if cer.Definition != "setpoint" || cer.Mode != "set" {
		t.Errorf("cervice is %q/%q, want setpoint/set", cer.Definition, cer.Mode)
	}
	if got := cer.Details["NodeID"]; len(got) != 1 || got[0] != "5" {
		t.Errorf("NodeID detail = %v, want [5]", got)
	}
	if got := cer.Details["Model"]; len(got) != 1 || got[0] != "artitrax" {
		t.Errorf("Model detail = %v, want [artitrax]", got)
	}
}

// The newest value replaces a pending one: a zero is never queued behind a
// speed that is no longer wanted.
func TestSenderKeepsOnlyTheNewest(t *testing.T) {
	s := newSender("FrontLeft", nil, nil, unitRPM, nil, setpointReply)
	s.offer(120, 1)
	s.offer(60, 1)
	s.offer(0, 2)
	if got := <-s.next; got.value != 0 || got.epoch != 2 {
		t.Errorf("pending job = %+v, want 0 in epoch 2", got)
	}
	select {
	case j := <-s.next:
		t.Errorf("a second job %+v was queued", j)
	default:
	}
}

func TestRepliesReachThePilot(t *testing.T) {
	p := newTestPilot()
	var asked action
	for _, x := range tenths(0.0, 5.05) {
		if a := p.step(held(connected(), l1, r1), at(x)); a.take {
			asked = a
		}
	}
	applyReply(p, reply{kind: controlReply, value: 1, epoch: asked.epoch})
	if !p.inControl {
		t.Fatal("a successful control reply did not grant control")
	}
	e := p.step(connected(), at(6)).epoch
	applyReply(p, reply{kind: setpointReply, epoch: e,
		err: &usecases.ProviderRefusal{StatusCode: http.StatusConflict, Detail: "painter stopped it"}})
	if p.inControl {
		t.Fatal("a 409 on a setpoint did not take control away")
	}
}

func TestAnUnreachableLoaderIsNotALossOfControl(t *testing.T) {
	p := newTestPilot()
	takeControl(t, p)
	e := p.step(connected(), at(7)).epoch
	applyReply(p, reply{kind: setpointReply, epoch: e, err: errors.New("connection refused")})
	if !p.inControl {
		t.Fatal("a network failure was taken as a loss of control")
	}
}
