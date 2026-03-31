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
	"sync"
	"testing"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// ── mock WriteAPI ──────────────────────────────────────────────────────────────

// mockWriteAPI implements api.WriteAPI, capturing every point written.
type mockWriteAPI struct {
	mu     sync.Mutex
	points []*write.Point
}

func (m *mockWriteAPI) WriteRecord(_ string)                             {}
func (m *mockWriteAPI) Flush()                                           {}
func (m *mockWriteAPI) Errors() <-chan error                             { return make(chan error) }
func (m *mockWriteAPI) SetWriteFailedCallback(_ api.WriteFailedCallback) {}
func (m *mockWriteAPI) WritePoint(p *write.Point) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.points = append(m.points, p)
}

// captured returns a snapshot of all written points.
func (m *mockWriteAPI) captured() []*write.Point {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*write.Point, len(m.points))
	copy(out, m.points)
	return out
}

// ── point inspection helpers ───────────────────────────────────────────────────

// tagMap extracts all tags from a point as a plain map.
// The lp.Tag type (from influxdata/line-protocol) need not be named explicitly.
func tagMap(p *write.Point) map[string]string {
	result := make(map[string]string)
	for _, tag := range p.TagList() {
		result[tag.Key] = tag.Value
	}
	return result
}

// fieldValue returns the value of the named field, or nil if absent.
func fieldValue(p *write.Point, key string) interface{} {
	for _, f := range p.FieldList() {
		if f.Key == key {
			return f.Value
		}
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestSystem returns a System with a non-nil Husk (empty CoreS slice) so that
// any re-discovery attempt inside collectIngest returns a graceful "not found"
// error rather than panicking on a nil Husk pointer.
func newTestSystem(ctx context.Context) components.System {
	sys := components.NewSystem("testCollector", ctx)
	sys.Husk = &components.Husk{}
	return sys
}

// newSignalServer starts an httptest.Server that always responds with a
// SignalA_v1a carrying the given value. The server is closed via t.Cleanup.
func newSignalServer(t *testing.T, value float64, unit string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sig := forms.SignalA_v1a{
			Value:     value,
			Unit:      unit,
			Timestamp: time.Now(),
			Version:   "SignalA_v1.0",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sig)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCollectIngest starts collectIngest in a goroutine, waits for the given
// duration to allow ticks to fire, then cancels the context and waits for the
// goroutine to exit. Returns the cancellation function and a channel that
// receives the function's return value.
func runCollectIngest(t *testing.T, tr *Traits, name string, period, wait time.Duration, mwa *mockWriteAPI) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		tr.collectIngest(name, period, mwa) //nolint:errcheck
		close(done)
	}()
	time.Sleep(wait)
	// Signal shutdown via the context owned by tr.owner.
	// The test must cancel the context after calling this helper.
	<-done
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "demo" {
		t.Errorf("name = %q, want demo", ua.GetName())
	}
	if _, ok := ua.GetServices()["mquery"]; !ok {
		t.Error("expected 'mquery' service in ServicesMap")
	}
	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits must be *Traits")
	}
	if tr.FluxURL == "" {
		t.Error("FluxURL must have a default value")
	}
	if tr.Bucket == "" {
		t.Error("Bucket must have a default value")
	}
	if len(tr.Measurements) == 0 {
		t.Error("expected at least one default measurement")
	}
}

// ── Traits serialization ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	original := Traits{
		FluxURL: "http://localhost:8086",
		Token:   "tok",
		Org:     "myorg",
		Bucket:  "mybucket",
		Measurements: []MeasurementT{
			{Name: "pressure", Details: map[string][]string{"Unit": {"kPa"}}, Period: 4},
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Traits
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.FluxURL != "http://localhost:8086" {
		t.Errorf("FluxURL = %q, want http://localhost:8086", decoded.FluxURL)
	}
	if decoded.Bucket != "mybucket" {
		t.Errorf("Bucket = %q, want mybucket", decoded.Bucket)
	}
	if len(decoded.Measurements) != 1 || decoded.Measurements[0].Name != "pressure" {
		t.Errorf("unexpected measurements: %v", decoded.Measurements)
	}
	// Internal fields must not appear in JSON.
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	for _, hidden := range []string{"owner", "cervices", "name", "client"} {
		if _, ok := raw[hidden]; ok {
			t.Errorf("field %q must not be exported to JSON", hidden)
		}
	}
}

// ── collectIngest: single provider ───────────────────────────────────────────

func TestCollectIngestSingleProvider(t *testing.T) {
	srv := newSignalServer(t, 42.5, "kPa")

	ctx, cancel := context.WithCancel(context.Background())
	sys := newTestSystem(ctx)

	cer := &components.Cervice{
		Definition: "pressure",
		Protos:     []string{"http"},
		Nodes: map[string][]components.NodeInfo{
			"sensor1": {{URL: srv.URL, Details: map[string][]string{"Unit": {"kPa"}}}},
		},
	}
	mwa := &mockWriteAPI{}
	tr := &Traits{
		owner:    &sys,
		cervices: components.Cervices{"pressure": cer},
	}

	done := make(chan struct{})
	go func() { tr.collectIngest("pressure", 50*time.Millisecond, mwa); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	pts := mwa.captured()
	if len(pts) == 0 {
		t.Fatal("expected at least one point written")
	}
	p := pts[0]
	if p.Name() != "pressure" {
		t.Errorf("measurement name = %q, want pressure", p.Name())
	}
	tags := tagMap(p)
	if tags["source"] != "sensor1" {
		t.Errorf("source tag = %q, want sensor1", tags["source"])
	}
	// Unit tag must come from the NodeInfo.Details, not the cervice-level filter.
	if tags["Unit"] != "kPa" {
		t.Errorf("Unit tag = %q, want kPa", tags["Unit"])
	}
	if v := fieldValue(p, "value"); v != 42.5 {
		t.Errorf("value field = %v, want 42.5", v)
	}
}

// ── collectIngest: multiple providers ────────────────────────────────────────

func TestCollectIngestMultipleProviders(t *testing.T) {
	srv1 := newSignalServer(t, 10.0, "kPa")
	srv2 := newSignalServer(t, 20.0, "bar")

	ctx, cancel := context.WithCancel(context.Background())
	sys := newTestSystem(ctx)

	cer := &components.Cervice{
		Definition: "pressure",
		Protos:     []string{"http"},
		Nodes: map[string][]components.NodeInfo{
			"sensor1": {{URL: srv1.URL, Details: map[string][]string{"Unit": {"kPa"}}}},
			"sensor2": {{URL: srv2.URL, Details: map[string][]string{"Unit": {"bar"}}}},
		},
	}
	mwa := &mockWriteAPI{}
	tr := &Traits{
		owner:    &sys,
		cervices: components.Cervices{"pressure": cer},
	}

	done := make(chan struct{})
	go func() { tr.collectIngest("pressure", 50*time.Millisecond, mwa); close(done) }()
	time.Sleep(200 * time.Millisecond) // allow several ticks so both providers are polled
	cancel()
	<-done

	pts := mwa.captured()
	if len(pts) < 2 {
		t.Fatalf("expected at least 2 points (one per provider), got %d", len(pts))
	}

	// Group by source tag; each provider must appear at least once with the right value and unit.
	type providerStats struct {
		values []float64
		unit   string
	}
	bySource := make(map[string]*providerStats)
	for _, p := range pts {
		tags := tagMap(p)
		src := tags["source"]
		if bySource[src] == nil {
			bySource[src] = &providerStats{unit: tags["Unit"]}
		}
		if v, ok := fieldValue(p, "value").(float64); ok {
			bySource[src].values = append(bySource[src].values, v)
		}
	}

	for _, tc := range []struct {
		source    string
		wantValue float64
		wantUnit  string
	}{
		{"sensor1", 10.0, "kPa"},
		{"sensor2", 20.0, "bar"},
	} {
		stats, ok := bySource[tc.source]
		if !ok {
			t.Errorf("no points received from %s", tc.source)
			continue
		}
		for _, v := range stats.values {
			if v != tc.wantValue {
				t.Errorf("%s: value = %v, want %v", tc.source, v, tc.wantValue)
			}
		}
		if stats.unit != tc.wantUnit {
			t.Errorf("%s: Unit tag = %q, want %q", tc.source, stats.unit, tc.wantUnit)
		}
	}
}

// ── collectIngest: provider failure resets nodes ──────────────────────────────

func TestCollectIngestProviderFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	sys := newTestSystem(ctx)

	cer := &components.Cervice{
		Definition: "pressure",
		Protos:     []string{"http"},
		Nodes: map[string][]components.NodeInfo{
			"sensor1": {{URL: srv.URL}},
		},
	}
	tr := &Traits{
		owner:    &sys,
		cervices: components.Cervices{"pressure": cer},
	}

	done := make(chan struct{})
	go func() { tr.collectIngest("pressure", 50*time.Millisecond, &mockWriteAPI{}); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	// After a provider failure collectIngest resets Nodes so re-discovery
	// is triggered on the next tick.
	if len(cer.Nodes) != 0 {
		t.Errorf("expected Nodes to be reset after provider failure, got %d entries", len(cer.Nodes))
	}
}

// ── collectIngest: unexpected response form is skipped ────────────────────────

func TestCollectIngestBadForm(t *testing.T) {
	// Server returns a valid 200 but with a form the collector cannot use.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// SignalB is not a SignalA_v1a — type assertion in collectIngest will fail.
		json.NewEncoder(w).Encode(forms.SignalB_v1a{Value: true, Version: "SignalB_v1.0"})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	sys := newTestSystem(ctx)

	cer := &components.Cervice{
		Definition: "switch",
		Protos:     []string{"http"},
		Nodes: map[string][]components.NodeInfo{
			"device1": {{URL: srv.URL}},
		},
	}
	mwa := &mockWriteAPI{}
	tr := &Traits{
		owner:    &sys,
		cervices: components.Cervices{"switch": cer},
	}

	done := make(chan struct{})
	go func() { tr.collectIngest("switch", 50*time.Millisecond, mwa); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	// No points should be written because the form type assertion failed every time.
	if len(mwa.captured()) != 0 {
		t.Errorf("expected 0 points for unsupported form, got %d", len(mwa.captured()))
	}
}

// ── serving dispatcher ────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	tr := &Traits{name: "testbucket"}

	t.Run("unknown service returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/collector/demo/unknown", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("non-GET to mquery returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/collector/demo/mquery", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "mquery")
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("DELETE to mquery returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/collector/demo/mquery", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "mquery")
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}
