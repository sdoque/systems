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
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// calibrated is the waist as the students might measure it: straight at 450,
// 20° to the left at 230, so 11 counts a degree with the count falling to the
// left.
func calibrated() WaistConfig {
	w := defaultWaist()
	w.CalibrationCount, w.CalibrationDegrees = 230, 20
	w.EffortTurnsLeft = 1
	return w
}

func TestCalibrationIsISO(t *testing.T) {
	w := calibrated()
	if !w.calibrated() {
		t.Fatal("a two-point calibration was not taken")
	}
	for _, c := range []struct {
		raw int
		deg float64
	}{{450, 0}, {230, 20}, {560, -10}} {
		if got := w.degrees(c.raw); math.Abs(got-c.deg) > 1e-9 {
			t.Errorf("count %d = %v°, want %v° (positive left)", c.raw, got, c.deg)
		}
	}
	if w.leftward() != -1 {
		t.Error("the count falls to the left, and leftward says otherwise")
	}
	// Measured on the other side, the same sensor gives the same answer.
	w.CalibrationCount, w.CalibrationDegrees = 670, -20
	if got := w.degrees(230); math.Abs(got-20) > 1e-9 {
		t.Errorf("calibrated on the right, count 230 = %v°, want 20", got)
	}
}

func TestUncalibratedByDefault(t *testing.T) {
	if defaultWaist().calibrated() {
		t.Error("the default waist claims a calibration nobody made")
	}
}

func TestBeyondTheLimit(t *testing.T) {
	w := calibrated() // limit 35°: counts 65 to 835
	for _, c := range []struct{ raw, side int }{{450, 0}, {70, 0}, {54, 1}, {846, -1}} {
		if got := w.beyond(c.raw); got != c.side {
			t.Errorf("calibrated, count %d: beyond = %d, want %d", c.raw, got, c.side)
		}
	}
	u := defaultWaist() // uncalibrated: 40 counts either side, lower is left
	for _, c := range []struct{ raw, side int }{{450, 0}, {490, 0}, {491, -1}, {409, 1}} {
		if got := u.beyond(c.raw); got != c.side {
			t.Errorf("uncalibrated, count %d: beyond = %d, want %d", c.raw, got, c.side)
		}
	}
}

func TestGuard(t *testing.T) {
	s := newWaistState(calibrated())
	const pastLeft, pastRight = 40, 860

	if e, _ := s.guard(30, 450, true); e != 30 {
		t.Errorf("inside the limit, effort was cut to %v", e)
	}
	if e, why := s.guard(30, 450, false); e != 0 || !strings.Contains(why, "fresh") {
		t.Errorf("with a stale reading: %v (%q), want no steering", e, why)
	}
	if e, _ := s.guard(30, pastLeft, true); e != 0 {
		t.Error("past the left limit, effort further left was allowed")
	}
	if e, _ := s.guard(-30, pastLeft, true); e != -30 {
		t.Error("past the left limit, effort back to the right was refused")
	}
	if e, _ := s.guard(-30, pastRight, true); e != 0 {
		t.Error("past the right limit, effort further right was allowed")
	}
	if e, _ := s.guard(30, pastRight, true); e != 30 {
		t.Error("past the right limit, effort back to the left was refused")
	}

	unknown := newWaistState(defaultWaist())
	if e, _ := unknown.guard(30, 450, true); e != 30 {
		t.Error("with the effort direction unknown, steering inside the window was refused; it is how it gets found")
	}
	if e, _ := unknown.guard(-30, 400, true); e != 0 {
		t.Error("with the effort direction unknown, effort beyond the window was allowed")
	}

	s.fault = "steering fault: test"
	if e, _ := s.guard(-10, 450, true); e != 0 {
		t.Error("steering was allowed with a fault standing")
	}
}

func TestWatchdog(t *testing.T) {
	cfg := calibrated()
	cfg.StallMs = 1000
	start := time.Now()
	at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }

	// Effort held, joint not moving: the chain.
	s := newWaistState(cfg)
	s.watch(40, 450, true, at(0))
	if f, _ := s.watch(40, 451, true, at(500)); f != "" {
		t.Fatalf("a fault before the watch period was over: %s", f)
	}
	if f, _ := s.watch(40, 451, true, at(1000)); !strings.Contains(f, "chain") {
		t.Errorf("a second of effort and one count of movement: %q, want a stall", f)
	}

	// Effort left and the count falling, as calibrated: all well.
	s = newWaistState(cfg)
	s.watch(40, 450, true, at(0))
	if f, _ := s.watch(40, 420, true, at(1000)); f != "" {
		t.Errorf("the joint moving the right way was a fault: %s", f)
	}

	// Effort left and the count rising: a sign is wrong, and with it the limit.
	s = newWaistState(cfg)
	s.watch(40, 450, true, at(0))
	if f, _ := s.watch(40, 480, true, at(1000)); !strings.Contains(f, "opposite") {
		t.Errorf("the joint moving the wrong way: %q, want a direction fault", f)
	}

	// Small efforts are not watched: holding the joint is not a stall.
	s = newWaistState(cfg)
	s.watch(10, 450, true, at(0))
	if f, _ := s.watch(10, 450, true, at(5000)); f != "" {
		t.Errorf("a holding effort was taken for a stall: %s", f)
	}

	// Direction unknown: say what happened, fault nothing.
	u := defaultWaist()
	u.StallMs = 1000
	s = newWaistState(u)
	s.watch(40, 450, true, at(0))
	f, obs := s.watch(40, 420, true, at(1000))
	if f != "" || !strings.Contains(obs, "effortTurnsLeft") {
		t.Errorf("with the direction unknown: fault %q, observation %q", f, obs)
	}
}

func TestAngleLoop(t *testing.T) {
	s := newWaistState(calibrated())
	if e := s.angleEffort(10, 0); e != 40 {
		t.Errorf("10° to the left of the target: effort %v, want 40", e)
	}
	if e := s.angleEffort(-30, 0); e != -50 {
		t.Errorf("30° off: effort %v, want the -50 ceiling", e)
	}
	if e := s.angleEffort(10, 9.8); e != 0 {
		t.Errorf("inside the deadband: effort %v, want 0", e)
	}
}

//-------------------------------------Through the drivetrain

// withWaist puts a fresh reading into the feedback.
func withWaist(d *drivetrain, raw int) {
	d.fb.mu.Lock()
	d.fb.waistRaw, d.fb.waistAt, d.fb.waistFresh = raw, time.Now(), true
	d.fb.mu.Unlock()
}

func artitraxMotors() []MotorSpec {
	return []MotorSpec{
		{Name: "FrontLeft", NodeID: 1, Kind: "wheel", Axle: "front", Side: "left"},
		{Name: "FrontRight", NodeID: 2, Kind: "wheel", Axle: "front", Side: "right"},
		{Name: "BackLeft", NodeID: 3, Kind: "wheel", Axle: "back", Side: "left"},
		{Name: "BackRight", NodeID: 4, Kind: "wheel", Axle: "back", Side: "right"},
		{Name: "Steering", NodeID: 5, Kind: "steering"},
	}
}

func drivingDrivetrain(t *testing.T, w WaistConfig) (*drivetrain, *Traits, *Traits) {
	t.Helper()
	d := testDrivetrain(t, true)
	d.cfg.Motors = artitraxMotors()
	d.cfg.MaxWheelRPM = 120
	d.cfg.Waist = w
	d.waist = newWaistState(w)
	vehicle := &Traits{Name: "Vehicle", Kind: "vehicle", dt: d, encoderIndex: -1}
	steering := &Traits{Name: "Steering", NodeID: 5, Kind: "steering", dt: d, encoderIndex: -1}
	if w := put(t, vehicle.controlService, "gamer", "1"); w.Code != http.StatusOK {
		t.Fatalf("taking control: %d %s", w.Code, w.Body)
	}
	return d, vehicle, steering
}

// A velocity through a bend sets the wheels from the kinematics, with the
// inner wheels slower.
func TestVelocityDrivesTheWheelsThroughTheKinematics(t *testing.T) {
	d, vehicle, _ := drivingDrivetrain(t, calibrated())
	withWaist(d, 230) // 20° to the left
	if w := put(t, vehicle.velocityService, "gamer", "1.0"); w.Code != http.StatusOK {
		t.Fatalf("velocity: %d %s", w.Code, w.Body)
	}
	d.writeCycle()
	left, right := d.setpoint[1], d.setpoint[2]
	if !(left > 0 && right > left) {
		t.Errorf("at 20° left, front wheels at %.1f and %.1f RPM; want the left slower", left, right)
	}
	want := artitrax.wheelRPMs(artitrax.wheelSpeeds(1, deg(20)), 120)
	if math.Abs(left-want[frontLeft]) > 1e-6 || math.Abs(d.setpoint[4]-want[backRight]) > 1e-6 {
		t.Errorf("setpoints %v, want the kinematics' %v", d.setpoint, want)
	}
}

func TestVelocityNeedsControl(t *testing.T) {
	d := testDrivetrain(t, true)
	vehicle := &Traits{Name: "Vehicle", Kind: "vehicle", dt: d, encoderIndex: -1}
	if w := put(t, vehicle.velocityService, "gamer", "1"); w.Code != http.StatusConflict {
		t.Errorf("velocity without control: %d", w.Code)
	}
}

func TestCurvatureNeedsACalibration(t *testing.T) {
	d, vehicle, _ := drivingDrivetrain(t, defaultWaist())
	withWaist(d, 450)
	w := put(t, vehicle.curvatureService, "gamer", "0.2")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "calibrated") {
		t.Errorf("curvature on an uncalibrated waist: %d %q, want 503 saying why", w.Code, w.Body)
	}
	if !d.helm.holds(pad) {
		t.Error("a refused curvature cost the pilot control")
	}
}

// A curvature command steers the joint towards the matching angle.
func TestCurvatureSteersTowardsItsAngle(t *testing.T) {
	d, vehicle, _ := drivingDrivetrain(t, calibrated())
	withWaist(d, 450) // straight
	k := artitrax.frontCurvature(deg(10))
	if w := put(t, vehicle.curvatureService, "gamer", formatFloat(k)); w.Code != http.StatusOK {
		t.Fatalf("curvature: %d %s", w.Code, w.Body)
	}
	d.writeCycle()
	if d.last[5] <= 0 {
		t.Errorf("asked to turn left from straight, the steering motor got %d; want positive effort", d.last[5])
	}
	// Reversed motor: the same request, the opposite raw effort.
	w := calibrated()
	w.EffortTurnsLeft = -1
	d2, v2, _ := drivingDrivetrain(t, w)
	withWaist(d2, 450)
	put(t, v2.curvatureService, "gamer", formatFloat(k))
	d2.writeCycle()
	if d2.last[5] >= 0 {
		t.Errorf("with the motor reversed, the raw effort was %d; want negative", d2.last[5])
	}
}

// Past the limit the steering motor stops at once, not down the ramp.
func TestTheLimitStopsTheSteeringAtOnce(t *testing.T) {
	d, _, steering := drivingDrivetrain(t, calibrated())
	withWaist(d, 450)
	put(t, steering.setpointService, "gamer", "50") // hard left
	for i := 0; i < 20; i++ {
		d.writeCycle()
	}
	if d.last[5] <= 0 {
		t.Fatal("the steering never started")
	}
	withWaist(d, 40) // now past the left limit
	d.writeCycle()
	if d.last[5] != 0 {
		t.Errorf("past the limit, the steering motor is still at %d", d.last[5])
	}
	// Back towards straight is allowed.
	put(t, steering.setpointService, "gamer", "-30")
	d.writeCycle()
	if d.last[5] >= 0 {
		t.Errorf("effort back towards straight was refused: %d", d.last[5])
	}
}

// A chain that has jumped stops the vehicle, and the fault clears only when
// someone takes control again.
func TestAStallStopsTheVehicle(t *testing.T) {
	w := calibrated()
	w.StallMs = 30
	d, vehicle, steering := drivingDrivetrain(t, w)
	put(t, steering.setpointService, "gamer", "60")
	deadline := time.Now().Add(2 * time.Second)
	for !d.helm.stopped && time.Now().Before(deadline) {
		withWaist(d, 450) // the joint never moves
		d.writeCycle()
		time.Sleep(5 * time.Millisecond)
	}
	if !d.helm.stopped || !strings.Contains(d.helm.why, "watchdog") {
		t.Fatalf("a steering motor pushing a joint that never moves did not stop the vehicle (stopped=%v, %q)", d.helm.stopped, d.helm.why)
	}
	if e, _ := d.waist.guard(-20, 450, true); e != 0 {
		t.Error("the fault did not hold the steering")
	}
	if w := put(t, vehicle.controlService, "gamer", "1"); w.Code != http.StatusOK {
		t.Fatalf("taking control after the fault: %d %s", w.Code, w.Body)
	}
	if d.waist.fault != "" {
		t.Error("taking control again did not clear the fault")
	}
}

// Nobody in control: the steering does not return to straight by itself.
func TestReleaseLeavesTheSteeringAlone(t *testing.T) {
	d, vehicle, _ := drivingDrivetrain(t, calibrated())
	withWaist(d, 450)
	put(t, vehicle.curvatureService, "gamer", formatFloat(artitrax.frontCurvature(deg(15))))
	put(t, vehicle.controlService, "gamer", "0")
	withWaist(d, 300) // well off straight
	d.writeCycle()
	if d.byAngle || d.curvature != 0 {
		t.Error("a release left a curvature command standing")
	}
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
