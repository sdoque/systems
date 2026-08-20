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
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

// The assessor says what can go wrong with the local cloud, and what would
// follow if it did.
//
// It is an FMEA that maintains itself. The half a spreadsheet makes people do
// by hand — which component feeds which, what stops working when this stops
// answering, what is left unwatched — is a graph traversal, and the graph is
// already there. What a graph cannot supply is how much any of it matters, so
// severity, occurrence and detection come from a file the owner writes, keyed
// on classes of consequence rather than on rows. The cloud changes and the rows
// regenerate; the owner's values change and one edit rescores everything.
//
// It reads GraphDB rather than each system's /kgraph, because the store holds
// the reasoner's entailments and, where the plant design and lifecycle views
// have been loaded, the P&ID tags and serial numbers that turn "a sensor" into
// a serial-numbered device with a maintenance history.
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

	sys := components.NewSystem("assessor", ctx)
	usecases.WatchShutdown(&sys, cancel)

	sys.Husk = &components.Husk{
		Description: "derives the local cloud's failure modes and effects from its knowledge graph",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30108, "http": 20108, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/assessor",
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
		log.Fatalf("Configuration error: %v\n", err)
	}
	sys.UAssets = make(map[string]*components.UnitAsset)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			log.Fatalf("Resource configuration error: %+v\n", err)
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

// serving dispatches a request to the service that answers it.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "fmea":
		t.fmea(w, r)
	case "scope":
		t.scope(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// newEmptyValuationError explains the one startup failure an operator will
// actually hit: the system deployed without its judgment file.
func newEmptyValuationError() error {
	return &missingValuation{}
}

type missingValuation struct{}

func (m *missingValuation) Error() string {
	return "no " + ValuationFileName + " in this directory. The graph supplies the failure " +
		"modes and their effects; this file supplies how much they matter, and without it " +
		"every row would be unscored. See the README for a starting point."
}
