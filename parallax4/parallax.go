/*******************************************************************************
 * Copyright (c) 2024 Jan van Deventer
 *
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-2.0/
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("parallax", ctx)

	// instatiate the husk
	sys.Husk = &components.Husk{
		Description: " provides a rotation service using a standard servo motor driven with PWM",
		Details:     map[string][]string{"Developer": {"Arrowhead"}},
		ProtoPort:   map[string]int{"https": 0, "http": 8693, "coap": 0},
		InfoLink:    "https://github.com/sdoque/mbaigo/tree/master/parallax",
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	// Configure the system
	rawResources, servsTemp, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("Configuration error: %v\n", err)
	}

	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	//	Resources := make(map[string]*UnitAsset)
	for _, raw := range rawResources {
		var cfg assetConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(cfg, &sys, servsTemp)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the http handler and server
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs // wait for a SIGINT (Ctrl+C) signal
	fmt.Println("\nshuting down system", sys.Name)
	cancel()                    // cancel the context, signaling the goroutines to stop
	time.Sleep(3 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// serving handles the resources services. NOTE: it expects those names from the request URL path
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "rotation":
		t.rotation(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configurration file]", http.StatusBadRequest)
	}
}

func (t *Traits) rotation(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		positionForm := t.getPosition()
		usecases.HTTPProcessGetRequest(w, r, &positionForm)
	case "PUT":
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			log.Println("Error with the setting request of the position ", err)
		}
		t.setPosition(sig)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
