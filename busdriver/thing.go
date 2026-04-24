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

// thing.go contains the unit-asset logic for the busdriver system:
//
//   - BusConfig / SignalConf  — JSON-serialisable configuration types
//   - Traits                  — per-signal runtime state (one instance per PID)
//   - initTemplate            — default config used to generate systemconfig.json
//   - newResource             — creates all unit assets from a config entry,
//                               opens the CAN socket, and starts the goroutines
//   - assetLoop               — per-signal goroutine; owns (value, tStamp)
//
// Goroutine architecture
// ──────────────────────
// One canPoller goroutine (in can_linux.go) owns the CAN socket and cycles
// through all configured PIDs.  For each PID it sends an OBD-II request and
// delivers the decoded response to the matching signal's updateChan.
//
// Each signal has its own assetLoop goroutine that selects on:
//   - updateChan  — new value from canPoller → store it
//   - trayChan    — HTTP GET request          → reply with stored value
//   - ctx.Done()  — shutdown                  → exit
//
// Because only assetLoop reads and writes (value, tStamp), no mutex is needed.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ── Configuration types (JSON-serialisable) ───────────────────────────────────

// BusConfig is the traits block for one CAN bus entry in systemconfig.json.
// A single entry describes the interface and all signals to expose.
type BusConfig struct {
	Interface string       `json:"interface"` // SocketCAN interface, e.g. "can0"
	Bitrate   int          `json:"bitrate"`   // bus speed in bit/s, e.g. 500000
	Signals   []SignalConf `json:"signals"`   // one entry per OBD-II PID to monitor
}

// SignalConf describes one OBD-II signal to expose as a unit asset.
type SignalConf struct {
	Name string `json:"name"` // asset name — appears in URL and Arrowhead registry
	PID  string `json:"pid"`  // hex PID, e.g. "0x0C"
	Unit string `json:"unit"` // unit string; leave blank to use the pidTable default
}

// ── Runtime types ─────────────────────────────────────────────────────────────

// STray is the request/reply envelope sent through trayChan.
// An HTTP handler creates one, sends it, then blocks on ValueP for the reply.
type STray struct {
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

// pidSubscription pairs a PID with the channel its assetLoop reads values from.
// canPoller holds a slice of these to know where to deliver each decoded value.
type pidSubscription struct {
	pid        uint8
	updateChan chan float64
}

// Traits holds the runtime state for one signal (one unit asset instance).
// Fields are intentionally unexported — all access goes through channels.
type Traits struct {
	pid        uint8        // OBD-II PID
	unit       string       // unit string for SignalA_v1a responses
	value      float64      // most recently decoded value
	tStamp     time.Time    // timestamp of that value
	trayChan   chan STray   // HTTP handlers send requests here
	updateChan chan float64 // canPoller delivers new values here
	name       string       // asset name for log messages
}

// ── initTemplate ──────────────────────────────────────────────────────────────

// initTemplate returns a UnitAsset populated with example OBD-II signals.
// mbaigo calls this once to generate systemconfig.json when the file is absent.
// Edit the Signals slice to change the default set of monitored PIDs.
func initTemplate() *components.UnitAsset {
	access := components.Service{
		Definition:  "signal",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "returns the latest OBD-II signal value (GET)",
	}
	return &components.UnitAsset{
		Name:    "Vehicle",
		Mission: "monitor_vehicle",
		Details: map[string][]string{"FunctionalLocation": {"Car"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Traits: &BusConfig{
			Interface: "can0",
			Bitrate:   500000,
			Signals: []SignalConf{
				{Name: "EngineRPM", PID: "0x0C", Unit: "rpm"},
				{Name: "CoolantTemperature", PID: "0x05", Unit: "Celsius"},
				{Name: "VehicleSpeed", PID: "0x0D", Unit: "km/h"},
			},
		},
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

// newResource parses the BusConfig from systemconfig.json, opens the CAN
// socket, and creates one unit asset per configured signal.  A single
// canPoller goroutine is started to service all signals on this bus.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	var cfg BusConfig
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], &cfg); err != nil {
			log.Fatalf("busdriver: cannot parse traits: %v", err)
		}
	}
	if cfg.Interface == "" {
		cfg.Interface = "can0"
	}
	if cfg.Bitrate == 0 {
		cfg.Bitrate = 500000
	}

	fd, err := openCAN(cfg.Interface)
	if err != nil {
		log.Fatalf("busdriver: cannot open %s: %v", cfg.Interface, err)
	}
	log.Printf("busdriver: opened %s at %d bit/s", cfg.Interface, cfg.Bitrate)

	var subs []pidSubscription
	var assets []*components.UnitAsset

	for _, sc := range cfg.Signals {
		pid, err := parsePID(sc.PID)
		if err != nil {
			log.Printf("busdriver: skipping %q — %v", sc.Name, err)
			continue
		}

		// Use pidTable default unit when the config leaves Unit blank.
		unit := sc.Unit
		if unit == "" {
			if info, lookupErr := lookupPID(pid); lookupErr == nil {
				unit = info.Unit
			} else {
				unit = "undefined"
			}
		}

		t := &Traits{
			pid:        pid,
			unit:       unit,
			trayChan:   make(chan STray),
			updateChan: make(chan float64, 1),
			name:       sc.Name,
		}
		subs = append(subs, pidSubscription{pid: pid, updateChan: t.updateChan})

		details := make(map[string][]string)
		for k, v := range configuredAsset.Details {
			details[k] = v
		}
		details["Unit"] = []string{unit}

		ua := &components.UnitAsset{
			Name:        sc.Name,
			Owner:       sys,
			Details:     details,
			ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
			Traits:      t,
		}
		tc := t
		ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
			serving(tc, w, r, servicePath)
		}

		go t.assetLoop(sys.Ctx)
		assets = append(assets, ua)
	}

	// One canPoller goroutine services all signals on this CAN bus.
	go canPoller(sys.Ctx, fd, subs)

	return assets, func() {
		log.Printf("busdriver: closing %s", cfg.Interface)
		closeCAN(fd)
	}
}

//-------------------------------------Service handlers

// access handles GET requests for the signal's current value.
// It sends an STray to the signal's assetLoop and blocks until a reply arrives.
func (t *Traits) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		order := STray{
			ValueP: make(chan forms.SignalA_v1a),
			Error:  make(chan error),
		}
		t.trayChan <- order
		select {
		case err := <-order.Error:
			log.Printf("access %s: error: %v", t.name, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		case f := <-order.ValueP:
			usecases.HTTPProcessGetRequest(w, r, &f)
		case <-time.After(5 * time.Second):
			http.Error(w, "request timed out", http.StatusGatewayTimeout)
			log.Printf("access %s: timeout", t.name)
		}
	default:
		http.Error(w, "method not supported", http.StatusMethodNotAllowed)
	}
}

// ── assetLoop ─────────────────────────────────────────────────────────────────

// assetLoop is the per-signal goroutine.  It is the sole owner of (value,
// tStamp) and handles two event types without any mutex:
//
//   - updateChan: a new decoded value arrived from canPoller — store it.
//   - trayChan:   an HTTP GET handler is waiting — reply with current value.
func (t *Traits) assetLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Printf("assetLoop %s: stopping", t.name)
			return

		case v := <-t.updateChan:
			t.value = v
			t.tStamp = time.Now()
			fmt.Printf("%s = %.2f %s\n", t.name, t.value, t.unit)

		case order := <-t.trayChan:
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = t.value
			f.Unit = t.unit
			f.Timestamp = t.tStamp
			order.ValueP <- f
		}
	}
}
