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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// writeFile creates a named file inside t.TempDir() with the given content.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// sampleCSV contains three data points at 1-minute intervals.
const sampleCSV = `timestamp,value
2026-03-09T10:00:00+01:00,10.000000
2026-03-09T10:01:00+01:00,20.000000
2026-03-09T10:02:00+01:00,30.000000
`

// sampleJSON is the same three data points in JSON format.
const sampleJSON = `[
  {"timestamp":"2026-03-09T10:00:00+01:00","value":10.0},
  {"timestamp":"2026-03-09T10:01:00+01:00","value":20.0},
  {"timestamp":"2026-03-09T10:02:00+01:00","value":30.0}
]`

// sampleXML is the same three data points in XML format.
const sampleXML = `<?xml version="1.0" encoding="UTF-8"?>
<samples>
  <sample><timestamp>2026-03-09T10:00:00+01:00</timestamp><value>10.0</value></sample>
  <sample><timestamp>2026-03-09T10:01:00+01:00</timestamp><value>20.0</value></sample>
  <sample><timestamp>2026-03-09T10:02:00+01:00</timestamp><value>30.0</value></sample>
</samples>`

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "signal" {
		t.Errorf("name = %q, want %q", ua.GetName(), "signal")
	}
	if _, ok := ua.GetServices()["access"]; !ok {
		t.Error("expected 'access' service in ServicesMap")
	}
	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits must be *Traits")
	}
	if tr.InputFile == "" {
		t.Error("InputFile must be non-empty in template")
	}
}

// ── Traits serialization ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	original := Traits{
		InputFile:     "data/test.csv",
		PlaybackSpeed: 60,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Traits
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.InputFile != "data/test.csv" {
		t.Errorf("InputFile = %q, want %q", decoded.InputFile, "data/test.csv")
	}
	if decoded.PlaybackSpeed != 60 {
		t.Errorf("PlaybackSpeed = %v, want 60", decoded.PlaybackSpeed)
	}
	// Internal fields must not appear in JSON.
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	for _, hidden := range []string{"sample", "tStamp", "trayChan"} {
		if _, ok := raw[hidden]; ok {
			t.Errorf("field %q must not be exported to JSON", hidden)
		}
	}
}

// ── file loaders ─────────────────────────────────────────────────────────────

func TestLoadCSV(t *testing.T) {
	path := writeFile(t, "data.csv", sampleCSV)
	samples, err := loadCSV(path)
	if err != nil {
		t.Fatalf("loadCSV: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("len = %d, want 3", len(samples))
	}
	if samples[0].Value != 10.0 {
		t.Errorf("samples[0].Value = %v, want 10.0", samples[0].Value)
	}
	if samples[2].Value != 30.0 {
		t.Errorf("samples[2].Value = %v, want 30.0", samples[2].Value)
	}
	if samples[0].Timestamp != "2026-03-09T10:00:00+01:00" {
		t.Errorf("samples[0].Timestamp = %q", samples[0].Timestamp)
	}
}

func TestLoadJSON(t *testing.T) {
	path := writeFile(t, "data.json", sampleJSON)
	samples, err := loadJSON(path)
	if err != nil {
		t.Fatalf("loadJSON: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("len = %d, want 3", len(samples))
	}
	if samples[1].Value != 20.0 {
		t.Errorf("samples[1].Value = %v, want 20.0", samples[1].Value)
	}
}

func TestLoadXML(t *testing.T) {
	path := writeFile(t, "data.xml", sampleXML)
	samples, err := loadXML(path)
	if err != nil {
		t.Fatalf("loadXML: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("len = %d, want 3", len(samples))
	}
	if samples[2].Value != 30.0 {
		t.Errorf("samples[2].Value = %v, want 30.0", samples[2].Value)
	}
}

func TestLoadSamples(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
		wantN   int
		wantErr bool
	}{
		{"csv", "data.csv", sampleCSV, 3, false},
		{"json", "data.json", sampleJSON, 3, false},
		{"xml", "data.xml", sampleXML, 3, false},
		{"unsupported extension", "data.txt", "irrelevant", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, tc.file, tc.content)
			samples, err := loadSamples(path)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadSamples: %v", err)
			}
			if len(samples) != tc.wantN {
				t.Errorf("len = %d, want %d", len(samples), tc.wantN)
			}
		})
	}
}

// ── detectInterval ────────────────────────────────────────────────────────────

func TestDetectInterval(t *testing.T) {
	t.Run("one-minute spacing", func(t *testing.T) {
		samples := []Sample{
			{Timestamp: "2026-03-09T10:00:00+01:00"},
			{Timestamp: "2026-03-09T10:01:00+01:00"},
		}
		if d := detectInterval(samples); d != time.Minute {
			t.Errorf("interval = %v, want 1m", d)
		}
	})

	t.Run("one-second spacing", func(t *testing.T) {
		samples := []Sample{
			{Timestamp: "2026-03-09T10:00:00+01:00"},
			{Timestamp: "2026-03-09T10:00:01+01:00"},
		}
		if d := detectInterval(samples); d != time.Second {
			t.Errorf("interval = %v, want 1s", d)
		}
	})

	t.Run("fewer than two samples falls back to 1s", func(t *testing.T) {
		if d := detectInterval([]Sample{{Timestamp: "2026-03-09T10:00:00+01:00"}}); d != time.Second {
			t.Errorf("interval = %v, want 1s", d)
		}
	})

	t.Run("unparseable timestamps fall back to 1s", func(t *testing.T) {
		samples := []Sample{
			{Timestamp: "not-a-time"},
			{Timestamp: "also-not-a-time"},
		}
		if d := detectInterval(samples); d != time.Second {
			t.Errorf("interval = %v, want 1s", d)
		}
	})
}

// ── newResource ───────────────────────────────────────────────────────────────

func TestNewResource(t *testing.T) {
	t.Run("creates unit asset with correct name and serving func", func(t *testing.T) {
		path := writeFile(t, "data.csv", sampleCSV)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sys := components.NewSystem("emulator", ctx)
		sys.Husk = &components.Husk{
			Host:      components.NewDevice(),
			ProtoPort: map[string]int{"http": 20156},
		}

		traitJSON, _ := json.Marshal(Traits{InputFile: path, PlaybackSpeed: 1000})
		cfgAsset := usecases.ConfigurableAsset{
			Name:    "testSignal",
			Details: map[string][]string{"Unit": {"kPa"}},
			Traits:  []json.RawMessage{traitJSON},
			Services: []components.Service{
				{Definition: "signal", SubPath: "access"},
			},
		}

		ua, cleanup := newResource(cfgAsset, &sys)
		defer cleanup()

		if ua.GetName() != "testSignal" {
			t.Errorf("name = %q, want %q", ua.GetName(), "testSignal")
		}
		if ua.ServingFunc == nil {
			t.Error("ServingFunc must be set")
		}
		if _, ok := ua.GetServices()["access"]; !ok {
			t.Error("expected 'access' service")
		}
		tr, ok := ua.GetTraits().(*Traits)
		if !ok {
			t.Fatal("traits must be *Traits")
		}
		if tr.InputFile != path {
			t.Errorf("InputFile = %q, want %q", tr.InputFile, path)
		}
	})
}

// ── serving ───────────────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	t.Run("unknown path returns 400", func(t *testing.T) {
		tr := &Traits{trayChan: make(chan STray, 1)}
		req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("access path with running emulator returns 200", func(t *testing.T) {
		path := writeFile(t, "data.csv", sampleCSV)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tr := &Traits{
			InputFile:     path,
			PlaybackSpeed: 1000,
			trayChan:      make(chan STray),
		}
		go tr.emulateAsset(ctx, map[string][]string{"Unit": {"kPa"}})

		// give the goroutine a moment to initialise
		time.Sleep(20 * time.Millisecond)

		req := httptest.NewRequest(http.MethodGet, "/emulator/testSignal/access", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "access")
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})
}

// ── readSignal ────────────────────────────────────────────────────────────────

func TestReadSignal(t *testing.T) {
	t.Run("GET returns 200 with current sample", func(t *testing.T) {
		path := writeFile(t, "data.json", sampleJSON)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tr := &Traits{
			InputFile:     path,
			PlaybackSpeed: 1000,
			trayChan:      make(chan STray),
		}
		go tr.emulateAsset(ctx, map[string][]string{"Unit": {"kPa"}})
		time.Sleep(20 * time.Millisecond)

		req := httptest.NewRequest(http.MethodGet, "/access", nil)
		w := httptest.NewRecorder()
		tr.readSignal(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("non-GET returns 404", func(t *testing.T) {
		tr := &Traits{trayChan: make(chan STray, 1)}
		req := httptest.NewRequest(http.MethodPost, "/access", nil)
		w := httptest.NewRecorder()
		tr.readSignal(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}
