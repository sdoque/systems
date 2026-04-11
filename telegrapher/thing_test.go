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
	"strings"
	"testing"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "Kitchen/temperature" {
		t.Errorf("expected Name %q, got %q", "Kitchen/temperature", ua.Name)
	}

	if _, ok := ua.ServicesMap["access"]; !ok {
		t.Error("expected ServicesMap to have an entry for \"access\"")
	}

	if ua.Traits == nil {
		t.Error("expected Traits to be non-nil")
	}
}

// TestPublishInfo_NoPendingDiscovery verifies that publishInfo writes a
// text/plain response with topic, broker, and period info when no sources
// are known yet.
func TestPublishInfo_NoPendingDiscovery(t *testing.T) {
	tr := &Traits{
		Topic:    "Room/temperature",
		Broker:   "tcp://localhost:1883",
		Period:   30,
		cervices: nil,
	}

	w := httptest.NewRecorder()
	tr.publishInfo(w)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"Room/temperature", "tcp://localhost:1883", "30"} {
		if !strings.Contains(body, want) {
			t.Errorf("publishInfo body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "pending discovery") {
		t.Errorf("publishInfo body should say 'pending discovery' when no sources known:\n%s", body)
	}
}

// TestServing_Publish_GET verifies that GET /publish calls publishInfo (200, text/plain).
func TestServing_Publish_GET(t *testing.T) {
	tr := &Traits{
		Topic:  "Room/temperature",
		Broker: "tcp://localhost:1883",
		Period: 30,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/publish", nil)
	serving(tr, w, r, "publish")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestServing_Publish_MethodNotAllowed verifies that DELETE /publish returns 405.
func TestServing_Publish_MethodNotAllowed(t *testing.T) {
	tr := &Traits{Topic: "Room/temperature"}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/telegrapher/Room_temperature/publish", nil)
	serving(tr, w, r, "publish")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestServing_InvalidPath verifies that an unknown service path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestAccess_GET_EmptyMessage verifies that GET /access returns 400 when no
// MQTT message has been received yet.
func TestAccess_GET_EmptyMessage(t *testing.T) {
	tr := &Traits{Message: nil}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty message", w.Code)
	}
}

// TestAccess_GET_WithMessage verifies that GET /access returns 200 and the
// stored message bytes when a message has been received.
func TestAccess_GET_WithMessage(t *testing.T) {
	tr := &Traits{Message: []byte(`{"temp":21.5}`)}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "21.5") {
		t.Errorf("body should contain message content, got: %s", w.Body.String())
	}
}

// TestAccess_Default verifies that unsupported methods return a non-2xx status.
func TestAccess_Default(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code == http.StatusOK {
		t.Error("expected non-200 for DELETE /access")
	}
}
