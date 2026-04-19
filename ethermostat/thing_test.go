/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// TestGetSetPoint verifies that getSetPoint returns a form with the correct Value and Unit.
func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 21.5}
	f := tr.getSetPoint()
	if f.Value != 21.5 {
		t.Errorf("expected Value 21.5, got %f", f.Value)
	}
	if f.Unit != "Celsius" {
		t.Errorf("expected Unit \"Celsius\", got %q", f.Unit)
	}
}

// TestSetSetPoint verifies that setSetPoint updates SetPt.
func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 20.0, name: "KitchenHeater"}
	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 22.0
	f.Unit = "Celsius"
	f.Timestamp = time.Now()
	tr.setSetPoint(f)
	if tr.SetPt != 22.0 {
		t.Errorf("expected SetPt 22.0, got %f", tr.SetPt)
	}
}

// TestGetError verifies that getError returns a form with the correct deviation.
func TestGetError(t *testing.T) {
	tr := &Traits{deviation: -2.0}
	f := tr.getError()
	if f.Value != -2.0 {
		t.Errorf("expected Value -2.0, got %f", f.Value)
	}
	if f.Unit != "Celsius" {
		t.Errorf("expected Unit \"Celsius\", got %q", f.Unit)
	}
}

// TestGetJitter verifies that getJitter returns the jitter in milliseconds.
func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 37 * time.Millisecond}
	f := tr.getJitter()
	if f.Value != 37.0 {
		t.Errorf("expected Value 37.0, got %f", f.Value)
	}
	if f.Unit != "millisecond" {
		t.Errorf("expected Unit \"millisecond\", got %q", f.Unit)
	}
}

// TestCalculateOutput tests the P-controller clamped to [0, 100].
func TestCalculateOutput(t *testing.T) {
	cases := []struct {
		name     string
		kp       float64
		diff     float64
		expected float64
	}{
		{"clamp high: Kp=5 diff=10 → 100", 5, 10, 100},
		{"neutral: Kp=5 diff=0 → 50", 5, 0, 50},
		{"clamp low: Kp=5 diff=-20 → 0", 5, -20, 0},
		{"proportional: Kp=1 diff=5 → 55", 1, 5, 55},
		{"temp above setpoint: Kp=5 diff=-2 → 40", 5, -2, 40},
		{"temp below setpoint: Kp=5 diff=2 → 60", 5, 2, 60},
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

// TestCalculateOutput_BooleanThreshold verifies the ON/OFF threshold at output=50.
func TestCalculateOutput_BooleanThreshold(t *testing.T) {
	tr := &Traits{Kp: 5}
	// diff=0 → output=50 → OFF (not > 50)
	if tr.calculateOutput(0) > 50 {
		t.Error("expected output=50 to map to OFF (not > 50)")
	}
	// diff=0.1 → output=50.5 → ON
	if !(tr.calculateOutput(0.1) > 50) {
		t.Error("expected output=50.5 to map to ON (> 50)")
	}
}

// TestExtractLocation verifies that the "Heater" suffix is stripped correctly.
func TestExtractLocation(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"KitchenHeater", "Kitchen"},
		{"DiningRoomHeater", "DiningRoom"},
		{"BathroomHeater", "Bathroom"},
		{"Heater", ""},
	}
	for _, tc := range cases {
		got := extractLocation(tc.input)
		if got != tc.expected {
			t.Errorf("extractLocation(%q): expected %q, got %q", tc.input, tc.expected, got)
		}
	}
}

// TestSelectTempNode_ExactMatch verifies that a node with a matching FunctionalLocation is preferred.
func TestSelectTempNode_ExactMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Källkälchen (Indoor)"}}},
			{URL: "http://host/meteorologue/KitchenModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Kitchen (Indoor)"}}},
		},
	}
	sysNode, ni, ok := selectTempNode(nodes, "Kitchen")
	if !ok {
		t.Fatal("expected a match, got none")
	}
	if sysNode != "meteorologue" {
		t.Errorf("expected sysNode 'meteorologue', got %q", sysNode)
	}
	if ni.URL != "http://host/meteorologue/KitchenModule/temperature" {
		t.Errorf("unexpected URL: %s", ni.URL)
	}
}

// TestSelectTempNode_ModuleNameMatch verifies that ModuleName is used when FunctionalLocation has no match.
func TestSelectTempNode_ModuleNameMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Indoor"},
			}},
			{URL: "http://host/meteorologue/IndoorModule2/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Bathroom"},
			}},
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Outdoor"},
			}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "Bathroom")
	if !ok {
		t.Fatal("expected ModuleName match, got none")
	}
	if ni.URL != "http://host/meteorologue/IndoorModule2/temperature" {
		t.Errorf("expected Bathroom module URL, got %s", ni.URL)
	}
}

// TestSelectTempNode_IndoorPreferredOverOutdoor verifies that the fallback prefers indoor nodes.
func TestSelectTempNode_IndoorPreferredOverOutdoor(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{
				"ModuleName": {"Outdoor"},
			}},
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"ModuleName": {"Indoor"},
			}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "Kitchen")
	if !ok {
		t.Fatal("expected fallback match, got none")
	}
	if strings.Contains(ni.URL, "Outdoor") {
		t.Errorf("expected indoor fallback, got outdoor URL: %s", ni.URL)
	}
}

// TestSelectTempNode_Fallback verifies that the first available node is used when no location matches.
func TestSelectTempNode_Fallback(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Källkälchen (Indoor)"}}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "DiningRoom")
	if !ok {
		t.Fatal("expected fallback match, got none")
	}
	if ni.URL != "http://host/meteorologue/IndoorModule/temperature" {
		t.Errorf("unexpected fallback URL: %s", ni.URL)
	}
}

// TestSelectTempNode_Empty verifies that an empty nodes map returns not-found.
func TestSelectTempNode_Empty(t *testing.T) {
	_, _, ok := selectTempNode(map[string][]components.NodeInfo{}, "Kitchen")
	if ok {
		t.Error("expected not-found for empty nodes map")
	}
}

// TestSetpt_GET verifies the setpoint handler returns 200 for GET.
func TestSetpt_GET(t *testing.T) {
	tr := &Traits{SetPt: 20.0}
	req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
	w := httptest.NewRecorder()
	tr.setpt(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestSetpt_InvalidMethod verifies the setpoint handler returns 405 for DELETE.
func TestSetpt_InvalidMethod(t *testing.T) {
	tr := &Traits{SetPt: 20.0}
	req := httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
	w := httptest.NewRecorder()
	tr.setpt(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestDiff_GET verifies the thermalerror handler returns 200 for GET.
func TestDiff_GET(t *testing.T) {
	tr := &Traits{deviation: 1.0}
	req := httptest.NewRequest(http.MethodGet, "/thermalerror", nil)
	w := httptest.NewRecorder()
	tr.diff(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestDiff_InvalidMethod verifies the thermalerror handler returns 405 for POST.
func TestDiff_InvalidMethod(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodPost, "/thermalerror", nil)
	w := httptest.NewRecorder()
	tr.diff(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestVariations_GET verifies the jitter handler returns 200 for GET.
func TestVariations_GET(t *testing.T) {
	tr := &Traits{jitter: 5 * time.Millisecond}
	req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
	w := httptest.NewRecorder()
	tr.variations(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestVariations_InvalidMethod verifies the jitter handler returns 405 for DELETE.
func TestVariations_InvalidMethod(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodDelete, "/jitter", nil)
	w := httptest.NewRecorder()
	tr.variations(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestServing_InvalidPath verifies that an unknown path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	w := httptest.NewRecorder()
	serving(tr, w, req, "unknown")
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}
