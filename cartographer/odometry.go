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
	"fmt"
	"math"
	"time"
)

// Wheel odometry for an articulated vehicle, from one axle.
//
// An articulated loader steers by bending in the middle, so its two halves
// point in different directions and no single heading describes the vehicle.
// Each half, though, is a rigid body on two wheels that do not slide sideways,
// and that is exactly a differential-drive robot: the axle's centre moves at
// the mean of its two wheel speeds, and turns at their difference divided by
// the track. The articulation angle is not needed at all, provided the pose
// being tracked is that of the half the scanner is mounted on.
//
// That matters here because the articulation sensor's scale is not known: the
// loader publishes it raw. Odometry from the scanner's own axle avoids it
// entirely.
//
// What it cannot avoid is slip. An articulated vehicle in a turn scrubs its
// tires, and the loader commands the same speed to all four wheels, so the
// wheels are forced to disagree with the geometry. The encoders report what
// the wheels did, which is better than what they were told, but the heading
// from them will drift in turns. It is a prior for the scan matcher, not a
// substitute for it.

// OdometryConfig is the vehicle's geometry, and which wheels to read.
type OdometryConfig struct {
	// Enabled switches wheel odometry on; absent means on, so a configuration
	// file written before odometry existed gets it too. Without it the matcher
	// starts each sweep from constant velocity.
	Enabled *bool `json:"enabled,omitempty"`

	// Vehicle picks the loader out of the cloud; LeftNodeID and RightNodeID
	// pick the two wheels of the axle the scanner is mounted over. The front
	// axle is nodes 1 and 2, the back axle 3 and 4.
	Vehicle     map[string][]string `json:"vehicle"`
	LeftNodeID  int                 `json:"leftNodeID"`
	RightNodeID int                 `json:"rightNodeID"`

	WheelCircumference float64 `json:"wheelCircumferenceMetres"`
	Track              float64 `json:"trackMetres"`

	// Mount is where the scanner sits relative to the centre of that axle:
	// metres forward and to the left, and which way it faces.
	Mount Mount `json:"scannerMount"`

	// MaxAgeMs is how old an encoder reading may be and still count. The
	// loader timestamps a reading when the frame arrives, on its own clock, so
	// this also assumes the two hosts' clocks agree to within it.
	//
	// It must be longer than the loader's one-second heartbeat. A wheel that
	// is not turning produces no change to send, so a follower hears its
	// reading — freshly timestamped — only once a heartbeat; an encoder that
	// has stopped reporting is heard from as often, with a timestamp that
	// keeps getting older.
	MaxAgeMs int `json:"maxAgeMs"`

	// MaxWheelRPM bounds what a wheel can plausibly have turned between two
	// readings. More than that is not motion: it is the loader restarting and
	// presetting its counters to zero.
	MaxWheelRPM float64 `json:"maxWheelRPM"`
}

// Mount places the scanner on its axle.
type Mount struct {
	Forward    float64 `json:"forwardMetres"`
	Left       float64 `json:"leftMetres"`
	YawDegrees float64 `json:"yawDegrees"`
}

func (m Mount) pose() pose {
	return pose{x: m.Forward, y: m.Left, theta: m.YawDegrees * math.Pi / 180}
}

func defaultOdometry() OdometryConfig {
	return OdometryConfig{
		Enabled:            &on,
		Vehicle:            map[string][]string{"Model": {"artitrax"}},
		LeftNodeID:         1,
		RightNodeID:        2,
		WheelCircumference: 1.335,
		Track:              0.6275,
		MaxAgeMs:           1500,
		MaxWheelRPM:        120,
	}
}

var on = true

func (c OdometryConfig) on() bool { return c.Enabled == nil || *c.Enabled }

func applyOdometryDefaults(c *OdometryConfig) {
	d := defaultOdometry()
	if c.Vehicle == nil {
		c.Vehicle = d.Vehicle
	}
	if c.LeftNodeID == 0 && c.RightNodeID == 0 {
		c.LeftNodeID, c.RightNodeID = d.LeftNodeID, d.RightNodeID
	}
	if c.WheelCircumference <= 0 {
		c.WheelCircumference = d.WheelCircumference
	}
	if c.Track <= 0 {
		c.Track = d.Track
	}
	if c.MaxAgeMs <= 0 {
		c.MaxAgeMs = d.MaxAgeMs
	}
	if c.MaxWheelRPM <= 0 {
		c.MaxWheelRPM = d.MaxWheelRPM
	}
}

// counterPeriod is where the loader's travel count wraps: a 24-bit counter at
// 81 920 counts per wheel revolution. The left wheels are negated by the
// loader, which changes the sign of the range but not its length.
const counterPeriod = float64(1<<24) / 81920 // 204.8 revolutions

// wheelSample is one wheel's travel, as the loader reported it.
type wheelSample struct {
	revolutions float64
	at          time.Time
}

// odometer turns successive pairs of wheel readings into motion.
type odometer struct {
	cfg   OdometryConfig
	mount pose

	have        bool
	left, right wheelSample
}

func newOdometer(cfg OdometryConfig) *odometer {
	return &odometer{cfg: cfg, mount: cfg.Mount.pose()}
}

// discontinuity is a pair of readings that cannot be motion.
type discontinuity struct{ reason string }

func (d *discontinuity) Error() string { return d.reason }

// advance takes the latest reading of each wheel and returns how the scanner
// moved since the previous pair, in the scanner's own frame at the previous
// pair. ok is false when there is no motion to report: the first pair, a pair
// that is stale, or a pair that cannot be motion — in which case err says why
// and the next pair starts afresh.
func (o *odometer) advance(l, r wheelSample, now time.Time) (delta pose, ok bool, err error) {
	maxAge := time.Duration(o.cfg.MaxAgeMs) * time.Millisecond
	if age := now.Sub(l.at); age > maxAge || age < -maxAge {
		return pose{}, false, fmt.Errorf("left wheel reading is %d ms old", age.Milliseconds())
	}
	if age := now.Sub(r.at); age > maxAge || age < -maxAge {
		return pose{}, false, fmt.Errorf("right wheel reading is %d ms old", age.Milliseconds())
	}

	if !o.have {
		o.have, o.left, o.right = true, l, r
		return pose{}, false, nil
	}

	dt := later(l.at, r.at).Sub(later(o.left.at, o.right.at)).Seconds()
	dl := unwrap(l.revolutions - o.left.revolutions)
	dr := unwrap(r.revolutions - o.right.revolutions)

	// A little slack for readings that are not quite simultaneous.
	plausible := o.cfg.MaxWheelRPM/60*math.Max(dt, 0)*1.5 + 0.05
	if math.Abs(dl) > plausible || math.Abs(dr) > plausible {
		o.left, o.right = l, r
		return pose{}, false, &discontinuity{fmt.Sprintf(
			"the wheels jumped %.2f and %.2f revolutions in %.2f s — taken as the loader restarting, not as motion", dl, dr, dt)}
	}
	o.left, o.right = l, r

	axle := arc(dl*o.cfg.WheelCircumference, dr*o.cfg.WheelCircumference, o.cfg.Track)
	// The scanner's motion is the axle's, seen from where the scanner sits.
	return compose(compose(inverse(o.mount), axle), o.mount), true, nil
}

// arc is the motion of a differential-drive axle whose wheels travelled sl and
// sr metres, integrated as a circular arc rather than a straight step, which
// matters when a sweep's worth of turning is not small.
func arc(sl, sr, track float64) pose {
	ds := (sl + sr) / 2
	dth := (sr - sl) / track
	if math.Abs(dth) < 1e-9 {
		return pose{x: ds}
	}
	radius := ds / dth
	return pose{x: radius * math.Sin(dth), y: radius * (1 - math.Cos(dth)), theta: dth}
}

// unwrap takes the change in a wrapping counter to the smallest equivalent
// change.
func unwrap(d float64) float64 {
	return d - counterPeriod*math.Round(d/counterPeriod)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

//-------------------------------------Poses as rigid motions

// compose is a followed by b, where b is expressed in a's frame.
func compose(a, b pose) pose {
	c, s := math.Cos(a.theta), math.Sin(a.theta)
	return pose{
		x:     a.x + c*b.x - s*b.y,
		y:     a.y + s*b.x + c*b.y,
		theta: wrapAngle(a.theta + b.theta),
	}
}

// inverse undoes a.
func inverse(a pose) pose {
	c, s := math.Cos(a.theta), math.Sin(a.theta)
	return pose{
		x:     -c*a.x - s*a.y,
		y:     s*a.x - c*a.y,
		theta: wrapAngle(-a.theta),
	}
}
