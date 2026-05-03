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

// Package main is the entry point for the Drafter system.
// Drafter is a skeleton / template for building mbaigo unit-asset systems.
// It exposes two services:
//
//   - hello  – returns "Hello Integrated World!" (stateless, no goroutines needed)
//   - metric – samples the Go runtime goroutine count every second via a
//              channel-based goroutine; concurrent HTTP requests are serialised
//              through the channel so no mutex is required.
//
// Students should start here (main, serving) and in thing.go (Traits, logic).

package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// ── graceful-shutdown context ──────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── system instantiation ───────────────────────────────────────────────────
	sys := components.NewSystem("drafter", ctx)

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "skeleton system for learning the mbaigo architecture",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20192, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/Drafter",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
	}

	// ── unit-asset bootstrap ───────────────────────────────────────────────────
	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear template slot
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// ── Arrowhead registration & servers ──────────────────────────────────────
	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	// ── wait for Ctrl-C ────────────────────────────────────────────────────────
	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// serving is the HTTP dispatcher called by the mbaigo framework for every
// incoming request.  servicePath is the sub-path segment from the URL, e.g.
// "hello" or "metric".
//
// Add a new case here whenever you add a new service to thing.go.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "hello":
		t.hello(w, r)
	case "metric":
		t.readMetric(w, r)
	default:
		http.Error(w,
			"Invalid service path — do not edit SubPath values in the config file",
			http.StatusBadRequest)
	}
}
