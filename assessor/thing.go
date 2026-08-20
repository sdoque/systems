/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ValuationFileName is where the owner's judgment lives, beside the binary like
// the authorizer's policies.json.
const ValuationFileName = "valuation.json"

// Traits are the assessor's asset-specific parameters.
type Traits struct {
	// GraphDBURL is the SPARQL SELECT endpoint, e.g.
	// http://localhost:7200/repositories/Arrowhead
	GraphDBURL string `json:"graphdbUrl"`

	owner *components.System
	name  string

	mu        sync.RWMutex
	valuation *ValuationFile
	loadedAt  time.Time

	// assessing is a test seam, so the handlers can be driven without a store.
	assessing func() (*Cloud, []*Finding, error)
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	fmea := components.Service{
		Definition: "fmea",
		SubPath:    "fmea",
		Details: map[string][]string{
			"Forms":   {"text/csv"},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   300,
		Description: "the cloud's failure modes and effects, derived from the knowledge graph and scored against valuation.json (GET, text/csv)",
	}
	scope := components.Service{
		Definition: "scope",
		SubPath:    "scope",
		Details: map[string][]string{
			"Forms":   {"application/json"},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   300,
		Description: "what the assessment covers: every unit asset, what it provides, what it depends on (GET)",
	}

	return &components.UnitAsset{
		Name: "analyst",
		// It reads what every system says and reasons over the whole, which is
		// aggregation. It drives nothing and measures nothing.
		Mission: components.MissionAggregation,
		Details: map[string][]string{"Mobility": {components.MobilityTethered}, "TetheredTo": {"GraphDB"}},
		ServicesMap: components.Services{
			fmea.SubPath:  &fmea,
			scope.SubPath: &scope,
		},
		Traits: &Traits{GraphDBURL: "http://localhost:7200/repositories/Arrowhead"},
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{owner: sys, name: uac.Name}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("assessor: could not unmarshal traits:", err)
		}
	}
	if t.GraphDBURL == "" {
		t.GraphDBURL = "http://localhost:7200/repositories/Arrowhead"
	}

	// Loaded at startup so a broken file is a startup failure rather than a
	// surprise on the first request. Fatal, like the authorizer: an assessment
	// scored against a file nobody could read is worse than no assessment.
	if err := t.reloadValuation(); err != nil {
		log.Fatalf("assessor: %v\n", err)
	}

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
		log.Println("assessor: closing the file")
	}
}

//-------------------------------------The valuation file

// reloadValuation reads the file when it has changed, so an owner can revise a
// judgment without restarting the system — and, having revised it, sees the
// change in the next assessment rather than the next deployment.
//
// A file that no longer parses leaves the previous ratings in place. Reverting
// to unscored on a typo would empty every rating in the next CSV somebody
// exported, and an FMEA with no numbers looks like an assessment that found
// nothing rather than one that failed to load.
func (t *Traits) reloadValuation() error {
	info, err := os.Stat(ValuationFileName)
	if os.IsNotExist(err) {
		if t.valuation == nil {
			return newEmptyValuationError()
		}
		return nil
	}
	if err != nil {
		return err
	}

	t.mu.RLock()
	unchanged := info.ModTime().Equal(t.loadedAt)
	t.mu.RUnlock()
	if unchanged {
		return nil
	}

	loaded, err := LoadValuation(ValuationFileName)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.valuation = loaded
	t.loadedAt = info.ModTime()
	t.mu.Unlock()
	log.Printf("assessor: valued %d effect classes, %d cause classes and %d detection classes from %s\n",
		len(loaded.Severity), len(loaded.Occurrence), len(loaded.Detection), ValuationFileName)
	return nil
}

// currentValuation returns the ratings to score against, refreshing them first.
func (t *Traits) currentValuation() *ValuationFile {
	if err := t.reloadValuation(); err != nil {
		log.Printf("assessor: keeping the previous valuation: %v\n", err)
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.valuation
}

//-------------------------------------The assessment

// assess reads the cloud and derives the findings.
func (t *Traits) assess() (*Cloud, []*Finding, error) {
	if t.assessing != nil {
		return t.assessing()
	}
	client := &http.Client{Timeout: 30 * time.Second}
	cloud, err := ReadCloud(client, t.GraphDBURL)
	if err != nil {
		return nil, nil, err
	}
	return cloud, Assess(cloud), nil
}

//-------------------------------------Service handlers

// fmea serves the assessment as CSV.
func (t *Traits) fmea(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
		return
	}
	cloud, findings, err := t.assess()
	if err != nil {
		log.Printf("assessor: %v\n", err)
		http.Error(w, "cannot read the knowledge graph: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Rendered into memory first: a half-written CSV with a 200 already sent
	// reads as a complete assessment that happens to be short.
	var body bytes.Buffer
	if err := WriteCSV(&body, cloud, findings, t.currentValuation(), time.Now()); err != nil {
		log.Printf("assessor: writing the assessment: %v\n", err)
		http.Error(w, "cannot render the assessment", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+usecases.ForLog(cloud.Name)+`_FMEA.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(body.Bytes()); err != nil {
		log.Printf("assessor: writing the assessment: %v\n", err)
	}
}

// scope serves what the assessment covers, so a reader can see the model the
// findings were derived from rather than take the findings on trust.
func (t *Traits) scope(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
		return
	}
	cloud, _, err := t.assess()
	if err != nil {
		log.Printf("assessor: %v\n", err)
		http.Error(w, "cannot read the knowledge graph: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	type svcOut struct {
		Definition   string   `json:"definition"`
		Subscribable bool     `json:"subscribable"`
		Unit         string   `json:"unit,omitempty"`
		Consumers    int      `json:"consumers"`
		Methods      []string `json:"methods,omitempty"`
	}
	type assetOut struct {
		System   string   `json:"system"`
		Asset    string   `json:"asset"`
		Mission  string   `json:"mission"`
		Location string   `json:"location,omitempty"`
		Provides []svcOut `json:"provides,omitempty"`
		Consumes []string `json:"consumes,omitempty"`
	}
	out := struct {
		Cloud  string              `json:"cloud"`
		Hosts  map[string][]string `json:"hosts"`
		Assets []assetOut          `json:"assets"`
	}{Cloud: cloud.Name, Hosts: cloud.Hosts}

	for _, a := range cloud.Assets {
		row := assetOut{System: a.System, Asset: a.Name, Mission: a.Mission, Location: a.Location}
		for _, s := range a.Provides {
			row.Provides = append(row.Provides, svcOut{
				Definition: s.Definition, Subscribable: s.Subscribable,
				Unit: s.Unit, Consumers: len(s.Consumers), Methods: s.Methods,
			})
		}
		for _, c := range a.Consumes {
			row.Consumes = append(row.Consumes, c.Definition)
		}
		out.Assets = append(out.Assets, row)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("assessor: writing the scope: %v\n", err)
	}
}
