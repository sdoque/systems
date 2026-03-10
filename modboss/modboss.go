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

// This is the main function for the Modbus master (modboss) system
func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("modboss", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "interacts with an Modbus slave or server",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20171, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/modboss",
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
		log.Fatalf("Configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		uas, cleanup := newResource(uac, &sys)
		defer cleanup()
		for _, ua := range uas {
			var asset components.UnitAsset = ua
			sys.UAssets[ua.GetName()] = &asset
		}
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the requests handlers and servers
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

	case "access":
		ua.access(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

func (ua *UnitAsset) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		valueForm := ua.read()
		usecases.HTTPProcessGetRequest(w, r, valueForm)
	case "PUT":
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
		newState, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("error extracting the service discovery request %v\n", err)
			return
		}
		// Perform a type assertion to convert the received form to the expected type
		switch ns := newState.(type) {
		case *forms.SignalA_v1a:
			// v is of type *forms.SignalA_v1a
			fmt.Printf("Received analog signal: %.2f %s\n", ns.Value, ns.Unit)
			ua.write(ns.Value)
		case *forms.SignalB_v1a:
			// v is of type *forms.SignalB_v1a
			fmt.Printf("Received digital signal: %v\n", ns.Value)
			ua.write(ns.Value)
		default:
			log.Printf("Problem unpacking the new value for %s: unsupported form type %T", ua.Name, ns)
			http.Error(w, "Unsupported form type", http.StatusBadRequest)
			return
		}

	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
