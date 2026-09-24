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

import "math"

// The vehicle's geometry, and what it takes to follow a curve with it.
//
// A pilot commands a speed and a curvature, and nothing about the machine: the
// same two numbers would drive a front-steered loader. Turning them into an
// articulation angle and four wheel speeds is this file's job, from dimensions
// that are configuration, so that the loader can be moved to another machine
// by editing a file.
//
// Signs follow ISO 8855 (and ROS REP-103): x forward, y to the left, z up. A
// positive curvature, a positive articulation and a positive yaw rate all mean
// turning left. The articulation sensor's own convention — lower counts to the
// left — is a calibration fact and stops at the calibration.
//
// The reference point is the center of the front axle. The commanded speed is
// that point's speed and the commanded curvature is the curvature of that
// point's path. The cartographer reports the scanner's pose, and with the
// scanner mounted over the front axle the two coincide; mounted elsewhere, the
// difference is the scanner mount the cartographer is configured with.
//
// The model is the steady-state one for articulated vehicles: with the joint
// held at an angle, both halves turn about one center at one rate, the front
// axle on radius (L1 cos γ + L2) / sin γ and the rear on (L2 cos γ + L1) / sin γ,
// where L1 and L2 are the distances from the joint to each axle. While the
// joint is moving the halves also turn relative to each other; at the rates this
// machine steers that term is ignored.

// Geometry is the vehicle's dimensions.
type Geometry struct {
	JointToFront       float64 `json:"jointToFrontAxleMetres"`
	JointToRear        float64 `json:"jointToRearAxleMetres"`
	Track              float64 `json:"trackMetres"`
	WheelCircumference float64 `json:"wheelCircumferenceMetres"`
}

// frontCurvature is the curvature of the front axle's path at articulation
// gamma, in radians, positive to the left.
func (g Geometry) frontCurvature(gamma float64) float64 {
	return math.Sin(gamma) / (g.JointToFront*math.Cos(gamma) + g.JointToRear)
}

// rearCurvature is the same for the rear axle.
func (g Geometry) rearCurvature(gamma float64) float64 {
	return math.Sin(gamma) / (g.JointToRear*math.Cos(gamma) + g.JointToFront)
}

// articulationFor is the articulation that puts the front axle on a path of
// curvature kappa, limited to ±limit radians.
//
// frontCurvature rises steadily with the angle over the whole range a joint can
// reach, so the inverse is found by bisection: plain, and correct for unequal
// half-lengths, where the closed form for equal ones is not.
func (g Geometry) articulationFor(kappa, limit float64) float64 {
	if kappa >= g.frontCurvature(limit) {
		return limit
	}
	if kappa <= g.frontCurvature(-limit) {
		return -limit
	}
	lo, hi := -limit, limit
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2
		if g.frontCurvature(mid) < kappa {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// wheel positions, in the order wheelSpeeds returns them.
const (
	frontLeft = iota
	frontRight
	backLeft
	backRight
)

// wheelSpeeds is how fast each wheel's contact point must move, in meters per
// second, for the front axle to move at v with the joint at gamma.
//
// It is computed from the articulation the joint actually has, not the one it
// was asked for: the wheels have to agree with the machine as it is, and while
// the joint is still swinging towards its target they would otherwise fight it.
//
// On a curve the inner wheels run slower than the outer, and the rear axle,
// on its own radius, at its own speed. At full articulation on the Artitrax the
// spread is nearly a fifth either side of the mean.
func (g Geometry) wheelSpeeds(v, gamma float64) [4]float64 {
	if math.Abs(gamma) < 1e-9 {
		return [4]float64{v, v, v, v}
	}
	kf := g.frontCurvature(gamma)
	kr := g.rearCurvature(gamma)
	vr := v * kf / kr // both halves turn at one rate: v·kf = vr·kr
	half := g.Track / 2
	return [4]float64{
		frontLeft:  v * (1 - kf*half),
		frontRight: v * (1 + kf*half),
		backLeft:   vr * (1 - kr*half),
		backRight:  vr * (1 + kr*half),
	}
}

// wheelRPMs turns wheel speeds into motor setpoints, scaling all four down
// together if any would exceed the motors' maximum. Scaling them together
// keeps their ratios, and so the curve: a vehicle asked to go too fast round a
// bend goes round it more slowly rather than wider.
func (g Geometry) wheelRPMs(speeds [4]float64, maxRPM float64) [4]float64 {
	var rpm [4]float64
	worst := 0.0
	for i, s := range speeds {
		rpm[i] = s / g.WheelCircumference * 60
		worst = math.Max(worst, math.Abs(rpm[i]))
	}
	if worst > maxRPM {
		scale := maxRPM / worst
		for i := range rpm {
			rpm[i] *= scale
		}
	}
	return rpm
}
