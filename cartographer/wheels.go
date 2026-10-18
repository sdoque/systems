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
	"errors"
	"fmt"
	"sync"
	"time"
)

// The wheels at the moment of the sweep.
//
// A sweep is stamped when the guetteur finished it, and the wheels used to be
// read whenever the cartographer got round to processing it: a few tens of
// milliseconds later on the vehicle, and anything up to a second later over
// Wi-Fi, varying from one sweep to the next. The motion between two sweeps was
// therefore the motion between two other, jittering instants. Along a corridor,
// where the wheels alone say how far the vehicle went, that error went straight
// into the map, and more of it the faster the vehicle drove.
//
// So the wheels are read often, each reading is kept with the time the loader
// received its encoder frame, and the travel at the sweep's own timestamp is
// interpolated between the readings either side of it. This assumes the loader
// and the guetteur agree on the time, which they do when they run on the same
// computer; run NTP when they do not.

// wheelHistory is the recent readings of the two wheels, oldest first.
type wheelHistory struct {
	mu          sync.Mutex
	left, right []wheelSample
	keep        time.Duration
}

func newWheelHistory(keep time.Duration) *wheelHistory {
	return &wheelHistory{keep: keep}
}

// add keeps a pair of readings. A reading that repeats the previous one's
// timestamp is the same encoder frame asked for twice, and adds nothing.
func (h *wheelHistory) add(l, r wheelSample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.left = appendSample(h.left, l, h.keep)
	h.right = appendSample(h.right, r, h.keep)
}

func appendSample(s []wheelSample, w wheelSample, keep time.Duration) []wheelSample {
	if n := len(s); n > 0 && !w.at.After(s[n-1].at) {
		return s
	}
	s = append(s, w)
	cut := 0
	for cut < len(s)-1 && w.at.Sub(s[cut].at) > keep {
		cut++
	}
	return append(s[:0], s[cut:]...)
}

// at is the two wheels' travel at the moment when.
func (h *wheelHistory) at(when time.Time) (wheelSample, wheelSample, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, errL := sampleAt(h.left, when)
	r, errR := sampleAt(h.right, when)
	return l, r, errors.Join(errL, errR)
}

// sampleAt interpolates one wheel's travel at when. Between two readings it is
// the straight line between them. After the newest it is the newest: the
// readings are taken after the sweep, so a wheel with no newer frame has not
// moved since — or its encoder has stopped, which the odometer tells from the
// reading's age. The returned sample keeps the newest reading's timestamp in
// that case for exactly that reason. Before the oldest there is nothing to go
// on.
func sampleAt(s []wheelSample, when time.Time) (wheelSample, error) {
	if len(s) == 0 {
		return wheelSample{}, errors.New("no wheel reading yet")
	}
	if last := s[len(s)-1]; !when.Before(last.at) {
		return last, nil
	}
	if when.Before(s[0].at) {
		return wheelSample{}, fmt.Errorf("the sweep is %d ms older than the oldest wheel reading kept",
			s[0].at.Sub(when).Milliseconds())
	}
	i := 1
	for s[i].at.Before(when) {
		i++
	}
	a, b := s[i-1], s[i]
	f := float64(when.Sub(a.at)) / float64(b.at.Sub(a.at))
	return wheelSample{metres: a.metres + f*(b.metres-a.metres), at: when}, nil
}

// pollWheels reads the wheels once and keeps the readings. The sampler and the
// mapping step both call it, so it is serialized: the wheel cervices are not
// safe to discover from two goroutines at once.
func (t *Traits) pollWheels() error {
	t.pollMu.Lock()
	defer t.pollMu.Unlock()
	l, r, err := t.readWheels()
	if err != nil {
		return err
	}
	t.wheels.add(l, r)
	return nil
}

// sampleWheels reads the wheels every pollMs, so that a sweep finds readings
// close to it on both sides. Its errors are not reported: the mapping step
// polls once more for every sweep and reports what goes wrong there, at a
// rate a person can read.
func (t *Traits) sampleWheels(ctx context.Context) {
	tick := time.NewTicker(time.Duration(t.cfg.Odometry.PollMs) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.pollWheels()
		}
	}
}
