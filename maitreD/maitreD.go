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
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// staleCachePath is a file this system used to write and no longer does.
//
// The maitreD once kept a copy of the CA's whitelist on disk so it could start
// while the CA was unreachable — a case that cannot arise, because the CA is
// the only thing that ever asks for an attestation. What the file could do was
// grant a certificate to any binary whose hash somebody added to it. It is
// removed at startup rather than merely abandoned, because a dead file with
// that consequence should not be left lying on a deployed host.
const staleCachePath = "whitelist.cache.json"

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be canceled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("maitreD", ctx)

	// Watch for SIGINT immediately so that Ctrl+C can interrupt blocking
	// startup steps (the RequestCertificate retry loop).
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

	removeStaleCache(staleCachePath)

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
	case "loadstatus":
		t.loadstatus(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// removeStaleCache deletes the whitelist cache left by earlier versions.
func removeStaleCache(path string) {
	err := os.Remove(path)
	if err == nil {
		log.Printf("removed the obsolete whitelist cache %s: this system no longer holds a whitelist", path)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		log.Printf("warning: could not remove the obsolete whitelist cache %s: %v", path, err)
	}
}
