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
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// sweep is one pass of the rangefinder, in the sensor's own frame: angle zero
// is straight ahead and angles grow to the left.
//
// valid travels beside distance rather than being encoded into it. A silence
// and a wall at maximum range are different facts, and every consumer of this
// system depends on being able to tell them apart.
type sweep struct {
	angles    []float64 // degrees
	distances []float64 // metres
	valid     []bool
	taken     time.Time     // when the sweep finished
	duration  time.Duration // how long it took to take
}

// source is where sweeps come from: the sensor over serial, or the simulator.
//
// The interface exists because the SF45/B is at the university and the
// cartographer has to be developed and tested without it. It is not a
// convenience — a mapping system that can only be exercised on hardware is one
// that gets tested rarely and late.
type source interface {
	// sweeps delivers sweeps until the context is cancelled or the source
	// fails. A closed channel means the source has stopped for good.
	sweeps(ctx context.Context) (<-chan sweep, error)
	// setSector narrows or widens the scanned arc, in degrees, centred ahead.
	setSector(degrees float64) error
	close() error
}

//-------------------------------------The simulator

// simulatedSource ray-casts against a set of wall segments from a pose that
// moves along a straight line. It is what makes an end-to-end test possible
// without the vehicle: a corridor, walked at a walking pace.
//
// The default walls are a three-metre-wide hallway with a doorway on one side,
// because a featureless corridor is the case where scan matching is known to
// fail — nothing in a straight, plain corridor tells you how far along it you
// are. A simulator that omits the doorway would flatter the cartographer.
type simulatedSource struct {
	cfg   GuetteurConfig
	walls []segment

	// the virtual vehicle
	x, y, heading float64
	speed         float64 // metres per second

	sector float64
	out    chan sweep
}

type segment struct{ x1, y1, x2, y2 float64 }

func newSimulatedSource(cfg GuetteurConfig) *simulatedSource {
	const halfWidth = 1.5
	const length = 20.0
	return &simulatedSource{
		cfg: cfg,
		walls: []segment{
			{0, -halfWidth, length, -halfWidth},        // right wall
			{0, halfWidth, 8, halfWidth},               // left wall, up to the doorway
			{9.2, halfWidth, length, halfWidth},        // left wall, after the doorway
			{8, halfWidth, 8, halfWidth + 1.2},         // doorway reveal
			{9.2, halfWidth, 9.2, halfWidth + 1.2},     // doorway reveal
			{8, halfWidth + 1.2, 9.2, halfWidth + 1.2}, // back of the alcove
			{length, -halfWidth, length, halfWidth},    // the far end
		},
		x: 1.0, y: 0.0, heading: 0.0,
		speed:  0.35,
		sector: cfg.SectorDegrees,
		out:    make(chan sweep, 2),
	}
}

func (s *simulatedSource) setSector(degrees float64) error {
	s.sector = degrees
	return nil
}

func (s *simulatedSource) close() error { return nil }

func (s *simulatedSource) sweeps(ctx context.Context) (<-chan sweep, error) {
	period := time.Duration(float64(time.Second) / s.cfg.SweepHz)
	go func() {
		defer close(s.out)
		tick := time.NewTicker(period)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.advance(period.Seconds())
				select {
				case s.out <- s.take(period):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return s.out, nil
}

// advance walks the virtual vehicle down the corridor and turns it round at the
// end, so an unattended run keeps producing new views instead of one static one.
func (s *simulatedSource) advance(dt float64) {
	s.x += s.speed * math.Cos(s.heading) * dt
	s.y += s.speed * math.Sin(s.heading) * dt
	if s.x > 18.0 && s.speed > 0 {
		s.speed = -s.speed
	}
	if s.x < 1.0 && s.speed < 0 {
		s.speed = -s.speed
	}
}

func (s *simulatedSource) take(duration time.Duration) sweep {
	n := s.cfg.PointsPerSweep
	sw := sweep{
		angles:    make([]float64, 0, n),
		distances: make([]float64, 0, n),
		valid:     make([]bool, 0, n),
		taken:     time.Now(),
		duration:  duration,
	}
	half := s.sector / 2
	for i := 0; i < n; i++ {
		a := -half + float64(i)*s.sector/float64(n-1)
		d, hit := s.cast(a)
		if hit {
			// A millimetre of noise, which is roughly what the real sensor
			// quotes, so the cartographer is never tuned against perfect data.
			d += rand.NormFloat64() * 0.01
		}
		sw.angles = append(sw.angles, a)
		sw.distances = append(sw.distances, d)
		sw.valid = append(sw.valid, hit)
	}
	return sw
}

// cast returns the distance to the nearest wall along one bearing, and whether
// anything was hit at all within range.
func (s *simulatedSource) cast(angleDeg float64) (float64, bool) {
	theta := s.heading + angleDeg*math.Pi/180
	dx, dy := math.Cos(theta), math.Sin(theta)
	best := math.Inf(1)
	for _, w := range s.walls {
		if d, ok := raySegment(s.x, s.y, dx, dy, w); ok && d < best {
			best = d
		}
	}
	if math.IsInf(best, 1) || best > s.cfg.MaxRange {
		return 0, false
	}
	return best, true
}

// raySegment intersects a ray with a line segment, returning the distance along
// the ray. Standard two-parameter solve; the determinant is zero when the ray
// and the segment are parallel.
func raySegment(px, py, dx, dy float64, w segment) (float64, bool) {
	sx, sy := w.x2-w.x1, w.y2-w.y1
	den := dx*sy - dy*sx
	if math.Abs(den) < 1e-12 {
		return 0, false
	}
	t := ((w.x1-px)*sy - (w.y1-py)*sx) / den
	u := ((w.x1-px)*dy - (w.y1-py)*dx) / den
	if t < 0 || u < 0 || u > 1 {
		return 0, false
	}
	return t, true
}

//-------------------------------------Choosing one

func newSource(cfg GuetteurConfig) (source, error) {
	switch cfg.Source {
	case "simulated", "":
		return newSimulatedSource(cfg), nil
	case "serial":
		return newSerialSource(cfg)
	default:
		return nil, fmt.Errorf("unknown source %q: expected \"serial\" or \"simulated\"", cfg.Source)
	}
}
