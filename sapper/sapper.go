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
	sys := components.NewSystem("sapper", ctx)

	// Instantiate the husk
	sys.Husk = &components.Husk{
		Description: "simulates a SAP maintenance order system, exposing order creation and status as Arrowhead services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20191, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/sapper",
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
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the unit asset map (from the template)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	// Generate PKI keys and CSR to obtain an authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the system and its services
	usecases.RegisterServices(&sys)

	// Start the HTTP handler and server
	go usecases.SetoutServers(&sys)

	// Wait for shutdown signal
	<-sys.Sigs
	fmt.Println("\nshuting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving dispatches incoming HTTP requests to the appropriate handler.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "orders":
		switch r.Method {
		case http.MethodPost:
			t.createOrderHandler(w, r)
		case http.MethodGet:
			t.queryOrderHandler(w, r)
		default:
			http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "Invalid service path [do not modify subpath in configuration file]", http.StatusBadRequest)
	}
}
