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
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) < tol }

// A chip turned a quarter to the left on the vehicle: what it calls x is the
// vehicle's y.
func TestTheMountTurnsTheChipIntoTheVehicle(t *testing.T) {
	r := Mount{YawDegrees: 90}.rotation()
	v := r.apply([3]float64{1, 0, 0})
	if !near(v[0], 0, 1e-12) || !near(v[1], 1, 1e-12) || !near(v[2], 0, 1e-12) {
		t.Errorf("the chip's x is %v in the vehicle, want 0, 1, 0", v)
	}
	if got := (Mount{}).rotation().apply([3]float64{1, 2, 3}); got != [3]float64{1, 2, 3} {
		t.Errorf("a zero mount moved %v", got)
	}
}

func TestTheGyroscopesZeroIsMeasuredStandingStill(t *testing.T) {
	z := gyroZero{duration: time.Second}
	start := time.Now()
	bias := [3]float64{0.01, -0.02, 0.03}
	for i := 0; i <= 100; i++ {
		z.add(start.Add(time.Duration(i)*10*time.Millisecond), bias, "test")
	}
	zero, known := z.value()
	if !known || !near(zero[0], 0.01, 1e-12) || !near(zero[2], 0.03, 1e-12) {
		t.Errorf("zero %v (known %v), want %v", zero, known, bias)
	}

	// Turning at 20 °/s for half the second and not for the other half: moved.
	m := gyroZero{duration: time.Second}
	for i := 0; i <= 100; i++ {
		g := [3]float64{}
		if i%2 == 0 {
			g[2] = 20 * math.Pi / 180
		}
		m.add(start.Add(time.Duration(i)*10*time.Millisecond), g, "test")
	}
	if _, known := m.value(); known {
		t.Error("a zero was taken while the vehicle turned")
	}

	if _, known := (&gyroZero{}).value(); !known {
		t.Error("with no measurement asked for, the rate was withheld")
	}
}

// The mean is of every sample, the field only of the good ones, and stamped
// in the middle of the window.
func TestAWindowAverages(t *testing.T) {
	var w window
	start := time.Now()
	w.add(start, rawSample{accel: [3]float64{0, 0, 9}, tempC: 30, mag: [3]float64{10, 0, 0}, magGood: true})
	w.add(start.Add(10*time.Millisecond), rawSample{accel: [3]float64{0, 0, 11}, tempC: 32, mag: [3]float64{999, 0, 0}})
	w.add(start.Add(20*time.Millisecond), rawSample{accel: [3]float64{0, 0, 10}, tempC: 31, mag: [3]float64{20, 0, 0}, magGood: true})
	m, at := w.mean()
	if m.accel[2] != 10 || m.tempC != 31 {
		t.Errorf("means %v and %v", m.accel, m.tempC)
	}
	if !m.magGood || m.mag[0] != 15 {
		t.Errorf("the field %v (good %v) took in a reading that was not good", m.mag, m.magGood)
	}
	if got := at.Sub(start); got != 10*time.Millisecond {
		t.Errorf("stamped %v after the first sample, want 10ms", got)
	}
}

// Standing level, the accelerometer reads gravity up; until the gyroscope's
// zero is known its rate is withheld rather than served with its bias.
func TestWhatIsNotKnownIsNotServed(t *testing.T) {
	cfg := defaultConfig()
	tr := newTraits(cfg)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serving(tr, w, httptest.NewRequest(http.MethodGet, "/spiker/imu/"+path, nil), path)
		return w
	}
	if code := get("acceleration").Code; code != http.StatusServiceUnavailable {
		t.Errorf("with no reading yet, acceleration answered %d", code)
	}

	tr.store(rawSample{accel: [3]float64{0, 0, standardGravity}, gyro: [3]float64{0.01, 0, 0}, tempC: 25}, time.Now())
	w := get("acceleration")
	if w.Code != http.StatusOK {
		t.Fatalf("acceleration answered %d: %s", w.Code, w.Body)
	}
	var v forms.VectorA_v1a
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Check() != nil || v.Frame != forms.VectorVehicle || !near(v.Z, standardGravity, 1e-9) || v.Unit != unitAccel {
		t.Errorf("served %+v", v)
	}
	if code := get("angularvelocity").Code; code != http.StatusServiceUnavailable {
		t.Errorf("the rate was served before its zero was measured (%d)", code)
	}
	if code := get("magneticfield").Code; code != http.StatusServiceUnavailable {
		t.Errorf("a field was served with no good magnetometer reading (%d)", code)
	}
	if code := get("dietemperature").Code; code != http.StatusOK {
		t.Errorf("the die temperature answered %d", code)
	}

	tr.store(rawSample{accel: [3]float64{0, 0, standardGravity}}, time.Now().Add(-2*time.Second))
	if code := get("acceleration").Code; code != http.StatusServiceUnavailable {
		t.Errorf("a reading two seconds old was served (%d)", code)
	}
}

type steadyChip struct{ s rawSample }

func (c steadyChip) sample() (rawSample, error) { return c.s, nil }

// The loop reads, averages and keeps the reading, with the zero taken off.
func TestTheLoopKeepsAReading(t *testing.T) {
	cfg := defaultConfig()
	cfg.GyroZeroSeconds = 0.05
	cfg.AverageMs = 20
	tr := newTraits(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx, steadyChip{rawSample{accel: [3]float64{0, 0, standardGravity}, gyro: [3]float64{0.02, 0, 0}, magGood: true, mag: [3]float64{20, 0, -40}}}, "test")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		r := tr.now
		tr.mu.Unlock()
		if r.gyroOK {
			if !near(r.gyro[0], 0, 1e-12) || !near(r.accel[2], standardGravity, 1e-9) || r.mag[2] != -40 {
				t.Errorf("reading %+v", r)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no reading with a known zero within two seconds")
}
