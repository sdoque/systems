/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 42.5}
	f := tr.getSetPoint()

	if f.Value != 42.5 {
		t.Errorf("getSetPoint().Value = %v, want 42.5", f.Value)
	}
	if f.Unit != "Percent" {
		t.Errorf("getSetPoint().Unit = %q, want \"Percent\"", f.Unit)
	}
}

func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 10.0}

	var f forms.SignalA_v1a
	f.Value = 55.0
	tr.setSetPoint(f)

	if tr.SetPt != 55.0 {
		t.Errorf("SetPt after setSetPoint(55) = %v, want 55.0", tr.SetPt)
	}
}

func TestGetError(t *testing.T) {
	tr := &Traits{deviation: 7.3}
	f := tr.getError()

	if f.Value != 7.3 {
		t.Errorf("getError().Value = %v, want 7.3", f.Value)
	}
	if f.Unit != "Percent" {
		t.Errorf("getError().Unit = %q, want \"Percent\"", f.Unit)
	}
}

func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 250 * time.Millisecond}
	f := tr.getJitter()

	if f.Value != 250 {
		t.Errorf("getJitter().Value = %v, want 250", f.Value)
	}
	if f.Unit != "millisecond" {
		t.Errorf("getJitter().Unit = %q, want \"millisecond\"", f.Unit)
	}
}

func TestCalculateOutput(t *testing.T) {
	// Kp=2, Lambda=1, Ki=0, Period=1 (second)
	// decay = exp(-1*1/1) = exp(-1) ≈ 0.368
	// integral updated each call, but iTerm = Ki*integral = 0 always
	// output = pTerm = Kp * levelDiff, clamped to [0,100]

	tr := &Traits{Kp: 2, Lambda: 1, Ki: 0, Period: 1}

	// diff=10 → P-only = 2*10 = 20
	out := tr.calculateOutput(10)
	if out != 20 {
		t.Errorf("calculateOutput(10) = %v, want 20", out)
	}

	// Reset integral for independent sub-tests
	tr.integral = 0

	// diff=100 → 2*100=200, clamped to 100
	out = tr.calculateOutput(100)
	if out != 100 {
		t.Errorf("calculateOutput(100) = %v, want 100 (clamped)", out)
	}

	tr.integral = 0

	// diff=-100 → 2*(-100)=-200, clamped to 0
	out = tr.calculateOutput(-100)
	if out != 0 {
		t.Errorf("calculateOutput(-100) = %v, want 0 (clamped)", out)
	}
}

func TestSetpt(t *testing.T) {
	tr := &Traits{SetPt: 30.0}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
	rr := httptest.NewRecorder()
	tr.setpt(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("setpt GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
	rr = httptest.NewRecorder()
	tr.setpt(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("setpt DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestDiff(t *testing.T) {
	tr := &Traits{deviation: 5.0}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/levelerror", nil)
	rr := httptest.NewRecorder()
	tr.diff(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("diff GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/levelerror", nil)
	rr = httptest.NewRecorder()
	tr.diff(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("diff DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestVariations(t *testing.T) {
	tr := &Traits{jitter: 100 * time.Millisecond}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
	rr := httptest.NewRecorder()
	tr.variations(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("variations GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/jitter", nil)
	rr = httptest.NewRecorder()
	tr.variations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("variations DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
