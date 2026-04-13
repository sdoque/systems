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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── sanitizeIDShort ───────────────────────────────────────────────────────────

func TestSanitizeIDShort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"thermostat", "thermostat"},
		{"Nurse System", "Nurse_System"},
		{"123sensor", "S_123sensor"},  // must start with letter
		{"  ", "S_unnamed"},           // blank after trim
		{"a--b__c", "a_b_c"},          // consecutive specials collapsed
		{"_leading", "leading"},        // leading underscore stripped
		{"trailing_", "trailing"},      // trailing underscore stripped
		{"CO2 Sensor #1", "CO2_Sensor_1"},
	}
	for _, tc := range tests {
		got := sanitizeIDShort(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeIDShort(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── b64url ────────────────────────────────────────────────────────────────────

func TestB64url_NoTrailingPadding(t *testing.T) {
	s := "urn:alc:aas:thermostat"
	enc := b64url(s)
	for _, ch := range enc {
		if ch == '=' {
			t.Errorf("b64url(%q) contains padding character '='", s)
		}
	}
	if enc == "" {
		t.Error("b64url returned empty string")
	}
}

// ── titleCaseURL ──────────────────────────────────────────────────────────────

func TestTitleCaseURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"temperature", "TemperatureUrl"},
		{"windSpeed", "WindSpeedUrl"},
		{"", ""},
	}
	for _, tc := range tests {
		got := titleCaseURL(tc.in)
		if got != tc.want {
			t.Errorf("titleCaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── buildAASEnv ───────────────────────────────────────────────────────────────

func TestBuildAASEnv_OneSystem(t *testing.T) {
	systems := map[string]*SystemInfo{
		"http://example.com/sys/thermostat": {
			SystemURI:  "http://example.com/sys/thermostat",
			SystemName: "thermostat",
			HostName:   "pi-office",
			IPs:        []string{"192.168.1.10"},
			Services: []ServiceInfo{
				{ServiceName: "thermostat_temperature", ServiceDef: "temperature", URL: "http://192.168.1.10:20185/thermostat/sensor1/temperature"},
			},
		},
	}

	env := buildAASEnv(systems)

	if len(env.AssetAdministrationShells) != 1 {
		t.Fatalf("expected 1 AAS, got %d", len(env.AssetAdministrationShells))
	}

	aas := env.AssetAdministrationShells[0]
	if aas.IDShort != "thermostat" {
		t.Errorf("IDShort = %q, want thermostat", aas.IDShort)
	}
	if aas.AssetInformation.GlobalAssetID != "http://example.com/sys/thermostat" {
		t.Errorf("GlobalAssetID = %q", aas.AssetInformation.GlobalAssetID)
	}

	// Expect 3 submodels: Identity, Host, Services
	if len(env.Submodels) != 3 {
		t.Errorf("expected 3 submodels, got %d", len(env.Submodels))
	}
}

func TestBuildAASEnv_NoHost(t *testing.T) {
	systems := map[string]*SystemInfo{
		"http://example.com/sys/emulator": {
			SystemURI:  "http://example.com/sys/emulator",
			SystemName: "emulator",
			// No HostName, no IPs
			Services: []ServiceInfo{
				{ServiceName: "emulator_signal", URL: "http://127.0.0.1:20180/emulator/sensor1/signal"},
			},
		},
	}

	env := buildAASEnv(systems)

	// Without host info: only Identity + Services = 2 submodels
	if len(env.Submodels) != 2 {
		t.Errorf("expected 2 submodels (no Host), got %d", len(env.Submodels))
	}
	// AAS refs should also be 2 (no Host ref)
	aas := env.AssetAdministrationShells[0]
	if len(aas.Submodels) != 2 {
		t.Errorf("expected 2 submodel refs, got %d", len(aas.Submodels))
	}
}

func TestBuildAASEnv_MultipleServices_DefinitionShortcut(t *testing.T) {
	systems := map[string]*SystemInfo{
		"http://example.com/sys/ds18b20": {
			SystemURI:  "http://example.com/sys/ds18b20",
			SystemName: "ds18b20",
			Services: []ServiceInfo{
				{ServiceName: "ds18b20_temperature", ServiceDef: "temperature", URL: "http://pi:20110/ds18b20/sensor1/temperature"},
			},
		},
	}

	env := buildAASEnv(systems)
	var svcSM *Submodel
	for i := range env.Submodels {
		if env.Submodels[i].IDShort == "Services" {
			svcSM = &env.Submodels[i]
			break
		}
	}
	if svcSM == nil {
		t.Fatal("Services submodel not found")
	}

	// Should contain both ServiceUrl_ds18b20_temperature AND TemperatureUrl
	found := map[string]bool{}
	for _, el := range svcSM.SubmodelElements {
		found[el.IDShort] = true
	}
	if !found["ServiceUrl_ds18b20_temperature"] {
		t.Error("expected ServiceUrl_ds18b20_temperature in Services submodel")
	}
	if !found["TemperatureUrl"] {
		t.Error("expected TemperatureUrl shortcut in Services submodel")
	}
}

func TestBuildAASEnv_DefinitionShortcut_NotUniqueIsSkipped(t *testing.T) {
	// When two services share the same definition, the shortcut should NOT appear.
	systems := map[string]*SystemInfo{
		"http://example.com/sys/modboss": {
			SystemURI:  "http://example.com/sys/modboss",
			SystemName: "modboss",
			Services: []ServiceInfo{
				{ServiceName: "modboss_coil1", ServiceDef: "OnOff", URL: "http://pi:20120/modboss/coil1/access"},
				{ServiceName: "modboss_coil2", ServiceDef: "OnOff", URL: "http://pi:20120/modboss/coil2/access"},
			},
		},
	}

	env := buildAASEnv(systems)
	var svcSM *Submodel
	for i := range env.Submodels {
		if env.Submodels[i].IDShort == "Services" {
			svcSM = &env.Submodels[i]
			break
		}
	}
	if svcSM == nil {
		t.Fatal("Services submodel not found")
	}

	for _, el := range svcSM.SubmodelElements {
		if el.IDShort == "OnOffUrl" {
			t.Error("OnOffUrl shortcut should not appear when definition maps to multiple URLs")
		}
	}
}

func TestBuildAASEnv_EmptyInput(t *testing.T) {
	env := buildAASEnv(map[string]*SystemInfo{})
	if len(env.AssetAdministrationShells) != 0 {
		t.Errorf("expected 0 AAS for empty input, got %d", len(env.AssetAdministrationShells))
	}
}

func TestBuildAASEnv_StableOrder(t *testing.T) {
	systems := map[string]*SystemInfo{
		"http://example.com/sys/zzz": {SystemURI: "http://example.com/sys/zzz", SystemName: "zzz"},
		"http://example.com/sys/aaa": {SystemURI: "http://example.com/sys/aaa", SystemName: "aaa"},
	}
	env1 := buildAASEnv(systems)
	env2 := buildAASEnv(systems)

	b1, _ := json.Marshal(env1)
	b2, _ := json.Marshal(env2)
	if string(b1) != string(b2) {
		t.Error("buildAASEnv is not deterministic")
	}

	if env1.AssetAdministrationShells[0].IDShort != "aaa" {
		t.Errorf("first AAS should be 'aaa' (sorted), got %q", env1.AssetAdministrationShells[0].IDShort)
	}
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()
	if ua.GetName() != "assembler" {
		t.Errorf("name = %q, want assembler", ua.GetName())
	}
	if _, ok := ua.GetServices()["sync"]; !ok {
		t.Error("sync service missing")
	}
	if _, ok := ua.GetServices()["status"]; !ok {
		t.Error("status service missing")
	}
	cfg, ok := ua.GetTraits().(*DemocratConfig)
	if !ok {
		t.Fatal("Traits should be *DemocratConfig")
	}
	if cfg.GraphDBURL == "" {
		t.Error("GraphDBURL should not be empty")
	}
	if cfg.FAASTURL == "" {
		t.Error("FAASTURL should not be empty")
	}
	if cfg.SyncInterval <= 0 {
		t.Error("SyncInterval should be positive")
	}
}

// ── serving dispatcher ────────────────────────────────────────────────────────

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{triggerChan: make(chan SyncRequest, 1)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/democrat/assembler/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestStatusHandler_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/democrat/assembler/status", nil)
	tr.statusHandler(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestSyncHandler_MethodNotAllowed(t *testing.T) {
	tr := &Traits{triggerChan: make(chan SyncRequest, 1)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/democrat/assembler/sync", nil)
	tr.syncHandler(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestStatusHandler_ReturnsJSON(t *testing.T) {
	tr := &Traits{
		lastResult: SyncResult{
			Time:     time.Now(),
			Systems:  3,
			Upserted: 3,
			Duration: "42ms",
		},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/democrat/assembler/status", nil)
	tr.statusHandler(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var result SyncResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if result.Systems != 3 {
		t.Errorf("Systems = %d, want 3", result.Systems)
	}
	if result.Upserted != 3 {
		t.Errorf("Upserted = %d, want 3", result.Upserted)
	}
}

// ── syncLoop ──────────────────────────────────────────────────────────────────

// TestSyncLoop_TriggerChanDelivery verifies the channel tray pattern:
// a SyncRequest sent to triggerChan causes syncLoop to call runSync and reply
// with a SyncResult.  We stub runSync by using a Traits whose GraphDBURL
// points to a test HTTP server that returns an empty SPARQL result set
// (so loadSystems returns 0 systems, no FA³ST calls are made).
func TestSyncLoop_TriggerChanDelivery(t *testing.T) {
	// Stub SPARQL endpoint that always returns an empty result.
	stubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/sparql-results+json")
		w.Write([]byte(`{"results":{"bindings":[]}}`))
	}))
	defer stubServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{
		GraphDBURL:   stubServer.URL,
		FAASTURL:     "http://localhost:8080/api/v3.0",
		SyncInterval: 3600, // large interval so auto-tick never fires
		triggerChan:  make(chan SyncRequest),
	}

	// We need owner.Ctx for syncLoop's context.Done branch; use a simple stub.
	go tr.syncLoopForTest(ctx)

	resultChan := make(chan SyncResult, 1)
	select {
	case tr.triggerChan <- SyncRequest{ResultChan: resultChan}:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending trigger")
	}

	select {
	case result := <-resultChan:
		// Empty SPARQL → 0 systems → error message but no panic
		if result.Systems != 0 {
			t.Errorf("Systems = %d, want 0", result.Systems)
		}
		if result.Duration == "" {
			t.Error("Duration should not be empty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SyncResult")
	}
}

// syncLoopForTest is identical to syncLoop but uses an explicit context
// instead of owner.Ctx so the test does not need a full components.System.
func (t *Traits) syncLoopForTest(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(t.SyncInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.lastResult = t.runSync()
		case req := <-t.triggerChan:
			result := t.runSync()
			t.lastResult = result
			if req.ResultChan != nil {
				req.ResultChan <- result
			}
		}
	}
}

func TestSyncLoop_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Traits{
		GraphDBURL:   "http://localhost:9999", // unreachable — not called
		SyncInterval: 3600,
		triggerChan:  make(chan SyncRequest),
	}

	done := make(chan struct{})
	go func() {
		tr.syncLoopForTest(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncLoop did not exit after context cancellation")
	}
}
