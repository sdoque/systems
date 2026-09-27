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
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// testMap is a map in the cartographer's form, drawn by hand: everything
// unseen until a test says otherwise.
type testMap struct{ m forms.MapA_v1a }

func newTestMap(widthM, heightM float64) *testMap {
	const res = 0.05
	w, h := int(widthM/res), int(heightM/res)
	t := &testMap{m: forms.MapA_v1a{Width: w, Height: h, Resolution: res, Frame: "map@test"}}
	t.m.Cells = make([]byte, w*h)
	for i := range t.m.Cells {
		t.m.Cells[i] = forms.MapUnknown
	}
	return t
}

func (t *testMap) fill(x0, y0, x1, y1 float64, v byte) {
	r := t.m.Resolution
	for j := int(y0 / r); j < int(y1/r) && j < t.m.Height; j++ {
		for i := int(x0 / r); i < int(x1/r) && i < t.m.Width; i++ {
			if i >= 0 && j >= 0 {
				t.m.Cells[j*t.m.Width+i] = v
			}
		}
	}
}

// corridor is a free rectangle with a wall all round it.
func (t *testMap) corridor(x0, y0, x1, y1 float64) {
	const wall = 0.1
	t.fill(x0-wall, y0-wall, x1+wall, y1+wall, forms.MapOccupied)
	t.fill(x0, y0, x1, y1, forms.MapFree)
}

var artitrax = Vehicle{MaxCurvature: 0.5, Clearance: 0.4}

// checkDrivable asserts what every route must be: forward only, never tighter
// than the vehicle turns, and ending where the plan says it ends.
func checkDrivable(t *testing.T, p *Plan, v Vehicle) {
	t.Helper()
	if len(p.X) < 2 {
		t.Fatalf("a route of %d poses", len(p.X))
	}
	maxTurn := v.MaxCurvature*stepFactor*0.1 + 1e-6 // the vehicle's own limit, over one 0.15 m step
	for i := 1; i < len(p.X); i++ {
		dx, dy := p.X[i]-p.X[i-1], p.Y[i]-p.Y[i-1]
		along := dx*math.Cos(p.Heading[i-1]) + dy*math.Sin(p.Heading[i-1])
		if along <= 0 {
			t.Fatalf("pose %d goes backwards", i)
		}
		if turn := math.Abs(wrap(p.Heading[i] - p.Heading[i-1])); turn > maxTurn {
			t.Fatalf("pose %d turns %.1f° in one step, tighter than the vehicle can", i, turn*180/math.Pi)
		}
	}
	end := len(p.X) - 1
	if d := math.Hypot(p.X[end]-p.GoalX, p.Y[end]-p.GoalY); d > 0.3 {
		t.Errorf("the route ends %.2f m from where the plan says it ends", d)
	}
}

func length(p *Plan) float64 {
	l := 0.0
	for i := 1; i < len(p.X); i++ {
		l += math.Hypot(p.X[i]-p.X[i-1], p.Y[i]-p.Y[i-1])
	}
	return l
}

func TestStraightCorridor(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	checkDrivable(t, p, artitrax)
	if p.Adjusted {
		t.Errorf("a reachable goal was adjusted: %s", p.Why)
	}
	if l := length(p); l > 18 {
		t.Errorf("17 m down a straight corridor took a %.1f m route", l)
	}
}

// The case that matters at LTU: the destination is behind the vehicle, the
// corridors loop, and the vehicle cannot turn round. It goes on round.
func TestBehindIsReachedByGoingRound(t *testing.T) {
	tm := newTestMap(20, 14)
	// A ring: outer 1..19 x 1..13, a block in the middle, corridors 2 m wide.
	tm.fill(0.9, 0.9, 19.1, 13.1, forms.MapOccupied)
	tm.fill(1, 1, 19, 13, forms.MapFree)
	tm.fill(3, 3, 17, 11, forms.MapOccupied)

	// On the bottom leg, facing +x; the goal is 4 m behind.
	p, err := plan(&tm.m, 0.1, artitrax, 10, 2, 0, 6, 2)
	if err != nil {
		t.Fatalf("no route round the loop: %v", err)
	}
	checkDrivable(t, p, artitrax)
	if l := length(p); l < 30 {
		t.Errorf("a goal 4 m behind was reached in %.1f m — that is not going round a 16 x 12 m loop", l)
	}
	t.Logf("4 m behind, reached going round: %.1f m, %d states", length(p), p.Expanded)
}

// With no loop, a goal behind cannot be reached forward, and the planner must
// say so rather than invent a maneuver.
func TestBehindInADeadEndIsRefused(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	_, err := plan(&tm.m, 0.1, artitrax, 12, 3, 0, 4, 3)
	if err == nil || !strings.Contains(err.Error(), "forward") {
		t.Errorf("a goal behind in a dead end: %v, want a refusal naming the forward-only limit", err)
	}
}

func TestAGoalOnAWallIsMovedAndExplained(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 15, 4.05) // on the upper wall
	if err != nil {
		t.Fatal(err)
	}
	if !p.Adjusted || !strings.Contains(p.Why, "obstacle") {
		t.Errorf("goal on a wall: adjusted=%v why=%q", p.Adjusted, p.Why)
	}
	if math.Abs(p.GoalX-15) > 0.5 {
		t.Errorf("moved to x=%.2f, not the nearest reachable point to x=15", p.GoalX)
	}
	checkDrivable(t, p, artitrax)
}

func TestAGoalOutsideTheMapIsMovedAndExplained(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 40, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Adjusted || !strings.Contains(p.Why, "outside") {
		t.Errorf("goal outside the map: adjusted=%v why=%q", p.Adjusted, p.Why)
	}
	if p.GoalX < 19 {
		t.Errorf("moved to x=%.2f, not the end of the corridor", p.GoalX)
	}
}

func TestAGoalInUnseenGroundIsMovedAndExplained(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 12, 4) // the rest of the corridor has not been seen
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 18, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Adjusted || !strings.Contains(p.Why, "never seen") {
		t.Errorf("goal in unseen ground: adjusted=%v why=%q", p.Adjusted, p.Why)
	}
}

// The scanner cannot see the ground under itself, so the vehicle's own
// position is often unseen. That must not make it a wall.
func TestTheGroundUnderTheVehicleIsFree(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	tm.fill(1.8, 2.8, 2.2, 3.2, forms.MapUnknown)
	if _, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3); err != nil {
		t.Errorf("a vehicle standing on unseen ground could not start: %v", err)
	}
}

// A gap narrower than the vehicle's clearance is not a way through. The goal
// beyond it is refused, and the route goes as near as it can.
func TestTooNarrowIsNotAWayThrough(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	tm.fill(9, 2, 11, 3.5, forms.MapOccupied) // leaves a 0.5 m gap
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Adjusted || !strings.Contains(p.Why, "not connected") {
		t.Errorf("goal beyond a 0.5 m gap: adjusted=%v why=%q", p.Adjusted, p.Why)
	}
	for i := range p.X {
		if p.X[i] > 9 {
			t.Fatalf("the route reaches x=%.2f, into the gap", p.X[i])
		}
	}
	checkDrivable(t, p, artitrax)
}

// Far from the scanner the map is striped: seen along each ray, unseen between
// them. The stripes must not read as walls, or nothing beyond a few meters can
// be reached.
func TestStripesBetweenRaysAreNotWalls(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	// Unseen every other map cell along x, beyond 5 m, as sparse rays leave it.
	r := tm.m.Resolution
	for j := int(2 / r); j < int(4/r); j++ {
		for i := int(5 / r); i < int(21/r); i += 2 {
			tm.m.Cells[j*tm.m.Width+i] = forms.MapUnknown
		}
	}
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.Adjusted {
		t.Errorf("a striped corridor stopped the route: %s (at x=%.2f)", p.Why, p.GoalX)
	}
}

// Wider stripes, as far from the scanner: seen one map cell in three. The gaps
// between rays are bridged; unseen ground with nothing seen beyond it is not.
func TestGapsBetweenRaysAreBridgedButNotTheUnknown(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	r := tm.m.Resolution
	for j := int(2 / r); j < int(4/r); j++ {
		for i := int(5 / r); i < int(21/r); i++ {
			if i%6 != 0 { // seen one column in six: 30 cm apart
				tm.m.Cells[j*tm.m.Width+i] = forms.MapUnknown
			}
		}
	}
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.Adjusted {
		t.Errorf("30 cm between rays stopped the route: %s (at x=%.2f)", p.Why, p.GoalX)
	}

	// Beyond the last ray nothing has been seen, and nothing is bridged.
	tm2 := newTestMap(22, 6)
	tm2.corridor(1, 2, 21, 4)
	tm2.fill(12, 2, 21, 4, forms.MapUnknown)
	p2, err := plan(&tm2.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Adjusted || p2.GoalX > 12.1 {
		t.Errorf("unseen ground with nothing beyond it was treated as free: goal at x=%.2f, adjusted=%v", p2.GoalX, p2.Adjusted)
	}
}

// A bridge never crosses a wall: a gap in the map is not a gap in the wall.
func TestABridgeDoesNotCrossAWall(t *testing.T) {
	tm := newTestMap(22, 6)
	tm.corridor(1, 2, 21, 4)
	tm.fill(10, 2, 10.1, 4, forms.MapOccupied) // a wall across the corridor
	p, err := plan(&tm.m, 0.1, artitrax, 2, 3, 0, 19, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.X {
		if p.X[i] > 10 {
			t.Fatalf("the route crossed the wall at x=%.2f", p.X[i])
		}
	}
}
