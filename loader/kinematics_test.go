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
	"testing"
)

var artitrax = Geometry{JointToFront: 0.6175, JointToRear: 0.6175, Track: 0.6275, WheelCircumference: 1.335}

func deg(d float64) float64 { return d * math.Pi / 180 }

// With equal halves both axles turn on L / tan(γ/2); at 40° that is 1.70 m,
// the figure the design discussion was based on.
func TestTurningRadius(t *testing.T) {
	for _, c := range []struct{ angle, radius float64 }{{15, 4.691}, {40, 1.697}} {
		if r := 1 / artitrax.frontCurvature(deg(c.angle)); math.Abs(r-c.radius) > 0.001 {
			t.Errorf("%v°: front radius %.3f m, want %.3f", c.angle, r, c.radius)
		}
		if r := 1 / artitrax.rearCurvature(deg(c.angle)); math.Abs(r-c.radius) > 0.001 {
			t.Errorf("%v°: rear radius %.3f m, want %.3f with equal halves", c.angle, r, c.radius)
		}
	}
}

// Positive is left, throughout.
func TestSigns(t *testing.T) {
	if artitrax.frontCurvature(deg(10)) <= 0 {
		t.Error("a positive articulation does not give a positive (leftward) curvature")
	}
	w := artitrax.wheelSpeeds(1, deg(20))
	if w[frontLeft] >= w[frontRight] || w[backLeft] >= w[backRight] {
		t.Errorf("turning left, the left wheels are not the slower ones: %v", w)
	}
	w = artitrax.wheelSpeeds(1, deg(-20))
	if w[frontRight] >= w[frontLeft] {
		t.Errorf("turning right, the right wheels are not the slower ones: %v", w)
	}
}

func TestArticulationForInvertsCurvature(t *testing.T) {
	g := Geometry{JointToFront: 0.9, JointToRear: 0.5, Track: 1, WheelCircumference: 1} // unequal halves
	for _, a := range []float64{-30, -5, 0, 0.3, 12, 29} {
		k := g.frontCurvature(deg(a))
		if got := g.articulationFor(k, deg(35)) * 180 / math.Pi; math.Abs(got-a) > 1e-6 {
			t.Errorf("curvature of %v° gave back %v°", a, got)
		}
	}
	if got := artitrax.articulationFor(10, deg(35)); math.Abs(got-deg(35)) > 1e-12 {
		t.Errorf("an impossible curvature gave %v rad, want the limit", got)
	}
	if got := artitrax.articulationFor(-10, deg(35)); math.Abs(got+deg(35)) > 1e-12 {
		t.Errorf("an impossible curvature the other way gave %v rad, want minus the limit", got)
	}
}

// At full lock the inner wheels run at 0.688 of the outer: the spread the
// design discussion quoted.
func TestWheelSpeedsAtFullLock(t *testing.T) {
	w := artitrax.wheelSpeeds(1, deg(40))
	if r := w[frontLeft] / w[frontRight]; math.Abs(r-0.688) > 0.001 {
		t.Errorf("inner/outer at 40° = %.3f, want 0.688", r)
	}
	// Equal halves: the rear axle runs on the same radius at the same speed.
	if math.Abs(w[backLeft]-w[frontLeft]) > 1e-9 || math.Abs(w[backRight]-w[frontRight]) > 1e-9 {
		t.Errorf("with equal halves the axles differ: %v", w)
	}
	// Straight: all equal, exactly.
	if s := artitrax.wheelSpeeds(0.7, 0); s != [4]float64{0.7, 0.7, 0.7, 0.7} {
		t.Errorf("straight gave %v", s)
	}
}

// Unequal halves: the rear axle has its own radius, and so its own speed.
func TestUnequalHalves(t *testing.T) {
	g := Geometry{JointToFront: 1.2, JointToRear: 0.6, Track: 1, WheelCircumference: 1}
	gamma := deg(30)
	w := g.wheelSpeeds(1, gamma)
	front := (w[frontLeft] + w[frontRight]) / 2
	back := (w[backLeft] + w[backRight]) / 2
	// Both halves turn at one rate, so each axle's speed is in proportion to
	// its radius.
	want := g.frontCurvature(gamma) / g.rearCurvature(gamma)
	if math.Abs(back/front-want) > 1e-9 {
		t.Errorf("back/front speed %.4f, want the radius ratio %.4f", back/front, want)
	}
}

// Too fast round a bend: all four scale down together, so the curve is kept.
func TestRPMLimitKeepsTheCurve(t *testing.T) {
	speeds := artitrax.wheelSpeeds(5, deg(30)) // far faster than the motors
	rpm := artitrax.wheelRPMs(speeds, 120)
	worst := 0.0
	for _, r := range rpm {
		worst = math.Max(worst, math.Abs(r))
	}
	if math.Abs(worst-120) > 1e-9 {
		t.Errorf("fastest wheel %.2f RPM, want the 120 limit", worst)
	}
	if got, want := rpm[frontLeft]/rpm[frontRight], speeds[frontLeft]/speeds[frontRight]; math.Abs(got-want) > 1e-9 {
		t.Errorf("scaling changed the inner/outer ratio from %.4f to %.4f", want, got)
	}
	// 1.335 m a revolution: 1 m/s straight is 44.94 RPM.
	if r := artitrax.wheelRPMs(artitrax.wheelSpeeds(1, 0), 120); math.Abs(r[0]-44.944) > 0.001 {
		t.Errorf("1 m/s is %.3f RPM, want 44.944", r[0])
	}
}
