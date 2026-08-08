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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("authorizer", ctx)

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "is the Arrowhead core system that decides which providers a consumer may use",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30104, "http": 20104, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/authorizer",
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
	sys.UAssets[assetTemplate.GetName()] = assetTemplate

	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		log.Fatalf("configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset) // clear the template slot
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("resource configuration error: %+v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
		sys.UAssets[ua.GetName()] = ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)

	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(2 * time.Second)
}
