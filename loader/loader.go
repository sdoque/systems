/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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

// The loader drives the five motors of an articulated mini wheel loader over
// CAN and exposes each of them as a service.
//
// It is deliberately the hardware and nothing more: it knows node IDs, the
// controller's integer scale and how often the drives must be spoken to, and it
// knows nothing about wheel radius, wheelbase or the vehicle's geometry. Those
// belong to the driver system, which is what makes the same binary able to run
// a different vehicle by changing the driver's parameters rather than this code.
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

	sys := components.NewSystem("loader", ctx)
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "drives the motors of an articulated mini wheel loader over CAN",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30197, "http": 20197, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/loader",
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

func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "setpoint":
		t.setpointService(w, r)
	default:
		http.Error(w, "Invalid service path", http.StatusBadRequest)
	}
}
