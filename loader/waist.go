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

// The waist: what its sensor means, how far it may go, and what drives it.
//
// The waist motor turns the joint through a bicycle chain, and there is no hard
// stop and no limit switch anywhere in the travel. Past about 40° either way the
// motor is simply driving the joint into itself, and something gives — the
// chain, the sprocket or the motor. The only limit is the one in this file.
//
// So steering effort is never sent blind. Every cycle it passes a guard:
//
//   - no fresh reading from the waist sensor, no steering at all;
//   - beyond the limit, no effort that pushes further out; effort back towards
//     straight is always allowed;
//   - while nobody has said which way the motor turns the joint, no effort at
//     all beyond the limit, since "back" is not known;
//   - a watchdog on the effort and the angle together: effort held and the
//     joint not moving is a jumped or broken chain, or a stalled motor; effort
//     one way and the joint moving the other is a sign configured wrongly,
//     which would turn the limit into the opposite of a limit. Either stops the
//     vehicle, and the loader says which.
//
// A refused effort takes the steering motor to zero at once, skipping the ramp:
// the ramp's worth of travel is exactly what the limit is there to prevent.

// WaistConfig is the sensor's calibration, the limit, and the angle loop.
type WaistConfig struct {
	// StraightCount is the sensor's reading with the joint straight.
	StraightCount int `json:"straightCount"`

	// CalibrationCount is the reading at a measured articulation of
	// CalibrationDegrees, positive to the LEFT (ISO 8855). Until both are set
	// the loader is uncalibrated: it will not steer to an angle, and its limit
	// is UncalibratedWindowCounts either side of straight.
	CalibrationCount   int     `json:"calibrationCount"`
	CalibrationDegrees float64 `json:"calibrationDegrees"`

	// LimitDegrees is the software limit, either side. The joint's range is
	// about ±40° with nothing to stop it beyond that; the default leaves a
	// margin for the travel between one reading and the next.
	LimitDegrees float64 `json:"limitDegrees"`

	// UncalibratedWindowCounts is the limit before calibration, in raw counts
	// either side of straight. Small, because before calibration nobody knows
	// what a count is worth: at 11 counts a degree, 40 counts is under 4°; if
	// the reference's (raw-450)/150 were radians, it would be 15°. Either is
	// safe, and 100 counts would not have been under the second reading.
	UncalibratedWindowCounts int `json:"uncalibratedWindowCounts"`

	// EffortTurnsLeft says what a positive raw effort on the steering motor
	// does to the vehicle: 1 if it turns it left, -1 if right, 0 if nobody has
	// looked yet. The first thing to find out on the vehicle.
	EffortTurnsLeft int `json:"effortTurnsLeft"`

	// StaleMs is how old a reading may be and still steer. It is much shorter
	// than for any other measurement, because it is what the limit stands on.
	StaleMs int `json:"staleMs"`

	// The angle loop: effort in percent per degree of error, its ceiling, and
	// a deadband inside which the joint is left alone. Not tuned on the vehicle.
	GainPercentPerDegree float64 `json:"gainPercentPerDegree"`
	MaxEffortPercent     float64 `json:"maxEffortPercent"`
	DeadbandDegrees      float64 `json:"deadbandDegrees"`

	// The watchdog: effort of at least StallEffortPercent held for StallMs must
	// move the joint by at least StallCounts.
	StallEffortPercent float64 `json:"stallEffortPercent"`
	StallMs            int     `json:"stallMs"`
	StallCounts        int     `json:"stallCounts"`
}

func defaultWaist() WaistConfig {
	return WaistConfig{
		StraightCount:            450,
		LimitDegrees:             35,
		UncalibratedWindowCounts: 40,
		StaleMs:                  200,
		GainPercentPerDegree:     4,
		MaxEffortPercent:         50,
		DeadbandDegrees:          0.5,
		StallEffortPercent:       30,
		StallMs:                  1500,
		StallCounts:              3,
	}
}

func applyWaistDefaults(w *WaistConfig) {
	d := defaultWaist()
	if w.StraightCount == 0 {
		w.StraightCount = d.StraightCount
	}
	if w.LimitDegrees <= 0 {
		w.LimitDegrees = d.LimitDegrees
	}
	if w.UncalibratedWindowCounts <= 0 {
		w.UncalibratedWindowCounts = d.UncalibratedWindowCounts
	}
	if w.StaleMs <= 0 {
		w.StaleMs = d.StaleMs
	}
	if w.GainPercentPerDegree <= 0 {
		w.GainPercentPerDegree = d.GainPercentPerDegree
	}
	if w.MaxEffortPercent <= 0 {
		w.MaxEffortPercent = d.MaxEffortPercent
	}
	if w.DeadbandDegrees <= 0 {
		w.DeadbandDegrees = d.DeadbandDegrees
	}
	if w.StallEffortPercent <= 0 {
		w.StallEffortPercent = d.StallEffortPercent
	}
	if w.StallMs <= 0 {
		w.StallMs = d.StallMs
	}
	if w.StallCounts <= 0 {
		w.StallCounts = d.StallCounts
	}
}

// calibrated reports whether the reading can be turned into degrees.
func (w WaistConfig) calibrated() bool {
	return w.CalibrationDegrees != 0 && w.CalibrationCount != w.StraightCount
}

// countsPerDegree is signed: negative when the count falls as the joint turns
// left.
func (w WaistConfig) countsPerDegree() float64 {
	return float64(w.CalibrationCount-w.StraightCount) / w.CalibrationDegrees
}

// degrees is the articulation, positive to the left. Only meaningful when
// calibrated.
func (w WaistConfig) degrees(raw int) float64 {
	return float64(raw-w.StraightCount) / w.countsPerDegree()
}

// leftward is the direction the count moves as the joint turns left: the
// calibration's answer when there is one, and until then the reference's
// convention, that lower counts are to the left. Guessed or not, the watchdog
// checks it against what the joint actually does.
func (w WaistConfig) leftward() int {
	if w.calibrated() {
		return sign(w.countsPerDegree())
	}
	return -1
}

// beyond says whether a reading is past the limit: 1 beyond it on the left, -1
// on the right, 0 within it.
func (w WaistConfig) beyond(raw int) int {
	if w.calibrated() {
		deg := w.degrees(raw)
		switch {
		case deg > w.LimitDegrees:
			return 1
		case deg < -w.LimitDegrees:
			return -1
		}
		return 0
	}
	offset := raw - w.StraightCount
	if abs(offset) <= w.UncalibratedWindowCounts {
		return 0
	}
	// Which side of straight this is, in the vehicle's terms.
	return sign(float64(offset)) * w.leftward()
}

// waistState is the guard and the watchdog's memory.
type waistState struct {
	cfg WaistConfig

	fault string // set by the watchdog; steering is refused until it is cleared

	watching   int // the sign of the effort being watched, 0 when none
	watchSince time.Time
	watchFrom  int

	reported map[string]time.Time // for saying things once in a while, not every cycle
}

func newWaistState(cfg WaistConfig) *waistState {
	return &waistState{cfg: cfg, reported: make(map[string]time.Time)}
}

// guard is the effort, in percent with positive to the left, that may actually
// be applied this cycle, and why not if it is less.
func (s *waistState) guard(effort float64, raw int, fresh bool) (float64, string) {
	if effort == 0 {
		return 0, ""
	}
	if s.fault != "" {
		return 0, s.fault
	}
	if !fresh {
		return 0, "no fresh reading from the waist sensor — steering is not driven blind"
	}
	side := s.cfg.beyond(raw)
	if side == 0 {
		return effort, ""
	}
	if s.cfg.EffortTurnsLeft == 0 {
		return 0, "the waist is past its limit and effortTurnsLeft is not set, so there is no telling which way is back"
	}
	if sign(effort) == side {
		return 0, fmt.Sprintf("the waist is past its limit on the %s; only effort back towards straight is allowed", sideName(side))
	}
	return effort, ""
}

// watch compares the effort applied with what the joint did, and reports a
// fault when they disagree. applied is in percent, positive to the left.
func (s *waistState) watch(applied float64, raw int, fresh bool, now time.Time) (fault string, observation string) {
	if !fresh || math.Abs(applied) < s.cfg.StallEffortPercent {
		s.watching = 0
		return "", ""
	}
	dir := sign(applied)
	if dir != s.watching {
		s.watching, s.watchSince, s.watchFrom = dir, now, raw
		return "", ""
	}
	held := now.Sub(s.watchSince)
	if held < time.Duration(s.cfg.StallMs)*time.Millisecond {
		return "", ""
	}
	moved := raw - s.watchFrom
	s.watchSince, s.watchFrom = now, raw

	if abs(moved) < s.cfg.StallCounts {
		return fmt.Sprintf("steering fault: %.0f%% effort for %d ms moved the waist %d counts — "+
			"check the chain, the waist motor and the sensor", applied, held.Milliseconds(), moved), ""
	}
	if s.cfg.EffortTurnsLeft == 0 {
		return "", fmt.Sprintf("effort to the %s (raw sign %+d) moved the waist count %s, %d to %d; "+
			"if the vehicle turned left, set effortTurnsLeft to %d, otherwise to %d",
			sideName(dir), dir, upDown(moved), raw-moved, raw, dir, -dir)
	}
	// Effort to the left should move the count in the leftward direction.
	if sign(float64(moved)) != dir*s.cfg.leftward() {
		return fmt.Sprintf("steering fault: effort to the %s moved the waist count %s, the opposite of what "+
			"effortTurnsLeft and the calibration say — one of them is wrong, and with it the limit", sideName(dir), upDown(moved)), ""
	}
	return "", ""
}

// angleEffort is the angle loop: the effort, in percent with positive to the
// left, that moves the joint from measured towards target, both in degrees.
func (s *waistState) angleEffort(target, measured float64) float64 {
	err := target - measured
	if math.Abs(err) < s.cfg.DeadbandDegrees {
		return 0
	}
	e := s.cfg.GainPercentPerDegree * err
	return math.Max(-s.cfg.MaxEffortPercent, math.Min(s.cfg.MaxEffortPercent, e))
}

// rawEffortSign is what to multiply an effort towards the left by to get the
// sign the motor wants. Until someone has looked, positive is sent as positive.
func (s *waistState) rawEffortSign() float64 {
	if s.cfg.EffortTurnsLeft < 0 {
		return -1
	}
	return 1
}

// every reports whether key has not been said for at least interval, and
// notes that it is being said now.
func (s *waistState) every(key string, interval time.Duration, now time.Time) bool {
	if last, ok := s.reported[key]; ok && now.Sub(last) < interval {
		return false
	}
	s.reported[key] = now
	return true
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sideName(s int) string {
	if s > 0 {
		return "left"
	}
	return "right"
}

func upDown(moved int) string {
	if moved > 0 {
		return "up"
	}
	return "down"
}
