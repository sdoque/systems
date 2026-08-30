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
	"html"
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

	// Watch for SIGINT immediately so Ctrl+C interrupts blocking startup steps.
	usecases.WatchShutdown(&sys, cancel)

	// Instantiate the Capsule
	sys.Husk = &components.Husk{
		Description: "is an Arrowhead mandatory core system that keeps track of the currently available services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}, "LocalCloud": {"AlphaCloud"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 30102, "http": 20102, "coap": 0},
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
	<-sys.Ctx.Done()
	log.Println("shutting down system", sys.Name)
	time.Sleep(3 * time.Second)
}

// ---------------------------------------------------------------------------- end of main()

// serving dispatches an incoming HTTP request to the appropriate handler.
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case registryPath:
		t.registryDB(w, r)
	case "query":
		t.queryDB(w, r)
	case "status":
		t.roleStatus(w, r)
	case systemListPath:
		t.systemList(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// renderListItems builds the sorted <li> HTML fragment sent to SSE subscribers.
//
// Each service shows one endpoint per configured protocol. Plain-HTTP links
// are clickable for browser-driven inspection by deployment technicians.
// HTTPS endpoints are rendered as labels rather than active links because the
// framework's TLS server requires mTLS (`tls.RequireAndVerifyClientCert`),
// which a regular browser cannot satisfy without an installed client cert
// signed by this cloud's CA. Programmatic peers using `http.DefaultClient`
// configured by `installTLSConfig` reach those endpoints; humans cannot.
func renderListItems(servicesList []forms.ServiceRecord_v1) string {
	sort.Slice(servicesList, func(i, j int) bool {
		return servicesList[i].Id < servicesList[j].Id
	})

	// Protocol render order: HTTP first because it is the browser-clickable
	// link; HTTPS afterwards as an mTLS-labeled endpoint.
	protoOrder := []string{"http", "https", "coap"}

	var sb strings.Builder
	for _, servRec := range servicesList {
		// Escaped, because every one of these came off the wire from whichever
		// system registered it.
		//
		// A QUDT unit is written <http://qudt.org/vocab/unit/DEG_C>, and a
		// browser reads that as an unknown tag and renders nothing — so a page
		// showing "Unit: []" was not missing the unit, it was swallowing it, and
		// QuantityKind was invisible on every service in the cloud because it is
		// always an identifier of that shape. The registry had the value all
		// along.
		//
		// The same escaping is what stops a system registering a detail
		// containing a script from running it in the browser of the technician
		// who opens this page.
		var details strings.Builder
		for key, values := range servRec.Details {
			shown := make([]string, 0, len(values))
			for _, value := range values {
				shown = append(shown, html.EscapeString(value))
			}
			fmt.Fprintf(&details, "%s: [%s] ", html.EscapeString(key), strings.Join(shown, " "))
		}

		parts := strings.Split(servRec.SubPath, "/")
		uaName := parts[0]

		var endpoints strings.Builder
		for _, proto := range protoOrder {
			port, ok := servRec.ProtoPort[proto]
			if !ok || port == 0 {
				continue
			}
			url := proto + "://" + servRec.IPAddresses[0] + ":" + strconv.Itoa(port) +
				"/" + servRec.SystemName + "/" + servRec.SubPath
			// The address is built from what the provider registered, so it is
			// escaped like anything else that arrived from elsewhere — an
			// attribute is a place a quotation mark ends something.
			safe := html.EscapeString(url)
			if proto == "http" {
				// Browser-clickable plain-HTTP link.
				fmt.Fprintf(&endpoints, ` <a href="%s">%s</a>`, safe, html.EscapeString(proto))
			} else {
				// mTLS endpoint (HTTPS) or non-HTTP protocols (CoAP):
				// shown as a labeled span so the URL is visible but not
				// clickable into a regular browser session.
				fmt.Fprintf(&endpoints, ` <span title="requires mTLS">[%s: %s]</span>`,
					html.EscapeString(proto), safe)
			}
		}

		fmt.Fprintf(&sb,
			"<li><p>Service ID: %d with definition <b>%s</b> from the <b>%s/%s</b>"+
				" — endpoints:%s — with details %s — will expire at: %s</p></li>",
			servRec.Id,
			html.EscapeString(servRec.ServiceDefinition),
			html.EscapeString(servRec.SystemName), html.EscapeString(uaName),
			endpoints.String(),
			details.String(),
			html.EscapeString(servRec.EndOfValidity),
		)
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
