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
	"log"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be canceled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("hobbyist", ctx)

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	// instantiate the husk
	sys.Husk = &components.Husk{
		Description: "reads the temperature from 1-wire sensors",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30150, "http": 20157, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/hobbyist",
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
		// One unit asset per locomotive the Central Station knows, which is the
		// whole set — including the ones still in their boxes. Placing an engine
		// on the track changes whether its services answer, not whether they
		// exist.
		assets, cleanup := newResources(uac, &sys, locoSource(uac), openBus(uac))
		defer cleanup()
		for _, ua := range assets {
			sys.UAssets[ua.GetName()] = ua
		}
	}

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the requests handlers and servers
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second) // allow the go routines to be executed, which might take more time than the main routine to end
}

// locoSource decides where the locomotive list comes from. Today that is a file
// exported from the Central Station; when the config-data channel is verified it
// becomes the station itself, and nothing above this function changes.
func locoSource(uac usecases.ConfigurableAsset) LocoSource {
	path := "locomotives.json"
	if values := uac.Details["LocomotiveList"]; len(values) > 0 {
		path = values[0]
	}
	return LocoFile{Path: path}
}

// openBus connects to the layout. Until the SocketCAN transport is written —
// following the build-tagged pattern busdriver uses — commands are refused
// rather than silently dropped.
func openBus(_ usecases.ConfigurableAsset) Bus {
	return silentBus{}
}
