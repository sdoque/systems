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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("cloudmodel", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "assembles the SysML v2 BDD/IBD models of all systems in a local cloud",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20106, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/cloudmodel",
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
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

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
		sys.UAssets[ua.GetName()] = ua
	}

	// Generate PKI keys and CSR to obtain an authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the system and its services
	usecases.RegisterServices(&sys)

	// start the request handlers and servers
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving handles the resource's services. NOTE: it expects those names from the request URL path.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "cloudmodel":
		t.aggregate(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// aggregate handles GET requests to generate the merged cloud SysML v2 model.
func (t *Traits) aggregate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		t.assembleModel(w)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}
