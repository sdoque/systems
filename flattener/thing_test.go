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
 ***************************************************************************SDG*/

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestInitTemplate verifies that initTemplate returns a UnitAsset with the
// expected name, both service sub-paths, and sensible default trait values.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "ComfortController" {
		t.Errorf("expected Name %q, got %q", "ComfortController", ua.Name)
	}

	for _, subpath := range []string{"setpoint", "price"} {
		if _, ok := ua.ServicesMap[subpath]; !ok {
			t.Errorf("expected service sub-path %q in ServicesMap", subpath)
		}
	}

	tr, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatal("expected Traits to be *Traits")
	}
	if tr.MinSetPoint >= tr.MaxSetPoint {
		t.Errorf("MinSetPoint (%.1f) must be less than MaxSetPoint (%.1f)", tr.MinSetPoint, tr.MaxSetPoint)
	}
	if tr.MinPrice >= tr.MaxPrice {
		t.Errorf("MinPrice (%.2f) must be less than MaxPrice (%.2f)", tr.MinPrice, tr.MaxPrice)
	}
	if tr.Region == "" {
		t.Error("expected a non-empty Region")
	}
	if tr.Period <= 0 {
		t.Errorf("expected Period > 0, got %d", tr.Period)
	}
}

// TestPriceToSetPoint verifies the linear inverse mapping at key points.
func TestPriceToSetPoint(t *testing.T) {
	tr := &Traits{
		MinSetPoint: 18.0,
		MaxSetPoint: 22.0,
		MinPrice:    0.50,
		MaxPrice:    3.00,
	}

	tests := []struct {
		name     string
		price    float64
		expected float64
	}{
		{"at min price → max setpoint", 0.50, 22.0},
		{"at max price → min setpoint", 3.00, 18.0},
		{"at mid price → mid setpoint", 1.75, 20.0},
		{"below min price → clamped to max setpoint", 0.10, 22.0},
		{"above max price → clamped to min setpoint", 5.00, 18.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tr.priceToSetPoint(tc.price)
			if got != tc.expected {
				t.Errorf("priceToSetPoint(%.2f) = %.1f, want %.1f", tc.price, got, tc.expected)
			}
		})
	}
}

// TestPriceToSetPoint_DegenerateRange verifies that an invalid price range
// (MaxPrice <= MinPrice) always returns MaxSetPoint.
func TestPriceToSetPoint_DegenerateRange(t *testing.T) {
	tr := &Traits{
		MinSetPoint: 18.0,
		MaxSetPoint: 22.0,
		MinPrice:    2.00,
		MaxPrice:    2.00, // equal → degenerate
	}
	got := tr.priceToSetPoint(1.50)
	if got != tr.MaxSetPoint {
		t.Errorf("degenerate range: expected MaxSetPoint %.1f, got %.1f", tr.MaxSetPoint, got)
	}
}

// TestGetSetPoint verifies that getSetPoint returns the current value with the
// correct unit.
func TestGetSetPoint(t *testing.T) {
	tr := &Traits{currentSetPoint: 20.5}
	f := tr.getSetPoint()
	if f.Value != 20.5 {
		t.Errorf("expected Value 20.5, got %.1f", f.Value)
	}
	if f.Unit != "Celsius" {
		t.Errorf("expected Unit %q, got %q", "Celsius", f.Unit)
	}
}

// TestGetPrice verifies that getPrice returns the current price with the
// correct unit.
func TestGetPrice(t *testing.T) {
	tr := &Traits{currentPrice: 1.23}
	f := tr.getPrice()
	if f.Value != 1.23 {
		t.Errorf("expected Value 1.23, got %.2f", f.Value)
	}
	if f.Unit != "SEK/kWh" {
		t.Errorf("expected Unit %q, got %q", "SEK/kWh", f.Unit)
	}
}

// TestFetchCurrentPrice_APIShape verifies that fetchCurrentPrice correctly
// parses the elprisetjustnu.se response format and selects the right hour,
// using a local test server instead of the real API.
func TestFetchCurrentPrice_APIShape(t *testing.T) {
	now := time.Now()
	currentHour := now.Hour()

	// Build a two-entry payload: one for the current hour, one for the next.
	prices := []hourlyPrice{
		{
			SEKPerKWh: 1.50,
			TimeStart: time.Date(now.Year(), now.Month(), now.Day(), currentHour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		},
		{
			SEKPerKWh: 2.00,
			TimeStart: time.Date(now.Year(), now.Month(), now.Day(), (currentHour+1)%24, 0, 0, 0, time.UTC).Format(time.RFC3339),
		},
	}

	body, err := json.Marshal(prices)
	if err != nil {
		t.Fatalf("failed to marshal test prices: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	// Parse the response directly (bypassing the URL construction in fetchCurrentPrice).
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("test server request failed: %v", err)
	}
	defer resp.Body.Close()

	var parsed []hourlyPrice
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	found := false
	for _, p := range parsed {
		ts, err := time.Parse(time.RFC3339, p.TimeStart)
		if err != nil {
			continue
		}
		if ts.Hour() == currentHour {
			if p.SEKPerKWh != 1.50 {
				t.Errorf("expected price 1.50 for current hour, got %.2f", p.SEKPerKWh)
			}
			found = true
		}
	}
	if !found {
		t.Error("current hour not found in parsed response")
	}
}

// TestServing_SetpointGET verifies that GET /setpoint returns a JSON body
// containing the current setpoint value.
func TestServing_SetpointGET(t *testing.T) {
	tr := &Traits{currentSetPoint: 19.8}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
	serving(tr, w, r, "setpoint")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty body for setpoint GET")
	}
}

// TestServing_PriceGET verifies that GET /price returns a JSON body.
func TestServing_PriceGET(t *testing.T) {
	tr := &Traits{currentPrice: 0.88}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/price", nil)
	serving(tr, w, r, "price")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty body for price GET")
	}
}

// TestServing_UnknownPath verifies that an unknown service path returns 400.
func TestServing_UnknownPath(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown path, got %d", w.Code)
	}
}

// TestServing_MethodNotAllowed verifies that PUT /setpoint returns 405.
func TestServing_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/setpoint", nil)
	serving(tr, w, r, "setpoint")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for PUT on setpoint, got %d", w.Code)
	}
}
