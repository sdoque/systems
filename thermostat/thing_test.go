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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// TestGetSetPoint verifies that getSetPoint returns a form with the correct
// Value and Unit fields.
func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 22.5}
	f := tr.getSetPoint()

	if f.Value != 22.5 {
		t.Errorf("expected Value 22.5, got %f", f.Value)
	}
	if f.Unit != "Celsius" {
		t.Errorf("expected Unit \"Celsius\", got %q", f.Unit)
	}
}

// TestSetSetPoint verifies that setSetPoint updates the SetPt field.
func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 20.0}

	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 24.0
	f.Unit = "Celsius"
	f.Timestamp = time.Now()

	tr.setSetPoint(f)

	if tr.SetPt != 24.0 {
		t.Errorf("expected SetPt 24.0, got %f", tr.SetPt)
	}
}

// TestGetError verifies that getError returns a form with the correct Value
// and Unit fields.
func TestGetError(t *testing.T) {
	tr := &Traits{deviation: -1.5}
	f := tr.getError()

	if f.Value != -1.5 {
		t.Errorf("expected Value -1.5, got %f", f.Value)
	}
	if f.Unit != "Celsius" {
		t.Errorf("expected Unit \"Celsius\", got %q", f.Unit)
	}
}

// TestGetJitter verifies that getJitter returns a form whose Unit is
// "millisecond".
func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 42 * time.Millisecond}
	f := tr.getJitter()

	if f.Unit != "millisecond" {
		t.Errorf("expected Unit \"millisecond\", got %q", f.Unit)
	}
	if f.Value != 42.0 {
		t.Errorf("expected Value 42, got %f", f.Value)
	}
}

// TestCalculateOutput is a table-driven test for the P-controller output
// clamped to [0, 100].
func TestCalculateOutput(t *testing.T) {
	cases := []struct {
		name     string
		kp       float64
		diff     float64
		expected float64
	}{
		{"clamp high: Kp=5 diff=10 -> 100", 5, 10, 100},
		{"neutral: Kp=5 diff=0 -> 50", 5, 0, 50},
		{"clamp low: Kp=5 diff=-20 -> 0", 5, -20, 0},
		{"proportional: Kp=1 diff=5 -> 55", 1, 5, 55},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tr := &Traits{Kp: tc.kp}
			got := tr.calculateOutput(tc.diff)
			if got != tc.expected {
				t.Errorf("calculateOutput(%f) with Kp=%f: expected %f, got %f",
					tc.diff, tc.kp, tc.expected, got)
			}
		})
	}
}

// TestSetpt verifies that the setpt handler returns 200 for GET and 404 for
// an unsupported method (DELETE).
func TestSetpt(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{SetPt: 21.0}
		req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
		w := httptest.NewRecorder()
		tr.setpt(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{SetPt: 21.0}
		req := httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
		w := httptest.NewRecorder()
		tr.setpt(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
}

// TestDiff verifies that the diff handler returns 200 for GET and 404 for an
// unsupported method (DELETE).
func TestDiff(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{deviation: 0.5}
		req := httptest.NewRequest(http.MethodGet, "/thermalerror", nil)
		w := httptest.NewRecorder()
		tr.diff(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{}
		req := httptest.NewRequest(http.MethodDelete, "/thermalerror", nil)
		w := httptest.NewRecorder()
		tr.diff(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
}

// TestVariations verifies that the variations handler returns 200 for GET and
// 404 for an unsupported method (DELETE).
func TestVariations(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{jitter: 5 * time.Millisecond}
		req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
		w := httptest.NewRecorder()
		tr.variations(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{}
		req := httptest.NewRequest(http.MethodDelete, "/jitter", nil)
		w := httptest.NewRecorder()
		tr.variations(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
}
