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

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "controls electrical heating plugs based on temperature readings from meteorologue",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30196, "http": 20196, "coap": 0},
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
	// Enrollment is started before the assets are built, which is the opposite
	// of every other system here and deliberate.
	//
	// This system is the only one whose construction blocks on the network:
	// newResources loops until a heater plug answers, because it creates one
	// unit asset per discovered heater and cannot know its own shape until it
	// has looked. That was harmless while the orchestrator was reached over
	// plain HTTP. Once a cloud adopts authorization the orchestrator must be
	// reached over HTTPS — the subject is the Common Name of a verified client
	// certificate, and a service quest arriving over HTTP carries no subject at
	// all — and the certificate that connection needs is the one installed by
	// RequestCertificate. Built in the old order, discovery waits for a
	// certificate that is only requested after discovery returns, and the
	// system never starts: it holds no port, registers nothing, and reports
	// only "certificate signed by unknown authority" every fifteen seconds.
	//
	// RequestCertificate reads nothing from sys.UAssets and returns
	// immediately, enrolling in a goroutine, so moving it earlier costs
	// nothing. The discovery loop's own retry is what closes the gap between
	// the two.
	usecases.RequestCertificate(&sys)

	// Forward shutdown signals to the context immediately so that Ctrl+C
	// unblocks the discovery retry loop inside newResources.
	assets, cleanup := newResources(uac, &sys)
	defer cleanup()
	for _, ua := range assets {
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Ctx.Done()
	fmt.Println("\nshutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}

// serving dispatches HTTP requests to the correct handler for the given service path.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "setpoint":
		t.setpt(w, r)
	case "deviation":
		t.diff(w, r)
	case "jitter":
		t.variations(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}
