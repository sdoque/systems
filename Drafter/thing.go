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

// thing.go contains everything that is unique to this unit asset:
//
//  1. The Traits struct  – configurable parameters and runtime state.
//  2. initTemplate       – the default/template unit asset (used to generate
//                          systemconfig.json on first run).
//  3. newResource        – creates a live unit asset from the config file.
//  4. sampleLoop         – background goroutine that reads the "sensor" value
//                          at regular intervals and handles GET requests safely
//                          through a channel (the "tray" pattern).
//  5. Service handlers   – hello and readMetric (called by serving in drafter.go).
//
// ── How the tray pattern works ────────────────────────────────────────────────
//
// A single goroutine (sampleLoop) owns the mutable state (value, tStamp).
// HTTP handlers never touch that state directly.  Instead they send an STray
// — a struct containing a reply channel — into trayChan, then block on the
// reply channel.  sampleLoop receives the tray, fills in the reply, and sends
// it back.  Because only one goroutine reads and writes the state, there is
// no data race and no mutex is needed.
//
//	HTTP handler          trayChan            sampleLoop goroutine
//	────────────────      ──────────────      ──────────────────────
//	order := STray{...}
//	trayChan <- order  ─────────────────────> order := <-trayChan
//	                                           fill order.ValueP
//	f := <-order.ValueP  <───────────────────  order.ValueP <- f
//
// ── To adapt this template ────────────────────────────────────────────────────
//
//  1. Replace `sampleMetric()` with your actual sensor-read function.
//  2. Change the unit string in sampleLoop ("goroutines" → your unit).
//  3. Add or remove service handlers and register them in serving() in drafter.go.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ── STray: the request/response envelope sent through trayChan ────────────────

// STray carries a GET request from an HTTP handler to the sampleLoop goroutine.
// ValueP receives the populated form; Error receives any sampling error.
type STray struct {
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

// ── Traits: unit-asset configuration and runtime state ────────────────────────

// Traits holds all parameters and internal state for one unit asset instance.
//
// JSON-tagged fields are written to / read from systemconfig.json.
// Fields tagged `json:"-"` are runtime-only and are never serialised.
type Traits struct {
	// SampleRate is the interval (in seconds) between sensor reads.
	// Adjust this in systemconfig.json to change the sampling frequency.
	SampleRate int `json:"sampleRate"`

	// ── runtime state (not serialised) ───────────────────────────────────────
	value    float64    `json:"-"` // most-recently sampled value
	tStamp   time.Time  `json:"-"` // timestamp of that sample
	trayChan chan STray  `json:"-"` // channel connecting handlers ↔ sampleLoop
}

// ── initTemplate: default unit asset ─────────────────────────────────────────

// initTemplate returns a UnitAsset populated with sensible defaults.
// mbaigo calls this once to produce a systemconfig.json template when no
// config file is found.  Modify the defaults here, not in the running code.
func initTemplate() *components.UnitAsset {

	// ── service definitions ────────────────────────────────────────────────────

	// hello: stateless greeting — no channels, no goroutines.
	helloSvc := components.Service{
		Definition:  "greeting",
		SubPath:     "hello",
		Details:     map[string][]string{"Forms": {"text/plain"}},
		RegPeriod:   30,
		Description: "returns 'Hello Integrated World!' (GET)",
	}

	// metric: live goroutine count — demonstrates the tray pattern.
	metricSvc := components.Service{
		Definition:  "metric",
		SubPath:     "metric",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "returns the current Go runtime goroutine count (GET)",
	}

	return &components.UnitAsset{
		Name:    "DraftAsset",
		Mission: "demonstrate_mbaigo_patterns",
		Details: map[string][]string{"FunctionalLocation": {"Lab"}},
		ServicesMap: components.Services{
			helloSvc.SubPath:  &helloSvc,
			metricSvc.SubPath: &metricSvc,
		},
		Traits: &Traits{
			SampleRate: 1, // sample every second
		},
	}
}

// ── newResource: live unit asset from config ──────────────────────────────────

// newResource instantiates a unit asset from the parsed systemconfig.json entry.
// It wires up the trayChan, starts the sampleLoop goroutine, and returns a
// cleanup function that the framework will call on shutdown.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		trayChan: make(chan STray),
	}

	// Copy JSON-tagged fields from the config file into t.
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("warning: could not unmarshal Traits:", err)
		}
	}
	if t.SampleRate <= 0 {
		t.SampleRate = 1 // safety default
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// Start the background sampler.
	go t.sampleLoop(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting unit asset %s\n", configuredAsset.Name)
	}
}

// ── sampleLoop: the "sensor" goroutine ────────────────────────────────────────

// sampleLoop runs for the lifetime of the system context.  It does two things:
//
//  1. Every SampleRate seconds it calls sampleMetric() and stores the result.
//  2. Whenever an HTTP handler sends an STray on trayChan, it replies with the
//     last stored value.
//
// Both activities are handled inside a single select, so reads and writes to
// (value, tStamp) are always sequential — no mutex required.
func (t *Traits) sampleLoop(ctx context.Context) {
	defer close(t.trayChan) // signal handlers that the goroutine has exited

	interval := time.Duration(t.SampleRate) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// A separate inner goroutine does the actual sampling and pushes results
	// back via sigChan / tsChan.  This keeps the outer select non-blocking:
	// even if sampleMetric() ever blocks (e.g. real hardware I/O), the outer
	// loop can still service incoming HTTP requests immediately.
	sigChan := make(chan float64)
	tsChan := make(chan time.Time)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v := sampleMetric() // ← replace with your sensor read
				select {
				case sigChan <- v:
					tsChan <- time.Now()
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("sampleLoop: context cancelled, stopping")
			return

		case v := <-sigChan:
			// New sample arrived from the inner goroutine — store it.
			t.value = v
			t.tStamp = <-tsChan

		case order := <-t.trayChan:
			// An HTTP handler is waiting for the current value — send it.
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = t.value
			f.Unit = "goroutines" // ← change to your unit, e.g. "Celsius"
			f.Timestamp = t.tStamp
			order.ValueP <- f
		}
	}
}

// sampleMetric reads the "sensor" value.
//
// Here we return the number of live goroutines in the current process — a
// real, time-varying value that requires no hardware and works on every
// platform.  Replace this function body with your actual sensor read, e.g.:
//
//	data, err := os.ReadFile("/sys/bus/w1/devices/28-xxx/w1_slave")
func sampleMetric() float64 {
	return float64(runtime.NumGoroutine())
}

// ── Service handlers ───────────────────────────────────────────────────────────

// hello handles GET /hello.
// It is a stateless handler: it needs no shared state and therefore no channel.
// This is the simplest possible mbaigo service handler.
func (t *Traits) hello(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		msg := "Hello Integrated World!"
		log.Println(msg) // visible in the terminal
		fmt.Fprint(w, msg)
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

// readMetric handles GET /metric.
// It sends an STray into trayChan and blocks until sampleLoop replies.
// The 5-second timeout prevents a stalled goroutine from hanging the request.
func (t *Traits) readMetric(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		order := STray{
			ValueP: make(chan forms.SignalA_v1a),
			Error:  make(chan error),
		}
		t.trayChan <- order // send request to sampleLoop
		select {
		case err := <-order.Error:
			log.Printf("readMetric: sampling error: %v\n", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		case f := <-order.ValueP:
			usecases.HTTPProcessGetRequest(w, r, &f)
		case <-time.After(5 * time.Second):
			http.Error(w, "request timed out", http.StatusGatewayTimeout)
			log.Println("readMetric: timed out waiting for sampleLoop")
		}
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}
