/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

// sailor is an mbaigo system that reads NMEA 2000 signals from a vessel's
// CAN bus (PiCAN-M HAT on Raspberry Pi) and exposes each signal as a typed
// Arrowhead service.
//
// Each configured signal becomes one unit asset reachable at:
//
//	GET /sailor/<SignalName>/access  →  SignalA_v1a JSON
//
// See systemconfig.json for the list of monitored signals and README.md for
// hardware setup and how to add new PGNs.

package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("sailor", ctx)

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)
	sys.Husk = &components.Husk{
		Description: "NMEA 2000 gateway — exposes vessel signals as Arrowhead services",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20194, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/sailor",
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
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		uas, cleanup := newResource(uac, &sys)
		defer cleanup()
		for _, ua := range uas {
			sys.UAssets[ua.GetName()] = ua
		}
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// serving dispatches incoming HTTP requests by service sub-path.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "access":
		t.access(w, r)
	default:
		http.Error(w, "Invalid service path", http.StatusBadRequest)
	}
}
