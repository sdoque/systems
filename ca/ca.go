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

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("ca", ctx)

	// Watch for SIGINT immediately so that Ctrl+C interrupts blocking
	// startup steps (currently none in the CA, but kept consistent with
	// every other system so future changes do not introduce un-killable
	// states).
	usecases.WatchShutdown(&sys, cancel)

	// Instantiate the husk
	sys.Husk = &components.Husk{
		Description: "handles X.509 certification for its local cloud",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30100, "http": 20100, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/ca",
		DName: pkix.Name{
			CommonName:         "ca",
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
		log.Fatalf("Configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// The CA does not enrol with itself: its self-signed root cert was loaded
	// (or created) by newResource via ensureCertificate. Close the CertReady
	// channel that mbaigo's SetoutServers waits on, so the HTTPS goroutine
	// can bind without going through RequestCertificate (which would
	// nonsensically attempt CA-to-itself enrolment).
	close(usecases.EnsureCertReady(&sys))

	// start the http handler and server
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
	case "certify":
		t.certify(w, r)
	case "whitelist":
		t.whitelisting(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// certify processes certificate signing request
func (t *Traits) certify(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		t.certifying(w, r)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
