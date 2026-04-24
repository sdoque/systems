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
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "Vessel" {
		t.Errorf("name = %q, want Vessel", ua.GetName())
	}
	if _, ok := ua.GetServices()["access"]; !ok {
		t.Error("ServicesMap should contain an 'access' service")
	}
	cfg, ok := ua.GetTraits().(*NMEAConfig)
	if !ok {
		t.Fatal("Traits should be *NMEAConfig")
	}
	if cfg.Interface == "" {
		t.Error("Interface should not be empty")
	}
	if len(cfg.Signals) == 0 {
		t.Error("Signals should contain at least one entry")
	}
}

// ── parsePGN ──────────────────────────────────────────────────────────────────

func TestParsePGN(t *testing.T) {
	tests := []struct {
		input   string
		want    uint32
		wantErr bool
	}{
		{"130306", 130306, false},
		{"128259", 128259, false},
		{"0x1FD02", 0x1FD02, false},   // hex notation
		{"  130306  ", 130306, false}, // surrounding spaces
		{"0x", 0, true},               // missing digits
		{"abc", 0, true},              // invalid decimal
	}
	for _, tc := range tests {
		got, err := parsePGN(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePGN(%q): expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePGN(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePGN(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ── lookupPGNField ────────────────────────────────────────────────────────────

func TestLookupPGNField_Known(t *testing.T) {
	fi, err := lookupPGNField(130306, "WindSpeed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fi.Unit != "m/s" {
		t.Errorf("Unit = %q, want m/s", fi.Unit)
	}
}

func TestLookupPGNField_UnknownPGN(t *testing.T) {
	if _, err := lookupPGNField(99999, "WindSpeed"); err == nil {
		t.Error("expected error for unknown PGN 99999")
	}
}

func TestLookupPGNField_UnknownField(t *testing.T) {
	if _, err := lookupPGNField(130306, "NoSuchField"); err == nil {
		t.Error("expected error for unknown field 'NoSuchField' in PGN 130306")
	}
}

// ── buildISORequest ───────────────────────────────────────────────────────────

func TestBuildISORequest(t *testing.T) {
	pgn := uint32(130306)
	f := buildISORequest(pgn)

	// CAN_EFF_FLAG must be set for a 29-bit extended frame.
	if f.ID&canEFFFlag == 0 {
		t.Errorf("ID = 0x%08X: CAN_EFF_FLAG not set", f.ID)
	}
	if f.DLC != 8 {
		t.Errorf("DLC = %d, want 8", f.DLC)
	}
	// Payload: PGN in LSB-first 3 bytes.
	if f.Data[0] != byte(pgn&0xFF) {
		t.Errorf("Data[0] = 0x%02X, want 0x%02X (PGN LSB)", f.Data[0], byte(pgn&0xFF))
	}
	if f.Data[1] != byte((pgn>>8)&0xFF) {
		t.Errorf("Data[1] = 0x%02X, want 0x%02X (PGN mid)", f.Data[1], byte((pgn>>8)&0xFF))
	}
	if f.Data[2] != byte((pgn>>16)&0xFF) {
		t.Errorf("Data[2] = 0x%02X, want 0x%02X (PGN MSB)", f.Data[2], byte((pgn>>16)&0xFF))
	}
}

// ── extractPGN ────────────────────────────────────────────────────────────────

func TestExtractPGN(t *testing.T) {
	tests := []struct {
		name    string
		canID   uint32 // 29-bit extended ID, without CAN_EFF_FLAG
		wantPGN uint32
	}{
		// PDU Format 2 (PF >= 240) — PS is part of PGN.
		{
			name:    "PGN 130306 Wind Data",
			canID:   canEFFFlag | 0x19FD0223, // priority=6, DP=1, PF=0xFD, PS=0x02, SA=0x23
			wantPGN: 130306,
		},
		{
			name:    "PGN 129026 COG SOG",
			canID:   canEFFFlag | 0x19F80223, // priority=6, DP=1, PF=0xF8, PS=0x02
			wantPGN: 129026,
		},
		// PDU Format 1 (PF < 240) — PS is destination, not part of PGN.
		{
			name:    "ISO Request PGN 59904",
			canID:   canEFFFlag | 0x18EAFF23, // priority=6, PF=0xEA, dst=0xFF, SA=0x23
			wantPGN: isoRequestPGN,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPGN(tc.canID)
			if got != tc.wantPGN {
				t.Errorf("extractPGN(0x%08X) = %d, want %d", tc.canID, got, tc.wantPGN)
			}
		})
	}
}

// ── PGN field decoding ────────────────────────────────────────────────────────

func TestDecodeWindSpeed(t *testing.T) {
	fi, _ := lookupPGNField(130306, "WindSpeed")
	// SID=0, Wind Speed = 0x0BB8 = 3000 → 3000 * 0.01 = 30.0 m/s
	d := [8]byte{0x00, 0xB8, 0x0B, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if got != 30.0 {
		t.Errorf("WindSpeed = %.4f, want 30.0", got)
	}
}

func TestDecodeWindAngle(t *testing.T) {
	fi, _ := lookupPGNField(130306, "WindAngle")
	// Bytes 3-4: 0x7530 = 30000 → 30000 * 0.0001 = 3.0 rad
	d := [8]byte{0x00, 0x00, 0x00, 0x30, 0x75, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if math.Abs(got-3.0) > 1e-9 {
		t.Errorf("WindAngle = %.6f, want 3.0", got)
	}
}

func TestDecodeWaterSpeed(t *testing.T) {
	fi, _ := lookupPGNField(128259, "WaterSpeed")
	// Bytes 1-2: 0x0258 = 600 → 600 * 0.01 = 6.0 m/s
	d := [8]byte{0x00, 0x58, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if got != 6.0 {
		t.Errorf("WaterSpeed = %.4f, want 6.0", got)
	}
}

func TestDecodeHeading(t *testing.T) {
	fi, _ := lookupPGNField(127250, "Heading")
	// Bytes 1-2: 0x3A98 = 15000 → 15000 * 0.0001 = 1.5 rad
	d := [8]byte{0x00, 0x98, 0x3A, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if math.Abs(got-1.5) > 1e-9 {
		t.Errorf("Heading = %.6f, want 1.5", got)
	}
}

func TestDecodeCOG(t *testing.T) {
	fi, _ := lookupPGNField(129026, "COG")
	// Bytes 2-3: 0x61A8 = 25000 → 25000 * 0.0001 = 2.5 rad
	d := [8]byte{0x00, 0x00, 0xA8, 0x61, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if math.Abs(got-2.5) > 1e-9 {
		t.Errorf("COG = %.6f, want 2.5", got)
	}
}

func TestDecodeSOG(t *testing.T) {
	fi, _ := lookupPGNField(129026, "SOG")
	// Bytes 4-5: 0x01F4 = 500 → 500 * 0.01 = 5.0 m/s
	d := [8]byte{0x00, 0x00, 0x00, 0x00, 0xF4, 0x01, 0x00, 0x00}
	got := fi.Decode(d)
	if got != 5.0 {
		t.Errorf("SOG = %.4f, want 5.0", got)
	}
}

func TestDecodeDepth(t *testing.T) {
	fi, _ := lookupPGNField(128267, "Depth")
	// Bytes 1-4: 0x000009C4 = 2500 → 2500 * 0.01 = 25.0 m
	d := [8]byte{0x00, 0xC4, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if got != 25.0 {
		t.Errorf("Depth = %.4f, want 25.0", got)
	}
}

func TestDecodeNotAvailable(t *testing.T) {
	fi, _ := lookupPGNField(130306, "WindSpeed")
	// 0xFFFF = "not available" sentinel → NaN
	d := [8]byte{0x00, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := fi.Decode(d)
	if !math.IsNaN(got) {
		t.Errorf("WindSpeed with 0xFFFF should be NaN, got %.4f", got)
	}
}

// ── serving dispatcher ────────────────────────────────────────────────────────

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/sailor/WindSpeed/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAccess_MethodNotAllowed(t *testing.T) {
	tr := &Traits{trayChan: make(chan STray, 1)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/sailor/WindSpeed/access", nil)
	tr.access(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ── assetLoop ─────────────────────────────────────────────────────────────────

func TestAssetLoop_DeliversValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{
		name:       "WindSpeed",
		unit:       "m/s",
		trayChan:   make(chan STray),
		updateChan: make(chan float64, 1),
	}
	go tr.assetLoop(ctx)

	// Deliver a decoded value as canPoller would.
	tr.updateChan <- 12.5
	time.Sleep(10 * time.Millisecond)

	// Request it as an HTTP handler would.
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
		if f.Value != 12.5 {
			t.Errorf("Value = %.2f, want 12.5", f.Value)
		}
		if f.Unit != "m/s" {
			t.Errorf("Unit = %q, want m/s", f.Unit)
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
