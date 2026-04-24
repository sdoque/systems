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

// thing.go contains the unit-asset logic for the sailor system:
//
//   - NMEAConfig / SignalConf   — JSON-serialisable configuration types
//   - Traits                    — per-signal runtime state (one instance per signal)
//   - initTemplate              — default config used to generate systemconfig.json
//   - newResource               — opens the CAN socket and creates all unit assets
//   - assetLoop                 — per-signal goroutine; owns (value, tStamp)
//
// Goroutine architecture
// ──────────────────────
// One canPoller goroutine (in can_linux.go) owns the CAN socket.  It listens
// for incoming NMEA 2000 frames and sends periodic ISO Requests.  When a frame
// matches a configured signal it decodes the field and delivers the value to
// that signal's updateChan.
//
// Each signal has its own assetLoop goroutine that selects on:
//
//	updateChan  — new value from canPoller → store it
//	trayChan    — HTTP GET request          → reply with stored value
//	ctx.Done()  — shutdown                  → exit
//
// Only assetLoop reads and writes (value, tStamp), so no mutex is needed.

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

// NMEAConfig is the traits block for a NMEA 2000 bus entry in systemconfig.json.
type NMEAConfig struct {
	Interface string       `json:"interface"` // SocketCAN interface, e.g. "can0"
	Signals   []SignalConf `json:"signals"`   // one entry per signal to expose
}

// SignalConf describes one NMEA 2000 signal to expose as a unit asset.
type SignalConf struct {
	Name  string `json:"name"`  // asset name — appears in URL and Arrowhead registry
	PGN   string `json:"pgn"`   // PGN number as decimal string, e.g. "130306"
	Field string `json:"field"` // field name within that PGN, e.g. "WindSpeed"
	Unit  string `json:"unit"`  // unit string; leave blank to use pgnTable default
}

// ── Runtime types ─────────────────────────────────────────────────────────────

// STray is the request/reply envelope sent through trayChan.
// An HTTP handler creates one, sends it, then blocks on ValueP for the reply.
type STray struct {
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

// pgnSubscription pairs a PGN+field key with the channel its assetLoop reads
// decoded values from.  canPoller holds a slice of these.
type pgnSubscription struct {
	pgn        uint32
	field      string
	updateChan chan float64
}

// Traits holds the runtime state for one signal (one unit asset instance).
// All fields are intentionally unexported — all access goes through channels.
type Traits struct {
	pgn        uint32
	field      string
	unit       string
	value      float64
	tStamp     time.Time
	trayChan   chan STray
	updateChan chan float64
	name       string
}

// ── initTemplate ──────────────────────────────────────────────────────────────

// initTemplate returns a UnitAsset populated with representative NMEA 2000
// signals for a typical sailing vessel.
// mbaigo calls this once to generate systemconfig.json when the file is absent.
func initTemplate() *components.UnitAsset {
	access := components.Service{
		Definition:  "signal",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "returns the latest NMEA 2000 signal value (GET)",
	}
	return &components.UnitAsset{
		Name:    "Vessel",
		Mission: "monitor_vessel",
		Details: map[string][]string{"FunctionalLocation": {"Boat"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Traits: &NMEAConfig{
			Interface: "can0",
			Signals: []SignalConf{
				{Name: "WindSpeed", PGN: "130306", Field: "WindSpeed", Unit: "m/s"},
				{Name: "WindAngle", PGN: "130306", Field: "WindAngle", Unit: "rad"},
				{Name: "WaterSpeed", PGN: "128259", Field: "WaterSpeed", Unit: "m/s"},
				{Name: "Heading", PGN: "127250", Field: "Heading", Unit: "rad"},
				{Name: "COG", PGN: "129026", Field: "COG", Unit: "rad"},
				{Name: "SOG", PGN: "129026", Field: "SOG", Unit: "m/s"},
				{Name: "Depth", PGN: "128267", Field: "Depth", Unit: "m"},
			},
		},
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

// newResource parses the NMEAConfig from systemconfig.json, opens the CAN
// socket, and creates one unit asset per configured signal.  A single
// canPoller goroutine is started to service all signals on this bus.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	var cfg NMEAConfig
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], &cfg); err != nil {
			log.Fatalf("sailor: cannot parse traits: %v", err)
		}
	}
	if cfg.Interface == "" {
		cfg.Interface = "can0"
	}

	fd, err := openCAN(cfg.Interface)
	if err != nil {
		log.Fatalf("sailor: cannot open %s: %v", cfg.Interface, err)
	}
	log.Printf("sailor: opened NMEA 2000 interface %s", cfg.Interface)

	var subs []pgnSubscription
	var assets []*components.UnitAsset

	for _, sc := range cfg.Signals {
		pgn, err := parsePGN(sc.PGN)
		if err != nil {
			log.Printf("sailor: skipping %q — %v", sc.Name, err)
			continue
		}

		// Use pgnTable default unit when the config leaves Unit blank.
		unit := sc.Unit
		if unit == "" {
			if fi, lookupErr := lookupPGNField(pgn, sc.Field); lookupErr == nil {
				unit = fi.Unit
			} else {
				unit = "undefined"
			}
		}

		t := &Traits{
			pgn:        pgn,
			field:      sc.Field,
			unit:       unit,
			trayChan:   make(chan STray),
			updateChan: make(chan float64, 1),
			name:       sc.Name,
		}
		subs = append(subs, pgnSubscription{
			pgn:        pgn,
			field:      sc.Field,
			updateChan: t.updateChan,
		})

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

	go canPoller(sys.Ctx, fd, subs)

	return assets, func() {
		log.Printf("sailor: closing %s", cfg.Interface)
		closeCAN(fd)
	}
}

// ── Service handlers ──────────────────────────────────────────────────────────

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
//   - updateChan: a decoded value arrived from canPoller — store it.
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
			fmt.Printf("%s = %.4f %s\n", t.name, t.value, t.unit)

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
