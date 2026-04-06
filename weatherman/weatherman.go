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
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// instantiate the System
	sys := components.NewSystem("weatherman", ctx)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "exposes a Davis Vantage Pro2 weather station as Arrowhead services via USB serial",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20184, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/weatherman",
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

	// instantiate the template unit asset (used only for initial config generation)
	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	// configure the system (reads systemconfig.json; generates it from template if absent)
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear template

	if len(rawResources) == 0 {
		log.Fatal("weatherman: no unit asset configuration found in systemconfig.json")
	}

	// parse the station config and discover assets from the first LOOP packet
	var uac usecases.ConfigurableAsset
	if err := json.Unmarshal(rawResources[0], &uac); err != nil {
		log.Fatalf("resource configuration error: %v\n", err)
	}
	assets, cleanup := newResources(uac, &sys)
	defer cleanup()
	for _, ua := range assets {
		sys.UAssets[ua.GetName()] = ua
	}

	// generate PKI keys and obtain a certificate from the CA
	usecases.RequestCertificate(&sys)

	// register the system and its services
	usecases.RegisterServices(&sys)

	// start the HTTP request handler and server
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal
	<-sys.Sigs
	fmt.Println("\nshutting down system", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

// serving handles incoming HTTP requests for a module unit asset.
// All services are read-only (GET); the value is looked up from the shared cache.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
		return
	}

	m := t.cache.get(t.assetName, servicePath)
	if m == nil {
		http.Error(w, "measurement not yet available", http.StatusServiceUnavailable)
		return
	}

	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = m.Value
	f.Unit = serviceUnit(servicePath)
	f.Timestamp = m.Timestamp
	usecases.HTTPProcessGetRequest(w, r, &f)
}

// serviceUnit returns the physical unit for a given service subpath.
func serviceUnit(s string) string {
	switch s {
	case "barometer":
		return "mbar"
	case "inside_temperature", "temperature":
		return "Celsius"
	case "inside_humidity", "humidity":
		return "%"
	case "wind_speed":
		return "km/h"
	case "wind_angle":
		return "°"
	case "rain_rate":
		return "mm/h"
	case "rain_24h":
		return "mm"
	case "uv":
		return "UV index"
	case "solar_radiation":
		return "W/m²"
	default:
		return ""
	}
}
