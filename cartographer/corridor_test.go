/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
	"math/rand"
	"testing"
	"time"
)

// plainCorridor replaces the test walls, for the length of one test, with a
// straight corridor 3 m wide and 100 m long, running at the given angle through
// the origin: nothing in it says how far along it the vehicle is.
func plainCorridor(t *testing.T, angleDeg float64) {
	t.Helper()
	saved := testWalls
	t.Cleanup(func() { testWalls = saved })
	a := angleDeg * math.Pi / 180
	ux, uy := math.Cos(a), math.Sin(a)
	nx, ny := -uy, ux
	testWalls = testWalls[:0:0]
	for _, side := range []float64{1.5, -1.5} {
		testWalls = append(testWalls, struct{ x1, y1, x2, y2 float64 }{
			-50*ux + side*nx, -50*uy + side*ny, 50*ux + side*nx, 50*uy + side*ny})
	}
}

// In a corridor the view fixes the heading and the distance to the walls, and
// not the position along it. The answer takes each from the source that has
// it — whichever way the corridor runs in the map.
func TestTheViewFixesWhatItCanAndTheWheelsTheRest(t *testing.T) {
	plainCorridor(t, 30)
	g := newGrid(40, 40, 0.05)
	g.matchRange = 10
	a := 30 * math.Pi / 180
	ux, uy := math.Cos(a), math.Sin(a)
	nx, ny := -uy, ux

	// The map holds the corridor well beyond the sweep's reach either way, as
	// it does once the vehicle has driven down it: no wall ends in view.
	for s := -12.0; s <= 12; s += 0.5 {
		p := pose{s * ux, s * uy, a}
		an, d, v := sweepFrom(p, 200, 200)
		g.integrate(p, an, d, v)
	}
	truth := pose{0, 0, a}
	an, d, v := sweepFrom(truth, 200, 200)

	// The wheels think the vehicle is 0.2 m further along, 0.1 m to the left
	// and turned 2° more than it is.
	prior := pose{truth.x + 0.2*ux + 0.1*nx, truth.y + 0.2*uy + 0.1*ny, truth.theta + 2*math.Pi/180}
	m := g.match(prior, an, d, v, defaultSearch())
	if m.sharp {
		t.Fatalf("a plain corridor gave a sharp fix at %+v (score %.1f)", m.at, m.score)
	}
	got, ok := g.alongTheCorridor(m.at, prior, an, d, v)
	if !ok {
		t.Fatal("the corridor was not recognized as fixing the heading and the cross position")
	}
	along := (got.x-truth.x)*ux + (got.y-truth.y)*uy
	across := (got.x-truth.x)*nx + (got.y-truth.y)*ny
	heading := wrapAngle(got.theta-truth.theta) * 180 / math.Pi
	t.Logf("along %+.3f m (the wheels said +0.2), across %+.3f m, heading %+.2f°", along, across, heading)
	if math.Abs(along-0.2) > 0.03 {
		t.Errorf("along the corridor %.3f m, want the wheels' 0.2", along)
	}
	if math.Abs(across) > 0.03 {
		t.Errorf("across the corridor %.3f m off; the walls fix that", across)
	}
	if math.Abs(heading) > 0.5 {
		t.Errorf("heading %.2f° off; the walls fix that", heading)
	}
}

// The failure the students saw: wheels of slightly different size, or one
// that slips, turn the odometry's heading steadily. Down a corridor the walls
// must hold the heading and the map stay straight.
func TestTheWallsHoldTheHeadingWhenTheWheelsSlip(t *testing.T) {
	plainCorridor(t, 0)
	const step = 0.07
	const sweeps = 280 // about 20 m
	var left, right float64
	var now time.Time
	tr := mappingTraits(func() (wheelSample, wheelSample, error) {
		return wheelSample{left, now}, wheelSample{right, now}, nil
	})
	tr.g = newGrid(60, 20, 0.05)
	tr.g.matchRange = tr.cfg.MatchRangeMetres

	// The right wheel reads 2 % more than it rolls: about 1.8° of turn per
	// meter, 36° over the run, if nothing corrects it.
	odo := pose{}
	start := time.Now()
	truth := pose{}
	for i := 0; i <= sweeps; i++ {
		now = start.Add(time.Duration(i) * 200 * time.Millisecond)
		tr.consume(scanAt(truth, now))
		truth.x += step
		left += step
		right += step * 1.02
		odo = compose(odo, arc(step, step*1.02, tr.cfg.Odometry.Track))
	}
	got := tr.at
	t.Logf("driven %.1f m: the wheels alone would be at heading %.1f° and %.2f m to the side; "+
		"the map has heading %.2f°, %.3f m to the side, %.3f m along (truth %.3f)",
		truth.x, odo.theta*180/math.Pi, odo.y, got.theta*180/math.Pi, got.y, got.x, truth.x)
	if e := math.Abs(got.theta) * 180 / math.Pi; e > 1 {
		t.Errorf("heading %.2f° after %.1f m; the walls should have held it", e, truth.x)
	}
	if math.Abs(got.y) > 0.10 {
		t.Errorf("%.3f m to the side of the corridor's center line", got.y)
	}
	// Along the corridor only the wheels can say, and they read 1 % long on
	// average.
	if e := math.Abs(got.x - truth.x); e > 0.4 {
		t.Errorf("along the corridor %.3f m off after %.1f m", e, truth.x)
	}
}

// Over Wi-Fi a sweep reaches the cartographer anything from a few to a few
// hundred milliseconds after it was taken. The wheels must be read for the
// moment the sweep was taken, not the moment it arrived, or the jitter becomes
// distance along the corridor.
func TestTheWheelsAreReadForTheMomentOfTheSweep(t *testing.T) {
	plainCorridor(t, 0)
	const speed = 1.0 // m/s, a brisk walk
	const period = 200 * time.Millisecond
	start := time.Now()
	var now time.Time
	travel := func(at time.Time) float64 { return speed * at.Sub(start).Seconds() }
	tr := mappingTraits(func() (wheelSample, wheelSample, error) {
		s := wheelSample{travel(now), now}
		return s, s, nil
	})
	tr.g = newGrid(60, 20, 0.05)
	tr.g.matchRange = tr.cfg.MatchRangeMetres

	// The sampler has been reading the wheels since before the first sweep.
	now = start.Add(-50 * time.Millisecond)
	tr.pollWheels()

	rng := rand.New(rand.NewSource(7))
	worst := 0.0
	var taken time.Time
	for i := 0; i <= 50; i++ {
		taken = start.Add(time.Duration(i) * period)
		now = taken.Add(time.Duration(rng.Int63n(int64(150 * time.Millisecond))))
		tr.consume(scanAt(pose{x: travel(taken)}, taken))
		if e := math.Abs(tr.at.x - travel(taken)); e > worst {
			worst = e
		}
	}
	t.Logf("%.1f m at %.1f m/s, sweeps processed 0–150 ms late: worst error along the corridor %.3f m",
		travel(taken), speed, worst)
	if worst > 0.03 {
		t.Errorf("worst error %.3f m along the corridor; the jitter leaked into the distance", worst)
	}
}

func TestWheelHistory(t *testing.T) {
	t0 := time.Now()
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	h := newWheelHistory(time.Second)
	h.add(wheelSample{1.0, ms(0)}, wheelSample{2.0, ms(0)})
	h.add(wheelSample{1.5, ms(100)}, wheelSample{2.0, ms(0)}) // the right wheel's frame again
	h.add(wheelSample{2.0, ms(200)}, wheelSample{3.0, ms(200)})

	l, r, err := h.at(ms(50))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(l.metres-1.25) > 1e-9 || math.Abs(r.metres-2.25) > 1e-9 || !l.at.Equal(ms(50)) {
		t.Errorf("at 50 ms: left %.3f, right %.3f, stamped %v; want 1.25, 2.25 at 50 ms",
			l.metres, r.metres, l.at.Sub(t0))
	}

	// After the newest reading the wheel has not moved, and the reading keeps
	// its own time, so the odometer can tell an encoder that has stopped.
	l, _, err = h.at(ms(500))
	if err != nil || l.metres != 2.0 || !l.at.Equal(ms(200)) {
		t.Errorf("after the newest: %.3f stamped %v (%v); want 2.0 at 200 ms", l.metres, l.at.Sub(t0), err)
	}

	if _, _, err := h.at(ms(-10)); err == nil {
		t.Error("a sweep older than every reading was given a travel")
	}

	// Readings older than the window are dropped.
	h.add(wheelSample{3, ms(700)}, wheelSample{3, ms(700)})
	h.add(wheelSample{9, ms(1500)}, wheelSample{9, ms(1500)})
	if _, _, err := h.at(ms(600)); err == nil {
		t.Error("a reading beyond the window was kept")
	}
	if _, _, err := h.at(ms(800)); err != nil {
		t.Errorf("the reading inside the window was dropped: %v", err)
	}
}
