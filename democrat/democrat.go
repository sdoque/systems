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

// democrat is an mbaigo system that bridges an Arrowhead local cloud to
// Industry 4.0 Asset Administration Shell (AAS) infrastructure.
//
// It reads the semantic model of the local cloud from GraphDB — populated by
// the kgrapher system — and upserts one AAS per Arrowhead system into a FA³ST
// AAS server (https://github.com/FraunhoferIOSB/FAAAST-Service).
//
// Each AAS has three submodels:
//
//	Identity  — system name and semantic URI
//	Host      — hostname and IP addresses (when known to kgrapher)
//	Services  — service URLs keyed by service name and definition
//
// Services:
//
//	GET /democrat/assembler/sync    — trigger an immediate sync; returns SyncResult JSON
//	GET /democrat/assembler/status  — return the last SyncResult without triggering a new sync
//
// Configuration (systemconfig.json traits):
//
//	graphdbUrl    — SPARQL SELECT endpoint (e.g. http://localhost:7200/repositories/Arrowhead)
//	faaastUrl     — FA³ST REST API v3 base URL (e.g. http://localhost:8080/api/v3.0)
//	syncInterval  — seconds between automatic background syncs (default 300)

package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("democrat", ctx)
	sys.Husk = &components.Husk{
		Description: "bridges the Arrowhead local cloud knowledge graph to FA³ST Asset Administration Shells",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20195, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/democrat",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
		RegistrarChan: make(chan *components.CoreSystem, 1),
		Messengers:    make(map[string]int),
	}

	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving dispatches HTTP requests to the correct service handler.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "sync":
		t.syncHandler(w, r)
	case "status":
		t.statusHandler(w, r)
	default:
		http.Error(w, "Invalid service path", http.StatusBadRequest)
	}
}

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
