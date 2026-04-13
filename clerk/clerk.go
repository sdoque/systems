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
 *   Franziska Sievert - initial implementation
 *   Jan A. van Deventer, Luleå - modernized for current mbaigo
 ***************************************************************************SDG*/

package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("clerk", ctx)

	sys.Husk = &components.Husk{
		Description: "browser-based order entry form for pen holder orders",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20190, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/clerk",
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
			log.Fatalf("resource configuration error: %v\n", err)
		}
		ua, cleanup := newResource(uac, &sys)
		defer cleanup()
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
	case "orders":
		t.ordersHandler(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// ordersHandler routes GET (page or lookup) and POST (new order).
func (t *Traits) ordersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {

	case http.MethodGet:
		id := r.URL.Query().Get("id")
		email := r.URL.Query().Get("email")
		if id != "" && email != "" {
			t.lookupFromTracker(w, id, email)
		} else if id != "" || email != "" {
			http.Error(w, "both id and email are required for order lookup", http.StatusBadRequest)
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, orderPage)
		}

	case http.MethodPost:
		t.submitOrder(w, r)

	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// submitOrder unpacks the incoming JSON order, forwards it to the tracker, and
// returns the confirmed record (including the assigned order number) as JSON.
func (t *Traits) submitOrder(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	unpacked, err := usecases.Unpack(body, "application/json")
	if err != nil {
		http.Error(w, "unpacking order: "+err.Error(), http.StatusBadRequest)
		return
	}
	order, ok := unpacked.(*PenHolderOrder_v1)
	if !ok {
		http.Error(w, "expected PenHolderOrder_v1 body", http.StatusBadRequest)
		return
	}

	// Validate dimensions server-side as well.
	if order.Height <= 0 || order.Height > 21 {
		http.Error(w, "height must be between 0 and 21 mm", http.StatusBadRequest)
		return
	}
	if order.Depth < 0 || order.Depth > order.Height {
		http.Error(w, "depth must be ≥ 0 and not exceed height", http.StatusBadRequest)
		return
	}

	packed, err := usecases.Pack(order, "application/json")
	if err != nil {
		http.Error(w, "packing order: "+err.Error(), http.StatusInternalServerError)
		return
	}

	f, err := usecases.SetState(t.cervices["order"], t.owner, packed)
	if err != nil {
		log.Printf("clerk: could not reach tracker: %v\n", err)
		http.Error(w, "tracker unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	confirmed, ok := f.(*PenHolderOrder_v1)
	if !ok {
		http.Error(w, "unexpected response from tracker", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(confirmed) //nolint:errcheck
}

// lookupFromTracker discovers the tracker's order service URL, appends ?id=N&email=E,
// and proxies the response back to the browser.
func (t *Traits) lookupFromTracker(w http.ResponseWriter, id, email string) {
	cer := t.cervices["order"]

	if len(cer.Nodes) == 0 {
		if err := usecases.Search4Services(cer, t.owner); err != nil {
			http.Error(w, "could not discover order service: "+err.Error(), http.StatusBadGateway)
			return
		}
	}

	var baseURL string
	for _, nodes := range cer.Nodes {
		if len(nodes) > 0 {
			baseURL = nodes[0].URL
			break
		}
	}
	if baseURL == "" {
		http.Error(w, "order service not found", http.StatusBadGateway)
		return
	}

	targetURL := baseURL + "?id=" + url.QueryEscape(id) + "&email=" + url.QueryEscape(email)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(targetURL)
	if err != nil {
		cer.Nodes = make(map[string][]components.NodeInfo)
		http.Error(w, "tracker error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "reading tracker response: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}
