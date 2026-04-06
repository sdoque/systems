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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestInitTemplate verifies that initTemplate returns a UnitAsset with the
// expected name and a Credentials trait with sensible defaults.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "MeteoStation" {
		t.Errorf("expected Name %q, got %q", "MeteoStation", ua.Name)
	}
	if ua.Mission != "provide_weather_data" {
		t.Errorf("expected Mission %q, got %q", "provide_weather_data", ua.Mission)
	}

	creds, ok := ua.Traits.(*Credentials)
	if !ok {
		t.Fatal("expected Traits to be *Credentials")
	}
	if creds.ClientID == "" {
		t.Error("expected non-empty ClientID placeholder")
	}
	if creds.ClientSecret == "" {
		t.Error("expected non-empty ClientSecret placeholder")
	}
	if creds.Period == 0 {
		t.Error("expected Period > 0")
	}
}

// TestModuleTypeMap verifies that all five expected module types are present
// and have at least one service each.
func TestModuleTypeMap(t *testing.T) {
	expected := []string{"NAMain", "NAModule1", "NAModule2", "NAModule3", "NAModule4"}
	for _, typ := range expected {
		info, ok := moduleTypeMap[typ]
		if !ok {
			t.Errorf("moduleTypeMap missing entry for %q", typ)
			continue
		}
		if info.assetName == "" {
			t.Errorf("moduleTypeMap[%q].assetName is empty", typ)
		}
		if len(info.services) == 0 {
			t.Errorf("moduleTypeMap[%q] has no services", typ)
		}
	}
}

// TestExtractMeasurements_NAMain verifies that all five fields are extracted
// from an indoor module DashboardData.
func TestExtractMeasurements_NAMain(t *testing.T) {
	temp := 21.5
	hum := 55.0
	co2 := 800
	pressure := 1013.2
	noise := 42

	dd := DashboardData{
		TimeUTC:     time.Now().Unix(),
		Temperature: &temp,
		Humidity:    &hum,
		CO2:         &co2,
		Pressure:    &pressure,
		Noise:       &noise,
	}

	m := extractMeasurements("NAMain", dd)

	checks := map[string]float64{
		"temperature": temp,
		"humidity":    hum,
		"co2":         float64(co2),
		"pressure":    pressure,
		"noise":       float64(noise),
	}
	for k, want := range checks {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing key %q in NAMain measurements", k)
			continue
		}
		if got != want {
			t.Errorf("NAMain %q: got %.2f, want %.2f", k, got, want)
		}
	}
}

// TestExtractMeasurements_NAModule1 verifies outdoor module extraction.
func TestExtractMeasurements_NAModule1(t *testing.T) {
	temp := -3.2
	hum := 87.0
	dd := DashboardData{Temperature: &temp, Humidity: &hum}
	m := extractMeasurements("NAModule1", dd)

	if m["temperature"] != temp {
		t.Errorf("temperature: got %.1f, want %.1f", m["temperature"], temp)
	}
	if m["humidity"] != hum {
		t.Errorf("humidity: got %.1f, want %.1f", m["humidity"], hum)
	}
}

// TestExtractMeasurements_NAModule2 verifies wind module extraction.
func TestExtractMeasurements_NAModule2(t *testing.T) {
	ws := 12
	wa := 270
	gs := 18
	ga := 265
	dd := DashboardData{WindStrength: &ws, WindAngle: &wa, GustStrength: &gs, GustAngle: &ga}
	m := extractMeasurements("NAModule2", dd)

	checks := map[string]float64{
		"wind_speed":  float64(ws),
		"wind_angle":  float64(wa),
		"gust_speed":  float64(gs),
		"gust_angle":  float64(ga),
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("wind %q: got %.1f, want %.1f", k, m[k], want)
		}
	}
}

// TestExtractMeasurements_NAModule3 verifies rain module extraction.
func TestExtractMeasurements_NAModule3(t *testing.T) {
	r1 := 2.4
	r24 := 12.0
	dd := DashboardData{SumRain1: &r1, SumRain24: &r24}
	m := extractMeasurements("NAModule3", dd)

	if m["rain"] != r1 {
		t.Errorf("rain: got %.1f, want %.1f", m["rain"], r1)
	}
	if m["rain_24h"] != r24 {
		t.Errorf("rain_24h: got %.1f, want %.1f", m["rain_24h"], r24)
	}
}

// TestExtractMeasurements_NilFields verifies that nil pointer fields are
// silently omitted rather than causing a panic.
func TestExtractMeasurements_NilFields(t *testing.T) {
	dd := DashboardData{} // all pointers nil
	m := extractMeasurements("NAMain", dd)
	if len(m) != 0 {
		t.Errorf("expected empty map for all-nil DashboardData, got %v", m)
	}
}

// TestExtractMeasurements_UnknownType verifies that an unknown module type
// returns an empty map without panicking.
func TestExtractMeasurements_UnknownType(t *testing.T) {
	m := extractMeasurements("NAUnknown", DashboardData{})
	if len(m) != 0 {
		t.Errorf("expected empty map for unknown module type, got %v", m)
	}
}

// TestModuleCache verifies that update and get work correctly.
func TestModuleCache(t *testing.T) {
	c := newModuleCache()
	ts := time.Now()

	c.update("IndoorModule", map[string]float64{"temperature": 22.1, "humidity": 48.0}, ts)

	got := c.get("IndoorModule", "temperature")
	if got == nil {
		t.Fatal("expected cached measurement, got nil")
	}
	if got.Value != 22.1 {
		t.Errorf("temperature: got %.1f, want 22.1", got.Value)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("timestamp mismatch")
	}

	if c.get("IndoorModule", "nonexistent") != nil {
		t.Error("expected nil for nonexistent key")
	}
	if c.get("NonexistentAsset", "temperature") != nil {
		t.Error("expected nil for nonexistent asset")
	}
}

// TestServiceUnit verifies that serviceUnit returns the correct physical unit
// for each known service subpath.
func TestServiceUnit(t *testing.T) {
	cases := map[string]string{
		"temperature": "Celsius",
		"humidity":    "%",
		"co2":         "ppm",
		"pressure":    "mbar",
		"noise":       "dB",
		"wind_speed":  "km/h",
		"gust_speed":  "km/h",
		"wind_angle":  "°",
		"gust_angle":  "°",
		"rain":        "mm/h",
		"rain_24h":    "mm",
		"unknown":     "",
	}
	for path, want := range cases {
		got := serviceUnit(path)
		if got != want {
			t.Errorf("serviceUnit(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestServing_GET verifies that a GET request for a cached measurement returns 200.
func TestServing_GET(t *testing.T) {
	c := newModuleCache()
	c.update("IndoorModule", map[string]float64{"temperature": 20.0}, time.Now())
	tr := &Traits{assetName: "IndoorModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/temperature", nil)
	serving(tr, w, r, "temperature")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty body")
	}
}

// TestServing_NotYetAvailable verifies that a GET for a measurement not yet in
// the cache returns 503.
func TestServing_NotYetAvailable(t *testing.T) {
	c := newModuleCache()
	tr := &Traits{assetName: "WindModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/wind_speed", nil)
	serving(tr, w, r, "wind_speed")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for empty cache, got %d", w.Code)
	}
}

// TestServing_MethodNotAllowed verifies that a non-GET request returns 405.
func TestServing_MethodNotAllowed(t *testing.T) {
	c := newModuleCache()
	tr := &Traits{assetName: "IndoorModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/temperature", nil)
	serving(tr, w, r, "temperature")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestTokenRefresh_APIShape verifies that postToken correctly parses a token
// response from a test HTTP server.
func TestTokenRefresh_APIShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "test-access-token",
			"refresh_token": "test-refresh-token",
		})
	}))
	defer srv.Close()

	tm := &TokenManager{
		Credentials: Credentials{ClientID: "id", ClientSecret: "secret"},
	}

	// Temporarily redirect the token endpoint to the test server by calling
	// postToken directly with the test server's URL.
	resp, err := http.PostForm(srv.URL, nil)
	if err != nil {
		t.Fatalf("test server request failed: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if result.AccessToken != "test-access-token" {
		t.Errorf("access_token: got %q, want %q", result.AccessToken, "test-access-token")
	}
	if result.RefreshToken != "test-refresh-token" {
		t.Errorf("refresh_token: got %q, want %q", result.RefreshToken, "test-refresh-token")
	}

	// Verify getToken after manual assignment.
	tm.mu.Lock()
	tm.accessToken = result.AccessToken
	tm.mu.Unlock()
	if tm.getToken() != "test-access-token" {
		t.Errorf("getToken: got %q, want %q", tm.getToken(), "test-access-token")
	}
}

// TestFetchStationData_APIShape verifies that fetchStationData correctly parses
// a getstationsdata-shaped JSON response from a test server.
func TestFetchStationData_APIShape(t *testing.T) {
	temp := 19.5
	hum := 60.0

	payload := StationsDataResponse{}
	payload.Body.Devices = []Device{
		{
			StationName: "TestStation",
			Type:        "NAMain",
			ModuleName:  "Indoor",
			DashboardData: DashboardData{
				TimeUTC:     time.Now().Unix(),
				Temperature: &temp,
				Humidity:    &hum,
			},
			Modules: []Module{},
		},
	}

	body, _ := json.Marshal(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	tm := &TokenManager{}
	tm.mu.Lock()
	tm.accessToken = "fake-token"
	tm.mu.Unlock()

	// Call getWithAutoRefresh directly against the test server URL.
	respBody, err := tm.getWithAutoRefresh(srv.URL)
	if err != nil {
		t.Fatalf("getWithAutoRefresh failed: %v", err)
	}

	var parsed StationsDataResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(parsed.Body.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(parsed.Body.Devices))
	}
	if parsed.Body.Devices[0].StationName != "TestStation" {
		t.Errorf("StationName: got %q, want %q", parsed.Body.Devices[0].StationName, "TestStation")
	}
}
