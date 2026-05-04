/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
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
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// whitelistCachePath is the on-disk location of the maitreD's CA-synced
// whitelist cache. Hard-coded relative to the working directory because it
// is runtime state, not operator-tunable config.
const whitelistCachePath = "whitelist.cache.json"

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("maitreD", ctx)

	// Watch for SIGINT immediately so that Ctrl+C can interrupt blocking
	// startup steps (RequestCertificate retry loop, whitelist bootstrap).
	usecases.WatchShutdown(&sys, cancel)

	// Instantiate the husk
	sys.Husk = &components.Husk{
		Description: "supports systems on local host computer to authenticate themselves towards the CA.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30101, "http": 20101, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/maitreD",
		DName: pkix.Name{
			CommonName:         "maitreD",
			Country:            []string{"SE"},
			Province:           []string{"Norrbotten"},
			Locality:           []string{"Luleaa"},
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Research"},
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
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Bootstrap the whitelist from the CA. This must happen after enrollment
	// (we need the CA URL and, eventually, mTLS) and before service registration
	// so that attestation requests cannot arrive against an unloaded whitelist.
	if err := bootstrapWhitelist(&sys); err != nil {
		log.Fatalf("whitelist bootstrap failed: %v", err)
	}

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the requests handlers and servers
	go usecases.SetoutServers(&sys)

	// Wait for shutdown. WatchShutdown's goroutine cancels ctx on SIGINT;
	// goroutines that respect ctx.Done() exit; the brief sleep covers
	// in-flight HTTP handlers and other non-cancellable cleanup.
	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// serving handles the resources services. NOTE: it expects those names from the request URL path
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "attest":
		t.attest(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}
