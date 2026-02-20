/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("emulator", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "replays signals stored in JSON, XML or CSV files",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20156, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/ds18b20",
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
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate

	// Configure the system
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	var cleanups []func()
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		cleanups = append(cleanups, cleanup)
		defer cleanup() // ensure cleanup is called when the program exits
		sys.UAssets[ua.GetName()] = &ua
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the requests handlers and servers
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs // wait for a SIGINT (Ctrl+C) signal
	log.Println("\nshuting down system", sys.Name)
	cancel()                    // cancel the context, signaling the goroutines to stop
	time.Sleep(2 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// Serving handles the resources services. NOTE: it expects those names from the request URL path
func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "access":
		ua.readSignal(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// readSignal gets the unit asset's signal datum and sends it in a signal form
func (ua *UnitAsset) readSignal(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		getMeasuremet := STray{
			Action: "read",
			// Buffer 1 prevents emulateAsset from blocking forever if the handler exits early.
			ValueP: make(chan forms.SignalA_v1a, 1),
			Error:  make(chan error, 1),
		}

		// IMPORTANT: Protect the send. Your previous code could block forever here.
		select {
		case ua.trayChan <- getMeasuremet:
			// delivered
		case <-r.Context().Done():
			http.Error(w, "Request cancelled", http.StatusRequestTimeout)
			log.Println("Signal reading request cancelled by client")
			return
		case <-time.After(1 * time.Second):
			http.Error(w, "Asset busy", http.StatusGatewayTimeout)
			log.Println("Failure to enqueue signal reading request (asset busy)")
			return
		}

		// Now wait for the response (or timeout/cancel)
		select {
		case err := <-getMeasuremet.Error:
			fmt.Printf("Logic error in getting measurement, %s\n", err)
			w.WriteHeader(http.StatusInternalServerError)
			return

		case signalForm := <-getMeasuremet.ValueP:
			usecases.HTTPProcessGetRequest(w, r, &signalForm)
			return

		case <-r.Context().Done():
			http.Error(w, "Request cancelled", http.StatusRequestTimeout)
			log.Println("Signal reading request cancelled while waiting for response")
			return

		case <-time.After(5 * time.Second):
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process signal reading request (timed out waiting for response)")
			return
		}

	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
