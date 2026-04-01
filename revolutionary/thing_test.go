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

	"github.com/sdoque/mbaigo/forms"
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
		f.Unit = "Percent"
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
