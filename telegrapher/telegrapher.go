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
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("telegrapher", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: " subscribes and publishes to an MQTT broker",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20172, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/telegrapher",
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
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
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

// Serving handles the resources services. NOTE: it exepcts those names from the request URL path
func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	svrs := ua.GetServices()
	if svrs[servicePath] != nil {
		ua.access(w, r, servicePath)
	} else {
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

func (ua *UnitAsset) access(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch r.Method {
	case "GET":
		msg := ua.Message
		if len(msg) > 0 {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")
			w.Write(msg)
		} else {
			http.Error(w, "The subscribed topic is not being published", http.StatusBadRequest)
		}
	case "PUT":
		// data, err := io.ReadAll(r.Body)
		// if err != nil {
		// 	http.Error(w, "Failed to read request body", http.StatusBadRequest)
		// 	return
		// }
		// defer r.Body.Close()

		// if err := ua.publishRaw(data); err != nil {
		log.Printf("MQTT client is connected: %v", ua.mClient.IsConnected())

		if err := ua.publishRaw([]byte(`{"test":123}`)); err != nil {
			log.Printf("Failed to publish: %v", err)
			http.Error(w, "MQTT publish failed", http.StatusInternalServerError)
			return
		}
		log.Printf("MQTT client is connected: %v", ua.mClient.IsConnected())

		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
