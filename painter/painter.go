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

// The painter shows an operator what the local cloud is doing.
//
// It reads the same thing the KGrapher reads — each system's own account of
// itself — and does none of the same work with it. There is no triple store, no
// SPARQL, no reasoner and no history: the picture is the cloud as it is now,
// and when it changes the picture changes. Anyone wanting to ask a question of
// the cloud should use the KGrapher, which remembers; anyone wanting to see the
// cloud should look here.
//
// Two systems rather than one, because the two jobs pull in opposite
// directions. A knowledge graph must be correct, complete and durable, and
// pays for it in machinery. A picture must be immediate and legible, and can
// be a few seconds out of date without anyone being misled.
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
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("painter", ctx)

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "paints the local cloud, its hosts, systems and the services that connect them",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30107, "http": 20107, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/painter",
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

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	// Configure the system
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("Configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the template
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// Generate PKI keys and CSR to obtain an authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the system and its services
	usecases.RegisterServices(&sys)

	// start the request handlers and servers
	go usecases.SetoutServers(&sys)

	// wait for the shutdown signal
	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// serving dispatches a request to the service that answers it.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "view":
		t.view(w, r)
	case "model":
		t.model(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}
