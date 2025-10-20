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
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// This is the main function for the OPC UA Client system
func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("opcuac", ctx)

	// Instantiate the husk
	sys.Husk = &components.Husk{
		Description: "interacts with an OPC UA server",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20170, "coap": 0},
		InfoLink:    "https://github.com/sdoque/mbaigo/tree/master/uaclient",
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
	fmt.Println("\nshuting down system", sys.Name)
	cancel()                    // cancel the context, signaling the goroutines to stop
	time.Sleep(3 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// Serving handles the resources services. NOTE: it expects those names from the request URL path
func (node *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "browse":
		node.browse(w, r)
	case "access":
		node.access(w, r)

	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

func (node *UnitAsset) browse(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		node.browseNode(w)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//

func (node *UnitAsset) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		vauleForm := node.read()
		usecases.HTTPProcessGetRequest(w, r, &vauleForm)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
