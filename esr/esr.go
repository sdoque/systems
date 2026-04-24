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
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	sys := components.NewSystem("serviceregistrar", ctx)

	// Instantiate the Capsule
	sys.Husk = &components.Husk{
		Description: "is an Arrowhead mandatory core system that keeps track of the currently available services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}, "LocalCloud": {"AlphaCloud"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20102, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/esr",
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

	// Generate PKI keys and CSR to obtain a authentication certificate from the CA
	usecases.RequestCertificate(&sys)

	// Register the (system) and its services
	usecases.RegisterServices(&sys)

	// start the http handler and server
	go usecases.SetoutServers(&sys)

	// wait for shutdown signal, and gracefully close properly goroutines with context
	<-sys.Sigs
	fmt.Println("\nShutting down system", sys.Name)
	cancel()
	time.Sleep(3 * time.Second)
}

// ---------------------------------------------------------------------------- end of main()

// serving dispatches an incoming HTTP request to the appropriate handler.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "register":
		t.updateDB(w, r)
	case "query":
		t.queryDB(w, r)
	case "unregister":
		t.cleanDB(w, r)
	case "status":
		t.roleStatus(w, r)
	case "syslist":
		t.systemList(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// renderListItems builds the sorted <li> HTML fragment sent to SSE subscribers.
func renderListItems(servicesList []forms.ServiceRecord_v1) string {
	sort.Slice(servicesList, func(i, j int) bool {
		return servicesList[i].Id < servicesList[j].Id
	})
	var sb strings.Builder
	for _, servRec := range servicesList {
		metaservice := ""
		for key, values := range servRec.Details {
			metaservice += key + ": " + fmt.Sprintf("%v", values) + " "
		}
		hyperlink := "http://" + servRec.IPAddresses[0] + ":" + strconv.Itoa(int(servRec.ProtoPort["http"])) + "/" + servRec.SystemName + "/" + servRec.SubPath
		parts := strings.Split(servRec.SubPath, "/")
		uaName := parts[0]
		sb.WriteString("<li><p>Service ID: " + strconv.Itoa(int(servRec.Id)) +
			" with definition <b><a href=\"" + hyperlink + "\">" + servRec.ServiceDefinition + "</b></a>" +
			" from the <b>" + servRec.SystemName + "/" + uaName + "</b>" +
			" with details " + metaservice +
			" will expire at: " + servRec.EndOfValidity + "</p></li>")
	}
	return sb.String()
}

// peersList provides a list of the other service registrars in the local cloud.
func peersList(sys *components.System) (peers []*components.CoreSystem, err error) {
	for _, cs := range sys.Husk.CoreS {
		if cs.Name != "serviceregistrar" {
			continue
		}
		u, err := url.Parse(cs.Url)
		if err != nil {
			return peers, err
		}
		uPort, err := strconv.Atoi(u.Port())
		if err != nil {
			fmt.Println(err)
		}
		if (u.Hostname() == sys.Husk.Host.IPAddresses[0] || u.Hostname() == "localhost") && uPort == sys.Husk.ProtoPort[u.Scheme] {
			continue
		}
		peers = append(peers, cs)
	}
	return peers, nil
}
