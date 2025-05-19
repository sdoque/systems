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
	"encoding/json"
	"fmt"
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
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate

	// Configure the system
	rawResources, servsTemp, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac UnitAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys, servsTemp)
		defer cleanup()
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
		ua.access(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// access gets the unit asset's AIO channel datum and sends it in a signal form
func (ua *UnitAsset) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		getMeasuremet := STray{
			Action: "read",
			Sample: make(chan forms.SignalA_v1a),
			Error:  make(chan error),
		}
		ua.sampleChan <- getMeasuremet
		select {
		case err := <-getMeasuremet.Error:
			fmt.Printf("Logic error in getting measurement, %s\n", err)
			w.WriteHeader(http.StatusInternalServerError) // Use 500 for an internal error
			return
		case signalForm := <-getMeasuremet.Sample:
			usecases.HTTPProcessGetRequest(w, r, &signalForm)
			return
		case <-time.After(5 * time.Second): // Optional timeout
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process signal reading request")
			return
		}
	case "POST", "PUT":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			fmt.Println("Error parsing media type:", err)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("error reading service discovery request body: %v", err)
			return
		}
		serviceReq, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the service discovery request %v\n", err)
			return
		}

		speed, ok := serviceReq.(*forms.SignalA_v1a)
		if !ok {
			log.Println("problem unpacking the temperature signal form")
			return
		}
		// Create a struct to send on a channel to handle the request
		readRecord := STray{
			Action: "write",
			Sample: speed,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}

		// Send request to add a record to the unit asset
		ua.requests <- readRecord

		// Use a select statement to wait for responses on either the Result or Error channel
		select {
		case err := <-readRecord.Error:
			if err != nil {
				log.Printf("Error retrieving service records: %v", err)
				http.Error(w, "Error retrieving service records", http.StatusInternalServerError)
				return
			}
		case servvicesList := <-readRecord.Result:
			fmt.Println(servvicesList)
			var slForm forms.ServiceRecordList_v1
			slForm.NewForm()
			slForm.List = servvicesList
			updatedRecordBytes, err := usecases.Pack(&slForm, mediaType)
			if err != nil {
				log.Printf("error confirming new service: %s", err)
				http.Error(w, "Error registering service", http.StatusInternalServerError)
			}
			w.Header().Set("Content-Type", mediaType)
			w.WriteHeader(http.StatusOK)
			_, err = w.Write([]byte(updatedRecordBytes))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case <-time.After(5 * time.Second): // Optional timeout
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Failure to process service discovery request")
			return
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
