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
	"errors"
	"math"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func samePose(a, b pose) bool {
	return near(a.x, b.x) && near(a.y, b.y) && near(wrapAngle(a.theta-b.theta), 0)
}

var clock = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// drive feeds an odometer wheel readings every 200 ms and returns the summed
// motion, composed the way the cartographer composes it.
func drive(o *odometer, steps int, perStepL, perStepR float64) pose {
	total := pose{}
	l, r := 0.0, 0.0
	for i := 0; i <= steps; i++ {
		now := clock.Add(time.Duration(i) * 200 * time.Millisecond)
		d, ok, err := o.advance(wheelSample{l, now}, wheelSample{r, now}, now)
		if err != nil {
			panic(err)
		}
		if ok {
			total = compose(total, d)
		}
		l += perStepL
		r += perStepR
	}
	return total
}

func testOdometer() *odometer {
	c := defaultOdometry()
	c.WheelCircumference = 1.0 // one metre a revolution keeps the arithmetic visible
	c.Track = 0.5
	return newOdometer(c)
}

func TestFirstPairIsOnlyABaseline(t *testing.T) {
	o := testOdometer()
	_, ok, err := o.advance(wheelSample{3, clock}, wheelSample{7, clock}, clock)
	if ok || err != nil {
		t.Errorf("first pair: ok=%v err=%v, want a silent baseline", ok, err)
	}
}

func TestStraight(t *testing.T) {
	got := drive(testOdometer(), 10, 0.1, 0.1) // ten steps of 10 cm
	if !samePose(got, pose{x: 1}) {
		t.Errorf("1 m straight gave %+v", got)
	}
}

func TestReverse(t *testing.T) {
	got := drive(testOdometer(), 10, -0.1, -0.1)
	if !samePose(got, pose{x: -1}) {
		t.Errorf("1 m backwards gave %+v", got)
	}
}

// Wheels turning in opposite directions spin the axle on the spot: the right
// wheel forward turns it to the left, counter-clockwise, positive.
func TestSpinOnTheSpot(t *testing.T) {
	// A quarter turn: each wheel travels a quarter of the circle whose
	// diameter is the track, pi*0.5/4 metres.
	quarter := math.Pi * 0.5 / 4
	got := drive(testOdometer(), 10, -quarter/10, quarter/10)
	if !samePose(got, pose{theta: math.Pi / 2}) {
		t.Errorf("a quarter turn on the spot gave %+v", got)
	}
}

// A half circle of radius 1 m: the axle ends 2 m to the left, facing back.
func TestHalfCircle(t *testing.T) {
	// Radius 1 m, track 0.5 m: inner wheel on radius 0.75, outer on 1.25.
	steps := 50
	inner := math.Pi * 0.75 / float64(steps)
	outer := math.Pi * 1.25 / float64(steps)
	got := drive(testOdometer(), steps, inner, outer)
	if !samePose(got, pose{x: 0, y: 2, theta: math.Pi}) {
		t.Errorf("a half circle of radius 1 m gave %+v, want (0, 2, pi)", got)
	}
}

// The loader's counter wraps at 204.8 revolutions. Crossing it is motion.
func TestCounterWrap(t *testing.T) {
	o := testOdometer()
	t1 := clock
	t2 := clock.Add(200 * time.Millisecond)
	o.advance(wheelSample{204.7, t1}, wheelSample{204.7, t1}, t1)
	d, ok, err := o.advance(wheelSample{0.1, t2}, wheelSample{0.1, t2}, t2)
	if !ok || err != nil {
		t.Fatalf("crossing the wrap: ok=%v err=%v", ok, err)
	}
	if !near(d.x, 0.2) {
		t.Errorf("0.2 revolutions across the wrap gave %v m", d.x)
	}

	// The left wheels are negated by the loader, so they live in the negative
	// half and forward still counts up: from -0.1, 0.2 of a revolution forward
	// wraps round to -204.7.
	o.have = false
	o.advance(wheelSample{-0.1, t1}, wheelSample{0.1, t1}, t1)
	d, ok, _ = o.advance(wheelSample{-204.7, t2}, wheelSample{0.3, t2}, t2)
	if !ok || !near(d.x, 0.2) {
		t.Errorf("left wheel across its wrap: ok=%v x=%v, want 0.2", ok, d.x)
	}
}

// A loader restart presets the counters to zero. That is a jump of however far
// the wheels had turned, and it is not motion.
func TestALoaderRestartIsNotMotion(t *testing.T) {
	o := testOdometer()
	t1 := clock
	t2 := clock.Add(200 * time.Millisecond)
	o.advance(wheelSample{57.3, t1}, wheelSample{57.2, t1}, t1)
	_, ok, err := o.advance(wheelSample{0, t2}, wheelSample{0, t2}, t2)
	var jump *discontinuity
	if ok || !errors.As(err, &jump) {
		t.Fatalf("a jump of 57 revolutions in 0.2 s: ok=%v err=%v, want a discontinuity", ok, err)
	}
	// And the next pair carries on from the new counters.
	t3 := t2.Add(200 * time.Millisecond)
	d, ok, err := o.advance(wheelSample{0.1, t3}, wheelSample{0.1, t3}, t3)
	if !ok || err != nil || !near(d.x, 0.1) {
		t.Errorf("after the restart: ok=%v err=%v x=%v, want 0.1", ok, err, d.x)
	}
}

// Full speed for the time between readings is plausible, and not refused.
func TestFullSpeedIsNotADiscontinuity(t *testing.T) {
	o := testOdometer()
	t1 := clock
	t2 := clock.Add(200 * time.Millisecond)
	o.advance(wheelSample{0, t1}, wheelSample{0, t1}, t1)
	revs := 120.0 / 60 * 0.2 // 120 RPM for 200 ms
	if _, ok, err := o.advance(wheelSample{revs, t2}, wheelSample{revs, t2}, t2); !ok || err != nil {
		t.Errorf("full speed: ok=%v err=%v", ok, err)
	}
}

func TestStaleReadingsAreNotOdometry(t *testing.T) {
	o := testOdometer()
	now := clock
	old := now.Add(-2 * time.Second)
	_, ok, err := o.advance(wheelSample{0, old}, wheelSample{0, now}, now)
	if ok || err == nil {
		t.Errorf("a two-second-old left wheel: ok=%v err=%v", ok, err)
	}
	// A wheel at rest is heard from once a heartbeat, and must still count.
	atRest := now.Add(-time.Second)
	if _, _, err := o.advance(wheelSample{0, atRest}, wheelSample{0, atRest}, now); err != nil {
		t.Errorf("a reading one heartbeat old was refused: %v", err)
	}
}

// A scanner mounted ahead of the axle swings sideways when the axle turns on
// the spot. Tracking the axle's motion as if it were the scanner's would miss
// that entirely.
func TestMountOffset(t *testing.T) {
	c := defaultOdometry()
	c.WheelCircumference = 1
	c.Track = 0.5
	c.Mount = Mount{Forward: 1}
	o := newOdometer(c)
	quarter := math.Pi * 0.5 / 4
	got := drive(o, 10, -quarter/10, quarter/10)
	// The scanner was 1 m ahead facing +x; after a quarter turn left about the
	// axle it is 1 m to the axle's left, facing +y. In its own starting frame
	// that is 1 m back and 1 m to the left.
	if !samePose(got, pose{x: -1, y: 1, theta: math.Pi / 2}) {
		t.Errorf("scanner 1 m ahead of a spinning axle moved %+v, want (-1, 1, pi/2)", got)
	}
}

func TestComposeAndInverse(t *testing.T) {
	a := pose{x: 1, y: 2, theta: 0.7}
	b := pose{x: -0.3, y: 0.5, theta: -1.9}
	if !samePose(compose(a, inverse(a)), pose{}) {
		t.Error("a composed with its inverse is not the identity")
	}
	if !samePose(compose(inverse(a), compose(a, b)), b) {
		t.Error("inverse(a) . (a . b) is not b")
	}
}

// The fault this replaces: the odometry frame starts wherever the loader was
// switched on, and the map frame wherever the first sweep was taken. The prior
// must be the map pose moved by the odometry's delta, never the odometry's own
// pose.
func TestPriorIsTheMapPoseMovedByTheDelta(t *testing.T) {
	mapPose := pose{x: 10, y: -3, theta: math.Pi / 2} // facing +y in the map
	delta := pose{x: 0.5}                             // half a metre straight ahead
	got := compose(mapPose, delta)
	if !samePose(got, pose{x: 10, y: -2.5, theta: math.Pi / 2}) {
		t.Errorf("prior %+v, want (10, -2.5, pi/2)", got)
	}
}

func TestDefaultsArePhysical(t *testing.T) {
	c := OdometryConfig{}
	applyOdometryDefaults(&c)
	if !c.on() {
		t.Error("a configuration written before odometry existed has it off")
	}
	if c.WheelCircumference != 1.335 || c.Track != 0.6275 {
		t.Errorf("geometry %v m around, %v m apart; want the measured 1.335 and 0.6275", c.WheelCircumference, c.Track)
	}
	if c.LeftNodeID != 1 || c.RightNodeID != 2 {
		t.Errorf("wheels %d and %d, want the front axle 1 and 2", c.LeftNodeID, c.RightNodeID)
	}
	if c.MaxAgeMs <= 1000 {
		t.Errorf("maxAgeMs %d is not longer than the loader's one-second heartbeat", c.MaxAgeMs)
	}
	off := false
	c = OdometryConfig{Enabled: &off}
	if c.on() {
		t.Error("enabled:false left odometry on")
	}
}

// mappingTraits is a cartographer with no network: the wheels are whatever the
// test says they are.
func mappingTraits(wheels func() (wheelSample, wheelSample, error)) *Traits {
	cfg := CartographerConfig{WidthMetres: 40, HeightMetres: 40, Resolution: 0.05,
		MatchRangeMetres: 10, MinTravelMetres: 0.05, MinTurnDegrees: 5}
	applyDefaults(&cfg)
	tr := &Traits{
		cfg:        cfg,
		g:          newGrid(cfg.WidthMetres, cfg.HeightMetres, cfg.Resolution),
		odo:        newOdometer(cfg.Odometry),
		readWheels: wheels,
	}
	tr.g.matchRange = cfg.MatchRangeMetres
	return tr
}

func scanAt(p pose, at time.Time) *forms.ScanA_v1a {
	a, d, v := sweepFrom(p, 200, 200)
	return &forms.ScanA_v1a{Angles: a, Distances: d, Valid: v, Timestamp: at}
}

// Down a straight stretch of corridor the view cannot say how far the vehicle
// has come. With the wheels it follows the vehicle; without them it refuses to
// map rather than guess. The wheel counters start at an arbitrary value, as
// they would on a loader that has been running: only their change may matter.
func TestTheWheelsCarryTheMapDownACorridor(t *testing.T) {
	const step = 0.07 // metres per sweep
	var left, right float64 = 57.3, 57.3
	wheels := func() (wheelSample, wheelSample, error) {
		now := time.Now()
		return wheelSample{left, now}, wheelSample{right, now}, nil
	}
	withWheels := mappingTraits(wheels)
	blind := mappingTraits(func() (wheelSample, wheelSample, error) {
		return wheelSample{}, wheelSample{}, errors.New("no loader")
	})

	truth := pose{x: 2}
	start := time.Now()
	for i := 0; i <= 30; i++ {
		at := start.Add(time.Duration(i) * 200 * time.Millisecond)
		withWheels.consume(scanAt(truth, at))
		blind.consume(scanAt(truth, at))

		truth.x += step
		left += step / withWheels.cfg.Odometry.WheelCircumference
		right += step / withWheels.cfg.Odometry.WheelCircumference
	}
	driven := 30 * step

	// The map's origin is where the first sweep was taken, so the estimate
	// should read the distance driven.
	got := withWheels.at
	if e := math.Abs(got.x - driven); e > 0.10 {
		t.Errorf("with wheels: at x=%.3f after driving %.2f m (error %.3f)", got.x, driven, e)
	}
	if math.Abs(got.y) > 0.10 || math.Abs(got.theta) > 3*math.Pi/180 {
		t.Errorf("with wheels: drifted to y=%.3f heading=%.1f°", got.y, got.theta*180/math.Pi)
	}
	if !withWheels.usingOdometry {
		t.Error("the odometry was never used")
	}

	if moved := blind.at.x; moved > 0.5*driven {
		t.Errorf("without wheels the estimate moved %.2f of %.2f m down a corridor that cannot show it", moved, driven)
	}
	if blind.degenerate == 0 {
		t.Error("without wheels no sweep was reported as unplaceable")
	}
	t.Logf("with wheels: x=%.3f (driven %.3f); without: x=%.3f, %d sweeps refused",
		got.x, driven, blind.at.x, blind.degenerate)
}
