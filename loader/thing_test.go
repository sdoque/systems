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

import "testing"

// The controller's scale is the one fact the reference implementation states
// outright: 120 RPM is 0x7800. Everything the vehicle does rides on it.
func TestWheelScale(t *testing.T) {
	d := &drivetrain{
		cfg:      LoaderConfig{MaxWheelRPM: 120},
		setpoint: map[int]float64{1: 120, 2: -120, 3: 0},
	}
	wheel := func(node int) MotorSpec { return MotorSpec{NodeID: node, Kind: "wheel"} }

	if got := d.rawFor(wheel(1)); got != 30720 {
		t.Errorf("120 RPM = %d; want 30720 (0x7800)", got)
	}
	if got := d.rawFor(wheel(2)); got != -30720 {
		t.Errorf("-120 RPM = %d; want -30720", got)
	}
	if got := d.rawFor(wheel(3)); got != 0 {
		t.Errorf("0 RPM = %d; want 0", got)
	}
}

// A 12 inch wheel turns 17.41 times a minute at 1 km/h. The command that
// carries is what the vehicle will actually be asked for first.
func TestOneKilometrePerHour(t *testing.T) {
	const rpmAt1kmh = 17.405 // 1 km/h / (pi * 0.3048 m) * 60
	d := &drivetrain{
		cfg:      LoaderConfig{MaxWheelRPM: 120},
		setpoint: map[int]float64{1: rpmAt1kmh},
	}
	got := d.rawFor(MotorSpec{NodeID: 1, Kind: "wheel"})
	if got < 4450 || got > 4460 {
		t.Errorf("1 km/h = %d counts; want about 4456", got)
	}
}

// Steering is an effort, not an angle, so its setpoint is a percentage of what
// the drive can push. Full deflection must reach full scale and no further.
func TestSteeringIsPercentOfEffort(t *testing.T) {
	d := &drivetrain{setpoint: map[int]float64{5: 100, 6: -50}}
	if got := d.rawFor(MotorSpec{NodeID: 5, Kind: "steering"}); got != 30720 {
		t.Errorf("100%% = %d; want 30720", got)
	}
	if got := d.rawFor(MotorSpec{NodeID: 6, Kind: "steering"}); got != -15360 {
		t.Errorf("-50%% = %d; want -15360", got)
	}
}

// Nothing may leave the integer range the drive accepts, whatever is asked for.
func TestClampNeverOverflows(t *testing.T) {
	for _, v := range []float64{1e9, -1e9, 40000, -40000} {
		got := clampToScale(v)
		if got > fullScale || got < -fullScale {
			t.Errorf("clampToScale(%v) = %d, outside the drive's range", v, got)
		}
	}
}

// The rate limiter walks towards the request. Braking gets the larger step,
// because stopping should never be slower than starting.
func TestRateLimit(t *testing.T) {
	cases := []struct {
		name                string
		last, desired, want int16
		accel, brake        int16
	}{
		{"from rest, capped by the accel step", 0, 10000, 30, 30, 100},
		{"already there, no movement", 500, 500, 500, 30, 100},
		{"small step taken whole", 100, 110, 110, 30, 100},
		{"slowing uses the brake step", 1000, 0, 900, 30, 100},
		{"reversing uses the brake step", 1000, -1000, 900, 30, 100},
	}
	for _, c := range cases {
		if got := rateLimit(c.last, c.desired, c.accel, c.brake); got != c.want {
			t.Errorf("%s: rateLimit(%d,%d) = %d; want %d", c.name, c.last, c.desired, got, c.want)
		}
	}
}

// A command far outside the range must not wrap around into full reverse.
func TestRateLimitDoesNotOverflow(t *testing.T) {
	if got := rateLimit(32760, 32767, 30, 100); got < 0 {
		t.Errorf("rateLimit near the int16 ceiling wrapped to %d", got)
	}
	if got := rateLimit(-32760, -32767, 30, 100); got > 0 {
		t.Errorf("rateLimit near the int16 floor wrapped to %d", got)
	}
}
