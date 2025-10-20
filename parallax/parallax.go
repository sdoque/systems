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
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("parallax", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: " provides a rotation service using a standard servo motor driven with PWM",
		Details:     map[string][]string{"Developer": {"Arrowhead"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20151, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/parallax",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate

	// Configure the system
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("Configuration error: %v\n", err)
	}

	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	var cleanups []func()
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
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

	// start the http handler and server
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs // wait for a SIGINT (Ctrl+C) signal
	fmt.Println("\nshuting down system", sys.Name)
	cancel()                    // cancel the context, signaling the goroutines to stop
	time.Sleep(3 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// Serving handles the resources services. NOTE: it expects those names from the request URL path
func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "rotation":
		ua.rotation(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

func (ua *UnitAsset) rotation(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		positionForm := ua.getPosition()
		usecases.HTTPProcessGetRequest(w, r, &positionForm)
	case "PUT":
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			log.Println("Error with the setting request of the position ", err)
		}
		confirmation, err := ua.setPosition(sig)
		if err != nil {
			log.Println("Error setting the position ", err)
		}
		// return the confirmation of the set operation
		bestContentType := "application/json" // we know what we sent, so we can respond in the same format
		responseData, err := usecases.Pack(&confirmation, bestContentType)
		if err != nil {
			log.Printf("Error packing response: %v", err)
		}
		w.Header().Set("Content-Type", bestContentType)
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(responseData)
		if err != nil {
			log.Printf("Error while writing response: %v", err)
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
