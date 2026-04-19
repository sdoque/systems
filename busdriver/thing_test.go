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
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// ------------------------------------- initTemplate

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "Vehicle" {
		t.Errorf("name = %q, want Vehicle", ua.GetName())
	}
	if _, ok := ua.GetServices()["access"]; !ok {
		t.Error("ServicesMap should contain an 'access' service")
	}
	cfg, ok := ua.GetTraits().(*BusConfig)
	if !ok {
		t.Fatal("Traits should be *BusConfig")
	}
	if cfg.Interface == "" {
		t.Error("Interface should not be empty")
	}
	if cfg.Bitrate == 0 {
		t.Error("Bitrate should not be zero")
	}
	if len(cfg.Signals) == 0 {
		t.Error("Signals should contain at least one entry")
	}
}

// ------------------------------------- parsePID

func TestParsePID(t *testing.T) {
	tests := []struct {
		input   string
		want    uint8
		wantErr bool
	}{
		{"0x0C", 0x0C, false},
		{"0x05", 0x05, false},
		{"0X0D", 0x0D, false},     // uppercase 0X
		{"12", 12, false},         // decimal
		{"  0x0C  ", 0x0C, false}, // surrounding spaces
		{"0x", 0, true},           // missing digits after prefix
		{"gg", 0, true},           // not valid hex or decimal
		{"256", 0, true},          // uint8 overflow
	}
	for _, tc := range tests {
		got, err := parsePID(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePID(%q): expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePID(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePID(%q) = 0x%02X, want 0x%02X", tc.input, got, tc.want)
		}
	}
}

// ------------------------------------- lookupPID

func TestLookupPID_Known(t *testing.T) {
	info, err := lookupPID(0x0C)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Unit != "rpm" {
		t.Errorf("Unit = %q, want rpm", info.Unit)
	}
	if info.Name != "EngineRPM" {
		t.Errorf("Name = %q, want EngineRPM", info.Name)
	}
}

func TestLookupPID_Unknown(t *testing.T) {
	if _, err := lookupPID(0xFF); err == nil {
		t.Error("expected error for unknown PID 0xFF")
	}
}

// ------------------------------------- buildOBDRequest

func TestBuildOBDRequest(t *testing.T) {
	f := buildOBDRequest(0x0C)
	if f.ID != obdRequestID {
		t.Errorf("ID = 0x%03X, want 0x%03X", f.ID, obdRequestID)
	}
	if f.DLC != 8 {
		t.Errorf("DLC = %d, want 8", f.DLC)
	}
	if f.Data[0] != 0x02 {
		t.Errorf("Data[0] = 0x%02X, want 0x02 (payload length)", f.Data[0])
	}
	if f.Data[1] != 0x01 {
		t.Errorf("Data[1] = 0x%02X, want 0x01 (Mode 01)", f.Data[1])
	}
	if f.Data[2] != 0x0C {
		t.Errorf("Data[2] = 0x%02X, want 0x0C (PID)", f.Data[2])
	}
}

// ------------------------------------- decodeOBDResponse

func TestDecodeOBDResponse(t *testing.T) {
	t.Run("engine RPM", func(t *testing.T) {
		// Formula: (A*256 + B) / 4
		// A=0x1A (26), B=0xF0 (240) → (26*256 + 240) / 4 = 6896 / 4 = 1724 rpm
		f := canFrame{
			ID:   0x7E8,
			DLC:  8,
			Data: [8]byte{0x04, 0x41, 0x0C, 0x1A, 0xF0},
		}
		val, err := decodeOBDResponse(f, 0x0C)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != 1724 {
			t.Errorf("RPM = %.1f, want 1724", val)
		}
	})

	t.Run("coolant temperature", func(t *testing.T) {
		// Formula: A - 40
		// A=0x64 (100) → 100 - 40 = 60 °C
		f := canFrame{
			ID:   0x7E8,
			DLC:  8,
			Data: [8]byte{0x03, 0x41, 0x05, 0x64},
		}
		val, err := decodeOBDResponse(f, 0x05)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != 60 {
			t.Errorf("temperature = %.1f °C, want 60", val)
		}
	})

	t.Run("vehicle speed", func(t *testing.T) {
		// Formula: A
		// A=0x50 (80) → 80 km/h
		f := canFrame{
			ID:   0x7E8,
			DLC:  8,
			Data: [8]byte{0x03, 0x41, 0x0D, 0x50},
		}
		val, err := decodeOBDResponse(f, 0x0D)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != 80 {
			t.Errorf("speed = %.1f km/h, want 80", val)
		}
	})

	t.Run("ECU 2 response is accepted (0x7E9)", func(t *testing.T) {
		f := canFrame{
			ID:   0x7E9,
			DLC:  8,
			Data: [8]byte{0x03, 0x41, 0x0D, 0x3C}, // 60 km/h
		}
		val, err := decodeOBDResponse(f, 0x0D)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != 60 {
			t.Errorf("speed = %.1f, want 60", val)
		}
	})

	t.Run("wrong CAN ID rejected", func(t *testing.T) {
		f := canFrame{ID: 0x123, Data: [8]byte{0x04, 0x41, 0x0C, 0x1A, 0xF0}}
		if _, err := decodeOBDResponse(f, 0x0C); err == nil {
			t.Error("expected error for non-OBD CAN ID 0x123")
		}
	})

	t.Run("wrong service byte rejected", func(t *testing.T) {
		f := canFrame{ID: 0x7E8, Data: [8]byte{0x04, 0x42, 0x0C, 0x1A, 0xF0}}
		if _, err := decodeOBDResponse(f, 0x0C); err == nil {
			t.Error("expected error for service byte 0x42 (not a Mode 01 response)")
		}
	})

	t.Run("PID mismatch rejected", func(t *testing.T) {
		// Response says PID 0x05 but we asked for 0x0C
		f := canFrame{ID: 0x7E8, Data: [8]byte{0x03, 0x41, 0x05, 0x64}}
		if _, err := decodeOBDResponse(f, 0x0C); err == nil {
			t.Error("expected error for PID mismatch")
		}
	})
}

// ------------------------------------- serving dispatcher

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/busdriver/EngineRPM/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAccess_MethodNotAllowed(t *testing.T) {
	tr := &Traits{trayChan: make(chan STray, 1)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/busdriver/EngineRPM/access", nil)
	tr.access(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ------------------------------------- assetLoop

// TestAssetLoop_DeliversValues verifies the full channel round-trip:
// push a value via updateChan, then retrieve it via trayChan.
func TestAssetLoop_DeliversValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{
		name:       "EngineRPM",
		unit:       "rpm",
		trayChan:   make(chan STray),
		updateChan: make(chan float64, 1),
	}
	go tr.assetLoop(ctx)

	// Deliver a new value as canPoller would.
	tr.updateChan <- 2400.0
	time.Sleep(10 * time.Millisecond) // let assetLoop process the update

	// Request the stored value as an HTTP handler would.
	order := STray{
		ValueP: make(chan forms.SignalA_v1a, 1),
		Error:  make(chan error, 1),
	}
	select {
	case tr.trayChan <- order:
	case <-time.After(time.Second):
		t.Fatal("timed out sending to trayChan")
	}
	select {
	case f := <-order.ValueP:
		if f.Value != 2400.0 {
			t.Errorf("Value = %.1f, want 2400.0", f.Value)
		}
		if f.Unit != "rpm" {
			t.Errorf("Unit = %q, want rpm", f.Unit)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reply from assetLoop")
	}
}

func TestAssetLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Traits{
		name:       "test",
		trayChan:   make(chan STray),
		updateChan: make(chan float64, 1),
	}
	done := make(chan struct{})
	go func() {
		tr.assetLoop(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("assetLoop did not exit after context cancellation")
	}
}
