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

// thing.go contains the unit-asset logic for the democrat system:
//
//   - DemocratConfig  — JSON-serialisable configuration (GraphDB + FA³ST URLs)
//   - Traits          — runtime state: last sync result + trigger channel
//   - initTemplate    — default config for systemconfig.json generation
//   - newResource     — creates the unit asset and starts the sync loop
//   - syncLoop        — goroutine; runs periodic syncs and handles trigger requests
//   - runSync         — queries GraphDB, builds AASEnv, upserts everything to FA³ST
//
// Goroutine architecture
// ──────────────────────
// One syncLoop goroutine runs in the background.  It:
//   - fires automatically every SyncInterval seconds (configurable)
//   - responds immediately to trigger requests sent by the HTTP sync handler
//
// HTTP handlers communicate with syncLoop through the channel tray pattern:
// a handler creates a SyncRequest{ResultChan: ch}, sends it to triggerChan,
// and blocks on ResultChan until syncLoop replies with the SyncResult.
// Only syncLoop writes lastResult, so no mutex is needed for that field.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── Configuration ─────────────────────────────────────────────────────────────

// DemocratConfig is the traits block in systemconfig.json.
type DemocratConfig struct {
	// GraphDBURL is the SPARQL SELECT endpoint for the GraphDB repository that
	// holds the kgrapher snapshot, e.g.:
	//   http://localhost:7200/repositories/Arrowhead
	GraphDBURL string `json:"graphdbUrl"`

	// FAASTURL is the FA³ST REST API v3 base URL, e.g.:
	//   http://localhost:8080/api/v3.0
	FAASTURL string `json:"faaastUrl"`

	// SyncInterval is the time in seconds between automatic background syncs.
	// Default: 300 (5 minutes).
	SyncInterval int `json:"syncInterval"`
}

// ── Runtime types ─────────────────────────────────────────────────────────────

// SyncRequest is the envelope sent through triggerChan by an HTTP handler.
// The handler blocks on ResultChan until syncLoop replies.
type SyncRequest struct {
	ResultChan chan SyncResult
}

// SyncResult is the outcome of one sync cycle, returned to the HTTP handler
// and retained as lastResult for the status service.
type SyncResult struct {
	Time     time.Time `json:"time"`
	Systems  int       `json:"systems"`
	Upserted int       `json:"upserted"` // number of AAS shells successfully upserted
	Errors   []string  `json:"errors,omitempty"`
	Duration string    `json:"duration"`
}

// Traits holds the runtime state for the democrat unit asset.
type Traits struct {
	GraphDBURL   string `json:"graphdbUrl"`
	FAASTURL     string `json:"faaastUrl"`
	SyncInterval int    `json:"syncInterval"`

	lastResult  SyncResult
	triggerChan chan SyncRequest
	owner       *components.System
	name        string
}

// ── initTemplate ──────────────────────────────────────────────────────────────

// initTemplate returns the default UnitAsset used to generate systemconfig.json.
func initTemplate() *components.UnitAsset {
	syncSvc := components.Service{
		Definition:  "sync",
		SubPath:     "sync",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   60,
		Description: "triggers an immediate AAS sync and returns the result (GET)",
	}
	statusSvc := components.Service{
		Definition:  "status",
		SubPath:     "status",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   60,
		Description: "returns the result of the last AAS sync without triggering a new one (GET)",
	}
	return &components.UnitAsset{
		Name:    "assembler",
		Mission: "sync_aas",
		Details: map[string][]string{"Type": {"AAS Bridge"}},
		ServicesMap: components.Services{
			syncSvc.SubPath:   &syncSvc,
			statusSvc.SubPath: &statusSvc,
		},
		Traits: &DemocratConfig{
			GraphDBURL:   "http://localhost:7200/repositories/Arrowhead",
			FAASTURL:     "http://localhost:8080/api/v3.0",
			SyncInterval: 300,
		},
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

// newResource creates the runtime unit asset, starts the sync loop, and
// performs an initial sync attempt so the status service returns meaningful
// data immediately after startup.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner:       sys,
		name:        uac.Name,
		triggerChan: make(chan SyncRequest),
	}

	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("democrat: could not unmarshal traits:", err)
		}
	}
	if t.GraphDBURL == "" {
		t.GraphDBURL = "http://localhost:7200/repositories/Arrowhead"
	}
	if t.FAASTURL == "" {
		t.FAASTURL = "http://localhost:8080/api/v3.0"
	}
	if t.SyncInterval <= 0 {
		t.SyncInterval = 300
	}

	go t.syncLoop(sys.Ctx)

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		log.Println("democrat: shutting down")
	}
}

//-------------------------------------Service handlers

// syncHandler handles GET /democrat/assembler/sync.
// It sends a SyncRequest to syncLoop and blocks until the result is ready,
// then returns the SyncResult as JSON.  Times out after 60 seconds.
func (t *Traits) syncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
		return
	}

	resultChan := make(chan SyncResult, 1)
	req := SyncRequest{ResultChan: resultChan}

	select {
	case t.triggerChan <- req:
	case <-time.After(5 * time.Second):
		http.Error(w, "sync loop busy", http.StatusServiceUnavailable)
		return
	}

	select {
	case result := <-resultChan:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	case <-time.After(60 * time.Second):
		http.Error(w, "sync timed out", http.StatusGatewayTimeout)
		log.Printf("democrat: sync handler timed out waiting for syncLoop")
	}
}

// statusHandler handles GET /democrat/assembler/status.
// It returns the last SyncResult without triggering a new sync.
func (t *Traits) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t.lastResult)
}

// ── syncLoop ──────────────────────────────────────────────────────────────────

// syncLoop is the background goroutine that owns lastResult.
// It runs a sync on a periodic ticker and also on demand when an HTTP handler
// sends a SyncRequest via triggerChan.
func (t *Traits) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(t.SyncInterval) * time.Second)
	defer ticker.Stop()

	// Perform an initial sync shortly after startup so the status service is
	// immediately useful, without blocking newResource.
	go func() {
		time.Sleep(2 * time.Second)
		select {
		case <-ctx.Done():
		case t.triggerChan <- SyncRequest{ResultChan: nil}:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Printf("democrat syncLoop: stopping")
			return

		case <-ticker.C:
			log.Printf("democrat: periodic sync triggered")
			t.lastResult = t.runSync()

		case req := <-t.triggerChan:
			result := t.runSync()
			t.lastResult = result
			// ResultChan is nil for the startup fire.
			if req.ResultChan != nil {
				req.ResultChan <- result
			}
		}
	}
}

// ── runSync ───────────────────────────────────────────────────────────────────

// runSync performs one full sync cycle:
//  1. Query GraphDB for the current local cloud state.
//  2. Build the AASEnv (one AAS per Arrowhead system).
//  3. Upsert every AAS shell and submodel into FA³ST.
//
// It returns a SyncResult summarising what happened.
func (t *Traits) runSync() SyncResult {
	start := time.Now()
	result := SyncResult{Time: start}

	client := &http.Client{Timeout: 15 * time.Second}

	systems, err := loadSystems(client, t.GraphDBURL)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("GraphDB query: %v", err))
		result.Duration = time.Since(start).Round(time.Millisecond).String()
		log.Printf("democrat: sync failed — %v", err)
		return result
	}

	result.Systems = len(systems)
	log.Printf("democrat: %d system(s) found in knowledge graph", len(systems))

	if len(systems) == 0 {
		result.Errors = append(result.Errors,
			"knowledge graph is empty — has kgrapher run and populated urn:state:current?")
		result.Duration = time.Since(start).Round(time.Millisecond).String()
		return result
	}

	env := buildAASEnv(systems)

	// Upsert shells.
	for _, aas := range env.AssetAdministrationShells {
		if err := upsertShell(client, t.FAASTURL, aas); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("upsert AAS %s: %v", aas.IDShort, err))
		} else {
			result.Upserted++
		}
	}

	// Upsert submodels.
	for _, sm := range env.Submodels {
		if err := upsertSubmodel(client, t.FAASTURL, sm); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("upsert submodel %s: %v", sm.IDShort, err))
		}
	}

	result.Duration = time.Since(start).Round(time.Millisecond).String()
	if len(result.Errors) == 0 {
		log.Printf("democrat: sync complete — %d AAS(s) upserted in %s",
			result.Upserted, result.Duration)
	} else {
		log.Printf("democrat: sync complete with %d error(s) — %s",
			len(result.Errors), result.Duration)
	}
	return result
}
