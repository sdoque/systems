/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("ethermostat", ctx)

	sys.Husk = &components.Husk{
		Description: "controls electrical heating plugs based on temperature readings from meteorologue",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20196, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/ethermostat",
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

	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset)

	if len(rawResources) == 0 {
		log.Fatal("ethermostat: no unit asset configuration found in systemconfig.json")
	}

	var uac usecases.ConfigurableAsset
	if err := json.Unmarshal(rawResources[0], &uac); err != nil {
		log.Fatalf("resource configuration error: %v\n", err)
	}
	assets, cleanup := newResources(uac, &sys)
	defer cleanup()
	for _, ua := range assets {
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving dispatches HTTP requests to the correct handler for the given service path.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "setpoint":
		t.setpt(w, r)
	case "thermalerror":
		t.diff(w, r)
	case "jitter":
		t.variations(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// setpt handles GET (read setpoint) and PUT (update setpoint) requests.
func (t *Traits) setpt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			log.Printf("ethermostat %s: setpoint PUT error: %v\n", t.name, err)
			return
		}
		t.setSetPoint(sig)
		confirmed := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &confirmed)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// diff handles GET requests for the current thermal error.
func (t *Traits) diff(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getError()
		usecases.HTTPProcessGetRequest(w, r, &f)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// variations handles GET requests for the control loop jitter.
func (t *Traits) variations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getJitter()
		usecases.HTTPProcessGetRequest(w, r, &f)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}
