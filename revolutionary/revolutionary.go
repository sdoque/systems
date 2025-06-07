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
	"io"
	"log"
	"mime"
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
	sys := components.NewSystem("revolutionary", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "interacts with the RevPi Connect 4 PLC",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20153, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/revolutionary",
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
		// defer cleanup()
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
	cancel() // cancel the context, signaling the goroutines to stop
	for _, cleanup := range cleanups {
		cleanup()
	}
	time.Sleep(2 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// Serving handles the resources services. NOTE: it expects those names from the request URL path
func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "access":
		ua.access(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// access gets the unit asset's AIO channel datum and sends it in a signal form
func (ua *UnitAsset) access(w http.ResponseWriter, r *http.Request) {

	switch r.Method {
	case http.MethodGet:
		// Prepare a fresh tray for this request
		requestTray := ServiceTray{
			SampledDatum: make(chan forms.SignalA_v1a),
			Error:        make(chan error),
		}
		ua.serviceChannel <- requestTray
		select {
		case err := <-requestTray.Error:
			log.Printf("Logic error in getting measurement: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		case signalForm := <-requestTray.SampledDatum:
			usecases.HTTPProcessGetRequest(w, r, &signalForm)
			return
		case <-time.After(5 * time.Second):
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Timeout on GET access")
			return
		}

	case http.MethodPost, http.MethodPut:
		// Unpack the incoming form
		log.Printf("Unpacking output signal form for %s", ua.Name)
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Printf("Error parsing media type: %v", err)
			http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("Error reading request body: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		serviceReq, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("Error unpacking output signal form: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		outputForm, ok := serviceReq.(*forms.SignalA_v1a) // Ensure the form is of the expected type
		if !ok {
			log.Println("Unexpected form type in access")
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		ua.outputChannel <- outputForm.Value // Send the value to the output channel for processing
		w.WriteHeader(http.StatusOK)         // Respond with 200 OK if the write is successful

	default:
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
	}
}
