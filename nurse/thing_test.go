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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// ------------------------------------- initTemplate

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "HealthTracker" {
		t.Errorf("name = %q, want HealthTracker", ua.GetName())
	}
	if _, ok := ua.GetServices()["monitor"]; !ok {
		t.Error("ServicesMap should contain a 'monitor' service")
	}

	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.SAP_URL == "" {
		t.Error("SAP_URL default should not be empty")
	}
	if len(tr.Signals) == 0 {
		t.Error("template should include at least one signal")
	}
}

// ------------------------------------- findSignal

func TestFindSignal_Found(t *testing.T) {
	tr := &Traits{
		Signals: []SignalT{
			{Name: "temperature", Threshold: 75.0},
			{Name: "pressure", Threshold: 10.0},
		},
	}
	s := tr.findSignal("pressure")
	if s == nil {
		t.Fatal("expected to find 'pressure' signal, got nil")
	}
	if s.Threshold != 10.0 {
		t.Errorf("threshold = %v, want 10.0", s.Threshold)
	}
}

func TestFindSignal_NotFound(t *testing.T) {
	tr := &Traits{
		Signals: []SignalT{{Name: "temperature"}},
	}
	if got := tr.findSignal("vibration"); got != nil {
		t.Errorf("expected nil for unknown signal, got %+v", got)
	}
}

// ------------------------------------- assetNameFromURL

func TestAssetNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"http://192.168.1.5:20100/ds18b20/Kitchen_Sensor/temperature", "Kitchen_Sensor"},
		{"http://host:1234/system/asset/service", "asset"},
		{"http://host/system/asset", "asset"},
		{"invalid-url", ""},
		{"http://host/single", ""},
	}
	for _, tc := range tests {
		got := assetNameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("assetNameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// ------------------------------------- CheckServerUp

func TestCheckServerUp_Up(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := CheckServerUp(srv.URL, 2*time.Second)
	if !r.Up {
		t.Errorf("expected Up=true, got false (%v)", r.Err)
	}
	if r.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", r.StatusCode)
	}
}

func TestCheckServerUp_Down(t *testing.T) {
	r := CheckServerUp("http://127.0.0.1:1/health", 500*time.Millisecond)
	if r.Up {
		t.Error("expected Up=false for unreachable server")
	}
	if r.Err == nil {
		t.Error("expected a non-nil error")
	}
}

func TestCheckServerUp_InvalidURL(t *testing.T) {
	r := CheckServerUp("://not a url", 1*time.Second)
	if r.Up {
		t.Error("expected Up=false for invalid URL")
	}
}

// ------------------------------------- ConvertAckJSONToTurtle

func TestConvertAckJSONToTurtle_Valid(t *testing.T) {
	raw := []byte(`{
		"maintenanceOrder":        "4000001",
		"maintenanceNotification": "2000001",
		"status":                  "TECO",
		"message":                 "completed",
		"createdAt":               "2025-01-01T00:00:00Z"
	}`)

	ttl, err := ConvertAckJSONToTurtle(raw, "https://example.com/sap/")
	if err != nil {
		t.Fatalf("ConvertAckJSONToTurtle error: %v", err)
	}

	for _, want := range []string{
		"@prefix ex:",
		"MaintenanceOrder/4000001",
		"MaintenanceNotification/2000001",
		`"TECO"`,
		`"completed"`,
	} {
		if !strings.Contains(ttl, want) {
			t.Errorf("Turtle output missing %q\n---\n%s", want, ttl)
		}
	}
}

func TestConvertAckJSONToTurtle_InvalidJSON(t *testing.T) {
	_, err := ConvertAckJSONToTurtle([]byte("not json"), "")
	if err == nil {
		t.Error("expected error for invalid JSON input")
	}
}

func TestConvertAckJSONToTurtle_DefaultBaseIRI(t *testing.T) {
	raw := []byte(`{"maintenanceOrder":"4000099","maintenanceNotification":"2000099","status":"TECO"}`)
	ttl, err := ConvertAckJSONToTurtle(raw, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ttl, "sinetiq.se") {
		t.Errorf("expected default IRI to contain sinetiq.se, got:\n%s", ttl)
	}
}

// ------------------------------------- ConvertMaintenanceDoneEventToTurtle

func TestConvertMaintenanceDoneEventToTurtle_Valid(t *testing.T) {
	now := time.Now()
	event := MaintenanceDoneEvent{
		OrderID:         "4000002",
		Status:          "TECO",
		CompletedAt:     &now,
		ActualWorkHours: 3.5,
		Notes:           "replaced sensor",
	}

	ttl, err := ConvertMaintenanceDoneEventToTurtle(event, "https://example.com/sap/")
	if err != nil {
		t.Fatalf("ConvertMaintenanceDoneEventToTurtle error: %v", err)
	}

	for _, want := range []string{
		"MaintenanceOrder/4000002",
		`"TECO"`,
		`"3.5"`,
		`"replaced sensor"`,
	} {
		if !strings.Contains(ttl, want) {
			t.Errorf("Turtle output missing %q\n---\n%s", want, ttl)
		}
	}
}

func TestConvertMaintenanceDoneEventToTurtle_DefaultBaseIRI(t *testing.T) {
	event := MaintenanceDoneEvent{OrderID: "4000003", Status: "TECO"}
	ttl, err := ConvertMaintenanceDoneEventToTurtle(event, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ttl, "sinetiq.se") {
		t.Errorf("expected default IRI, got:\n%s", ttl)
	}
}

// ------------------------------------- update handler (completion notification)

func newTraitsWithPendingOrder(orderID, signalName string) *Traits {
	return &Traits{
		SAP_URL: "http://localhost",
		Signals: []SignalT{
			{
				Name:          signalName,
				Threshold:     75.0,
				Operational:   false,
				TOverCount:    map[string]int{"node1": 5},
				WorkRequested: map[string]bool{"node1": true},
			},
		},
		pendingOrders: map[string]string{orderID: signalName},
	}
}

func TestUpdate_RestoressSignalToOperational(t *testing.T) {
	tr := newTraitsWithPendingOrder("4000010", "temperature")

	now := time.Now()
	event := MaintenanceDoneEvent{OrderID: "4000010", Status: "TECO", CompletedAt: &now}
	body, _ := json.Marshal(event)

	r := httptest.NewRequest(http.MethodPost, "/nurse/HealthTracker/monitor", bytes.NewReader(body))
	w := httptest.NewRecorder()
	tr.update(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	sig := tr.findSignal("temperature")
	if sig == nil {
		t.Fatal("signal 'temperature' not found after update")
	}
	if !sig.Operational {
		t.Error("signal should be Operational after completion event")
	}
	if len(sig.WorkRequested) != 0 {
		t.Error("WorkRequested should be empty after completion event")
	}
	if len(sig.TOverCount) != 0 {
		t.Error("TOverCount should be reset after completion event")
	}
	if _, still := tr.pendingOrders["4000010"]; still {
		t.Error("pending order should be removed after completion event")
	}
}

func TestUpdate_SAPAdaptorFormat(t *testing.T) {
	// The SAP adaptor sends "maintenanceOrder" instead of "orderId".
	tr := newTraitsWithPendingOrder("4000020", "pressure")

	body := []byte(`{"maintenanceOrder":"4000020","status":"TECO"}`)
	r := httptest.NewRequest(http.MethodPost, "/nurse/HealthTracker/monitor", bytes.NewReader(body))
	w := httptest.NewRecorder()
	tr.update(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	sig := tr.findSignal("pressure")
	if sig == nil || !sig.Operational {
		t.Error("signal should be restored when using maintenanceOrder field")
	}
}

func TestUpdate_UnknownOrder(t *testing.T) {
	tr := &Traits{
		Signals:       []SignalT{{Name: "temperature", TOverCount: map[string]int{}, WorkRequested: map[string]bool{}}},
		pendingOrders: map[string]string{},
	}
	now := time.Now()
	event := MaintenanceDoneEvent{OrderID: "9999999", Status: "TECO", CompletedAt: &now}
	body, _ := json.Marshal(event)

	r := httptest.NewRequest(http.MethodPost, "/nurse/HealthTracker/monitor", bytes.NewReader(body))
	w := httptest.NewRecorder()
	tr.update(w, r) // must not panic
	// Response should still be 200 (unknown order is logged, not an error to the caller)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for unknown order", w.Code)
	}
}

func TestUpdate_InvalidJSON(t *testing.T) {
	tr := &Traits{Signals: []SignalT{}, pendingOrders: map[string]string{}}
	r := httptest.NewRequest(http.MethodPost, "/nurse/HealthTracker/monitor", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	tr.update(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", w.Code)
	}
}

// ------------------------------------- state handler

func TestState(t *testing.T) {
	tr := &Traits{
		SAP_URL: "http://localhost",
		Signals: []SignalT{
			{Name: "temperature", Threshold: 75.0, Operational: true, TOverCount: map[string]int{}, WorkRequested: map[string]bool{}},
		},
	}
	tr.ua = &components.UnitAsset{Name: "HealthTracker"}

	w := httptest.NewRecorder()
	tr.state(w)

	body := w.Body.String()
	if !strings.Contains(body, "temperature") {
		t.Errorf("state output missing signal name 'temperature': %s", body)
	}
	if !strings.Contains(body, "75") {
		t.Errorf("state output missing threshold value: %s", body)
	}
}

// ------------------------------------- serving dispatcher

func TestServing_GET_monitor(t *testing.T) {
	tr := &Traits{
		Signals: []SignalT{
			{Name: "temperature", Threshold: 80.0, Operational: true, TOverCount: map[string]int{}, WorkRequested: map[string]bool{}},
		},
		pendingOrders: map[string]string{},
	}
	tr.ua = &components.UnitAsset{Name: "HealthTracker"}

	r := httptest.NewRequest(http.MethodGet, "/nurse/HealthTracker/monitor", nil)
	w := httptest.NewRecorder()
	serving(tr, w, r, "monitor")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "temperature") {
		t.Error("GET monitor should include signal name in response")
	}
}

func TestServing_MethodNotAllowed(t *testing.T) {
	tr := &Traits{Signals: []SignalT{}, pendingOrders: map[string]string{}}
	r := httptest.NewRequest(http.MethodDelete, "/nurse/HealthTracker/monitor", nil)
	w := httptest.NewRecorder()
	serving(tr, w, r, "monitor")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestServing_InvalidServicePath(t *testing.T) {
	tr := &Traits{}
	r := httptest.NewRequest(http.MethodGet, "/nurse/HealthTracker/unknown", nil)
	w := httptest.NewRecorder()
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
