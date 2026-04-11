/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// ------------------------------------- initTemplate

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "DraftAsset" {
		t.Errorf("name = %q, want DraftAsset", ua.GetName())
	}
	if _, ok := ua.GetServices()["hello"]; !ok {
		t.Error("ServicesMap should contain a 'hello' service")
	}
	if _, ok := ua.GetServices()["metric"]; !ok {
		t.Error("ServicesMap should contain a 'metric' service")
	}

	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.SampleRate <= 0 {
		t.Errorf("SampleRate = %d, want > 0", tr.SampleRate)
	}
}

// ------------------------------------- sampleMetric

func TestSampleMetric(t *testing.T) {
	v := sampleMetric()
	if v <= 0 {
		t.Errorf("sampleMetric() = %v, want > 0 (there is always at least one goroutine)", v)
	}
}

// ------------------------------------- hello handler

func TestHello_GET(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/drafter/DraftAsset/hello", nil)
	tr.hello(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Hello Integrated World!") {
		t.Errorf("body = %q, want to contain 'Hello Integrated World!'", w.Body.String())
	}
}

func TestHello_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/drafter/DraftAsset/hello", nil)
		tr.hello(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, w.Code)
		}
	}
}

// ------------------------------------- readMetric handler

// TestReadMetric_GET starts a goroutine that acts as sampleLoop:
// it receives an STray and replies with a populated SignalA_v1a.
func TestReadMetric_GET(t *testing.T) {
	tray := make(chan STray, 1)
	tr := &Traits{trayChan: tray}

	// Simulate sampleLoop
	go func() {
		order := <-tray
		var f forms.SignalA_v1a
		f.NewForm()
		f.Value = 7
		f.Unit = "goroutines"
		f.Timestamp = time.Now()
		order.ValueP <- f
	}()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/drafter/DraftAsset/metric", nil)
	tr.readMetric(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestReadMetric_MethodNotAllowed(t *testing.T) {
	tr := &Traits{trayChan: make(chan STray, 1)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/drafter/DraftAsset/metric", nil)
	tr.readMetric(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ------------------------------------- serving dispatcher

func TestServing_Hello(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/drafter/DraftAsset/hello", nil)
	serving(tr, w, r, "hello")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/drafter/DraftAsset/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ------------------------------------- sampleLoop: channel integration

// TestSampleLoop_DeliversValues verifies that sampleLoop populates the tray
// channel with values after the first tick.
func TestSampleLoop_DeliversValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{
		SampleRate: 1,
		trayChan:   make(chan STray),
	}
	go tr.sampleLoop(ctx)

	// Wait for the first sample tick (up to 1.5 s).
	time.Sleep(1200 * time.Millisecond)

	// Ask sampleLoop for the current value.
	order := STray{
		ValueP: make(chan forms.SignalA_v1a, 1),
		Error:  make(chan error, 1),
	}
	select {
	case tr.trayChan <- order:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending to trayChan")
	}

	select {
	case f := <-order.ValueP:
		if f.Value <= 0 {
			t.Errorf("Value = %v, want > 0", f.Value)
		}
		if f.Unit != "goroutines" {
			t.Errorf("Unit = %q, want goroutines", f.Unit)
		}
	case err := <-order.Error:
		t.Fatalf("sampleLoop returned error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply from sampleLoop")
	}
}

// TestSampleLoop_ContextCancel verifies that sampleLoop exits cleanly when
// the context is cancelled (it must close trayChan so callers unblock).
func TestSampleLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	tr := &Traits{
		SampleRate: 1,
		trayChan:   make(chan STray),
	}
	done := make(chan struct{})
	go func() {
		tr.sampleLoop(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// sampleLoop exited — good
	case <-time.After(3 * time.Second):
		t.Fatal("sampleLoop did not exit after context cancellation")
	}
}
