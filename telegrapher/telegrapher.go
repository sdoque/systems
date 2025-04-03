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

	// instatiate the husk
	sys.Husk = &components.Husk{
		Description: " subcribes and publishes to an MQTT broker",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20172, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/telegrapher",
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate

	// Configure the system
	rawResources, servsTemp, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("Configuration error: %v\n", err)
	}

	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	//	Resources := make(map[string]*UnitAsset)
	for _, raw := range rawResources {
		var uac UnitAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
		}
		promUA, cleanup := newResource(uac, &sys, servsTemp)
		defer cleanup()
		for _, nua := range promUA {
			sys.UAssets[nua.GetName()] = &nua
		}
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
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configurration file]", http.StatusBadRequest)
	}
}

func (ua *UnitAsset) access(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch r.Method {
	case "GET":
		msg := messageList[ua.metatopic+"/"+servicePath]
		if msg != nil {
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")
			w.Write(msg)
		} else {
			http.Error(w, "The subscribed topic is not being published", http.StatusBadRequest)
		}
	case "PUT":
		// sig, err := usecases.HTTPProcessSetRequest(w, r)
		// if err != nil {
		// 	log.Println("Error with the setting request of the position ", err)
		// }
		// ua.setPosition(sig)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
