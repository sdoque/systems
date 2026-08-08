/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func TestPercentToRaw(t *testing.T) {
	tests := []struct {
		percent float64
		want    int
	}{
		{0, 0},
		{50, 5000},
		{100, 10000},
		{-5, 0},      // clamped to 0
		{150, 10000}, // clamped to 100 → 10000
	}

	for _, tc := range tests {
		got := PercentToRaw(tc.percent)
		if got != tc.want {
			t.Errorf("PercentToRaw(%v) = %d, want %d", tc.percent, got, tc.want)
		}
	}
}

func TestNormalizeToPercent(t *testing.T) {
	// The implementation does: percent = reading / 100, ignores min/max, clamps to [0,100].
	tests := []struct {
		reading float64
		want    float64
	}{
		{5000, 50.0},
		{0, 0},
		{10001, 100}, // reading/100 = 100.01 → clamped to 100
		{-1, 0},      // reading/100 = -0.01 → clamped to 0
	}

	for _, tc := range tests {
		got := NormalizeToPercent(tc.reading, 0, 100) // min/max ignored by implementation
		if got != tc.want {
			t.Errorf("NormalizeToPercent(%v, 0, 100) = %v, want %v", tc.reading, got, tc.want)
		}
	}
}

func TestAccess_GET(t *testing.T) {
	tr := &Traits{
		serviceChannel: make(chan ServiceTray),
	}

	// Goroutine that acts as the sampleSignal loop: receives the tray and sends back a datum.
	go func() {
		tray := <-tr.serviceChannel
		var f forms.SignalA_v1a
		f.NewForm()
		f.Value = 42.0
		f.Unit = "<http://qudt.org/vocab/unit/PERCENT>"
		tray.SampledDatum <- f
	}()

	req := httptest.NewRequest(http.MethodGet, "/access", nil)
	rr := httptest.NewRecorder()
	tr.access(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("access GET status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAccess_Default(t *testing.T) {
	tr := &Traits{
		serviceChannel: make(chan ServiceTray),
		outputChannel:  make(chan float64),
	}

	req := httptest.NewRequest(http.MethodDelete, "/access", nil)
	rr := httptest.NewRecorder()
	tr.access(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("access DELETE status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// TestInitTemplate verifies the template name, service map, and default trait values.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "LevelSensor_1" {
		t.Errorf("name = %q, want LevelSensor_1", ua.GetName())
	}
	if _, ok := ua.GetServices()["access"]; !ok {
		t.Error("ServicesMap should contain an 'access' service")
	}
	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.Address == "" {
		t.Error("Address default should not be empty")
	}
}

// TestServing_InvalidPath verifies that an unknown service path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/revolutionary/LevelSensor_1/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestConfiguredUnitIsConvertible checks that the level is declared in a unit
// the framework can convert, which is what lets the leveler consume this channel
// at all: the controller asks for its setpoint's unit, and a reading in a name
// QUDT does not know is refused rather than converted.
//
// The payload itself is stamped from the same configured detail — see the unit
// field newResource fills in — so the reading and the record cannot disagree.
func TestConfiguredUnitIsConvertible(t *testing.T) {
	ua := initTemplate()

	declared := ua.Details["Unit"]
	if len(declared) == 0 {
		t.Fatal("the asset declares no unit, so a consumer has nothing to convert from")
	}
	if _, ok := usecases.LookupUnit(declared[0]); !ok {
		t.Errorf("the level is published in %q, which no consumer can convert from", declared[0])
	}

	kinds := ua.Details["QuantityKind"]
	if len(kinds) == 0 {
		t.Error("the asset declares no quantity kind, so a consumer asking for a ratio never finds it")
	}
}

// TestShutdownStopsTheLoopWithoutExiting is the defect this test was written
// for: the reader goroutine called os.Exit(0) when the context was cancelled.
// One channel of one unit asset shutting down took the whole system with it —
// every other asset lost its shutdown, and the cleanup function newResource
// returns never ran. That the test binary survives this test is half the
// assertion; the other half is that sampleSignal actually returns.
func TestShutdownStopsTheLoopWithoutExiting(t *testing.T) {
	tr := &Traits{
		name:           "LevelSensor_1",
		Address:        "InputValue_1",
		serviceChannel: make(chan ServiceTray),
		outputChannel:  make(chan float64),
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		tr.sampleSignal(ctx)
		close(stopped)
	}()

	cancel()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("sampleSignal did not return after the context was cancelled")
	}
}

// TestAWriteFailureDoesNotStopTheLoop is the second half of the same defect: a
// failed write to the process image returned from the loop, so the asset stopped
// sampling and stopped answering every later request. On this machine piTest is
// absent, so the write fails for real.
func TestAWriteFailureDoesNotStopTheLoop(t *testing.T) {
	tr := &Traits{
		name:           "LevelSensor_1",
		Address:        "OutputValue_1",
		unit:           "<http://qudt.org/vocab/unit/PERCENT>",
		Value:          42.0,
		serviceChannel: make(chan ServiceTray),
		outputChannel:  make(chan float64),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.sampleSignal(ctx)

	// A write the hardware cannot take.
	body := `{"value":75.0,"unit":"<http://qudt.org/vocab/unit/PERCENT>","version":"SignalA_v1.0"}`
	put := httptest.NewRequest(http.MethodPut, "/access", strings.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	tr.access(httptest.NewRecorder(), put)

	// The loop must still be there to answer the next caller.
	rec := httptest.NewRecorder()
	tr.access(rec, httptest.NewRequest(http.MethodGet, "/access", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("a GET after a failed write returned %d — the loop stopped serving", rec.Code)
	}
}

// TestRequestsAreRefusedRatherThanHungWhenTheLoopIsGone checks the handoff is
// bounded. The send to serviceChannel is unbuffered, so with no loop attending
// it the handler blocked for the life of the process: a hung request rather than
// a failed one, and one leaked goroutine per caller.
func TestRequestsAreRefusedRatherThanHungWhenTheLoopIsGone(t *testing.T) {
	tr := &Traits{
		name:           "LevelSensor_1",
		serviceChannel: make(chan ServiceTray),
		outputChannel:  make(chan float64),
	}

	answered := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		tr.access(rec, httptest.NewRequest(http.MethodGet, "/access", nil))
		answered <- rec.Code
	}()

	select {
	case code := <-answered:
		if code != http.StatusServiceUnavailable {
			t.Errorf("expected 503 with no loop attending, got %d", code)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the request never returned — the handler is blocked on an unattended channel")
	}
}
