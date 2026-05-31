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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestTraits builds a minimal Traits suitable for unit tests.
// CompletionDelay is set to 0 so the TECO countdown is never used in handler tests.
func newTestTraits() *Traits {
	return &Traits{
		CompletionDelay: 0,
		orders:          make(map[string]*Order),
		monitor: &components.Cervice{
			Definition: "SignalMonitoring",
			Nodes:      make(map[string][]components.NodeInfo),
		},
		enrichment: &components.Cervice{
			Definition: "EnrichmentNotification",
			Nodes:      make(map[string][]components.NodeInfo),
		},
	}
}

// withDiscoverMonitor temporarily replaces the discoverMonitor function for the
// duration of a test and restores it afterwards.
func withDiscoverMonitor(t *testing.T, fn func(*components.Cervice, *components.System) error) {
	t.Helper()
	orig := discoverMonitor
	discoverMonitor = fn
	t.Cleanup(func() { discoverMonitor = orig })
}

// withDiscoverEnrichment is the equivalent shim for the nurse's enrichment endpoint.
func withDiscoverEnrichment(t *testing.T, fn func(*components.Cervice, *components.System) error) {
	t.Helper()
	orig := discoverEnrichment
	discoverEnrichment = fn
	t.Cleanup(func() { discoverEnrichment = orig })
}

// validOrderBody returns a JSON-encoded minimal valid OrderRequest.
func validOrderBody(t *testing.T) *bytes.Buffer {
	t.Helper()
	req := OrderRequest{
		EquipmentID: "10000045",
		Plant:       "1000",
		Description: "Pressure exceeded threshold",
	}
	b, _ := json.Marshal(req)
	return bytes.NewBuffer(b)
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "SAPSimulator" {
		t.Errorf("name = %q, want %q", ua.GetName(), "SAPSimulator")
	}
	if _, ok := ua.GetServices()["maintenanceorders"]; !ok {
		t.Error("expected 'maintenanceorders' service in ServicesMap")
	}
	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits must be *Traits")
	}
	if tr.CompletionDelay != 30 {
		t.Errorf("CompletionDelay = %v, want 30", tr.CompletionDelay)
	}
}

// ── Traits serialization ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	original := &Traits{CompletionDelay: 45}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Traits
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.CompletionDelay != 45 {
		t.Errorf("CompletionDelay = %v, want 45", decoded.CompletionDelay)
	}
	// Internal fields must not appear in JSON.
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	for _, hidden := range []string{"orders", "monitor", "owner", "ua"} {
		if _, ok := raw[hidden]; ok {
			t.Errorf("field %q must not be exported to JSON", hidden)
		}
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

func TestNewResource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("sapper", ctx)
	sys.Husk = &components.Husk{
		Host:      components.NewDevice(),
		ProtoPort: map[string]int{"http": 20191},
	}

	traitJSON, _ := json.Marshal(Traits{CompletionDelay: 10})
	cfgAsset := usecases.ConfigurableAsset{
		Name:     "SAPSimulator",
		Traits:   []json.RawMessage{traitJSON},
		Services: []components.Service{{Definition: "MaintenanceOrder", SubPath: "maintenanceorders"}},
	}

	ua, cleanup := newResource(cfgAsset, &sys)
	defer cleanup()

	if ua.GetName() != "SAPSimulator" {
		t.Errorf("name = %q, want SAPSimulator", ua.GetName())
	}
	if ua.ServingFunc == nil {
		t.Error("ServingFunc must be set")
	}
	if _, ok := ua.GetServices()["maintenanceorders"]; !ok {
		t.Error("expected 'maintenanceorders' service")
	}
	if ua.CervicesMap["monitor"] == nil {
		t.Error("expected 'monitor' cervice")
	}
	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("traits must be *Traits")
	}
	if tr.CompletionDelay != 10 {
		t.Errorf("CompletionDelay = %v, want 10", tr.CompletionDelay)
	}
}

// ── order ID generation ───────────────────────────────────────────────────────

func TestOrderIDGeneration(t *testing.T) {
	tr := newTestTraits()

	id1 := tr.nextOrderID()
	notif1 := tr.nextNotifID()
	id2 := tr.nextOrderID()

	if id1 == id2 {
		t.Error("consecutive order IDs must be unique")
	}
	if id1[0] != '4' {
		t.Errorf("order ID must start with '4', got %q", id1)
	}
	if notif1[0] != '2' {
		t.Errorf("notification ID must start with '2', got %q", notif1)
	}
	if len(id1) != 9 { // "4" + 8 digits
		t.Errorf("order ID length = %d, want 9", len(id1))
	}
}

// ── createOrder ───────────────────────────────────────────────────────────────

func TestCreateOrder(t *testing.T) {
	tr := newTestTraits()
	// Use a very long delay so runLifecycle doesn't fire during the test.
	tr.CompletionDelay = 9999

	req := OrderRequest{EquipmentID: "10000045", Plant: "1000", Description: "test"}
	o := tr.createOrder(req)

	if o.Status != "CRTD" {
		t.Errorf("initial status = %q, want CRTD", o.Status)
	}
	if o.ID == "" || o.Notification == "" {
		t.Error("order ID and notification ID must be non-empty")
	}
	if o.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}

	tr.mu.Lock()
	stored, ok := tr.orders[o.ID]
	tr.mu.Unlock()
	if !ok {
		t.Error("order must be stored in the orders map")
	}
	if stored != o {
		t.Error("stored order must be the same pointer as returned")
	}
}

// ── enrichAndRelease + runTECOCountdown ──────────────────────────────────────
//
// The lifecycle is now split: the order sits in CRTD until a planner calls
// enrichAndRelease, which flips status to REL and starts the TECO countdown.
// This test drives that flow against fake monitor + enrichment endpoints.

func TestEnrichAndRelease(t *testing.T) {
	callbackReceived := make(chan struct{}, 1)
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbackReceived <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer monitor.Close()

	enrichmentReceived := make(chan struct{}, 1)
	enrichmentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enrichmentReceived <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer enrichmentSrv.Close()

	withDiscoverMonitor(t, func(c *components.Cervice, _ *components.System) error {
		c.Nodes = map[string][]components.NodeInfo{"testNode": {{URL: monitor.URL}}}
		return nil
	})
	withDiscoverEnrichment(t, func(c *components.Cervice, _ *components.System) error {
		c.Nodes = map[string][]components.NodeInfo{"testNode": {{URL: enrichmentSrv.URL}}}
		return nil
	})

	tr := newTestTraits()
	tr.CompletionDelay = 1 // 1 s TECO countdown after release

	o := &Order{
		ID:           "400000001",
		Notification: "200000001",
		Status:       "CRTD",
		CreatedAt:    time.Now(),
		Request:      OrderRequest{EquipmentID: "10000045"},
	}
	tr.mu.Lock()
	tr.orders[o.ID] = o
	tr.mu.Unlock()

	released, err := tr.enrichAndRelease(o.ID, json.RawMessage(`{"operations":[]}`))
	if err != nil {
		t.Fatalf("enrichAndRelease: unexpected error: %v", err)
	}
	if released.Status != "REL" {
		t.Errorf("status after release = %q, want REL", released.Status)
	}
	if released.ReleasedAt.IsZero() {
		t.Error("ReleasedAt must be set after enrichAndRelease")
	}

	// Enrichment should arrive at the nurse essentially immediately.
	select {
	case <-enrichmentReceived:
	case <-time.After(500 * time.Millisecond):
		t.Error("expected enrichment to be received by nurse server")
	}

	// After ~1.2 s the TECO countdown should have fired and the monitor callback delivered.
	time.Sleep(1200 * time.Millisecond)
	tr.mu.Lock()
	statusFinal := o.Status
	tr.mu.Unlock()
	if statusFinal != "TECO" {
		t.Errorf("status after TECO countdown = %q, want TECO", statusFinal)
	}
	select {
	case <-callbackReceived:
	default:
		t.Error("expected TECO callback to be received by monitor server")
	}
}

// TestEnrichAndRelease_RejectsNonCRTD verifies the release path refuses to act
// on orders that aren't waiting.
func TestEnrichAndRelease_RejectsNonCRTD(t *testing.T) {
	tr := newTestTraits()
	o := &Order{ID: "400000002", Status: "TECO", CreatedAt: time.Now()}
	tr.mu.Lock()
	tr.orders[o.ID] = o
	tr.mu.Unlock()

	if _, err := tr.enrichAndRelease(o.ID, json.RawMessage(`{}`)); err == nil {
		t.Error("expected error releasing a non-CRTD order, got nil")
	}
}

// ── notifyConsumer ────────────────────────────────────────────────────────────

func TestNotifyConsumer(t *testing.T) {
	t.Run("posts completion event to discovered URL", func(t *testing.T) {
		var got CompletionEvent
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		withDiscoverMonitor(t, func(c *components.Cervice, _ *components.System) error {
			c.Nodes = map[string][]components.NodeInfo{"n": {{URL: srv.URL}}}
			return nil
		})

		tr := newTestTraits()
		tr.CompletionDelay = 5
		o := &Order{ID: "400000001", Status: "TECO"}
		tr.notifyConsumer(o)

		if got.OrderID != "400000001" {
			t.Errorf("OrderID = %q, want 400000001", got.OrderID)
		}
		if got.Status != "TECO" {
			t.Errorf("Status = %q, want TECO", got.Status)
		}
		if got.CompletedAt == nil {
			t.Error("CompletedAt must be set")
		}
	})

	t.Run("no-op when discovery returns no nodes", func(t *testing.T) {
		withDiscoverMonitor(t, func(c *components.Cervice, _ *components.System) error {
			c.Nodes = make(map[string][]components.NodeInfo) // empty — no provider found
			return nil
		})

		tr := newTestTraits()
		o := &Order{ID: "400000002", Status: "TECO"}
		tr.notifyConsumer(o) // must not panic
	})
}

// ── createOrderHandler ────────────────────────────────────────────────────────

func TestCreateOrderHandler(t *testing.T) {
	t.Run("valid request returns 201 with order IDs", func(t *testing.T) {
		tr := newTestTraits()
		tr.CompletionDelay = 9999 // suppress lifecycle

		req := httptest.NewRequest(http.MethodPost, "/orders", validOrderBody(t))
		w := httptest.NewRecorder()
		tr.createOrderHandler(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
		}
		var resp OrderResponse
		json.NewDecoder(w.Body).Decode(&resp)
		if resp.MaintenanceOrder == "" {
			t.Error("maintenanceOrder must be non-empty")
		}
		if resp.MaintenanceNotification == "" {
			t.Error("maintenanceNotification must be non-empty")
		}
		if resp.Status != "CRTD" {
			t.Errorf("status = %q, want CRTD", resp.Status)
		}
	})

	t.Run("missing required fields returns 400", func(t *testing.T) {
		tr := newTestTraits()
		body, _ := json.Marshal(OrderRequest{EquipmentID: "10000045"}) // missing Plant and Description
		req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBuffer(body))
		w := httptest.NewRecorder()
		tr.createOrderHandler(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		tr := newTestTraits()
		req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString("not json"))
		w := httptest.NewRecorder()
		tr.createOrderHandler(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// ── queryOrderHandler ─────────────────────────────────────────────────────────

func TestQueryOrderHandler(t *testing.T) {
	tr := newTestTraits()
	o := tr.createOrder(OrderRequest{EquipmentID: "10000045", Plant: "1000", Description: "test"})

	t.Run("existing order returns 200 with status", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/orders?id="+o.ID, nil)
		w := httptest.NewRecorder()
		tr.queryOrderHandler(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		json.NewDecoder(w.Body).Decode(&resp)
		if resp["maintenanceOrder"] != o.ID {
			t.Errorf("maintenanceOrder = %q, want %q", resp["maintenanceOrder"], o.ID)
		}
		if resp["status"] != "CRTD" {
			t.Errorf("status = %q, want CRTD", resp["status"])
		}
	})

	t.Run("unknown order ID returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/orders?id=NOTEXIST", nil)
		w := httptest.NewRecorder()
		tr.queryOrderHandler(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("missing id parameter returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		w := httptest.NewRecorder()
		tr.queryOrderHandler(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// ── serving dispatcher ────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	tr := newTestTraits()

	t.Run("POST to maintenanceorders creates order", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/sapper/SAPSimulator/maintenanceorders", validOrderBody(t))
		w := httptest.NewRecorder()
		serving(tr, w, req, "maintenanceorders")
		if w.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201", w.Code)
		}
	})

	t.Run("GET to maintenanceorders without id returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sapper/SAPSimulator/maintenanceorders", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "maintenanceorders")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("unsupported method returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/sapper/SAPSimulator/maintenanceorders", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "maintenanceorders")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})

	t.Run("unknown service path returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sapper/SAPSimulator/unknown", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}
