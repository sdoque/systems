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
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("tracker", ctx)

	sys.Husk = &components.Husk{
		Description: "tracks pen holder orders in a SQLite database",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"https": 0, "http": 20191, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/tracker",
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
	case "order":
		t.orderHandler(w, r)
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}

// orderHandler handles GET (retrieve by ?id=N), POST (new order), and PUT (update order).
func (t *Traits) orderHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {

	case http.MethodGet:
		idStr := r.URL.Query().Get("id")
		email := r.URL.Query().Get("email")
		if idStr == "" || email == "" {
			http.Error(w, "missing query parameters: id and email are both required", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "id must be an integer", http.StatusBadRequest)
			return
		}
		order, err := GetOrderByIDAndEmail(t.db, id, email)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		order.NewForm()
		usecases.HTTPProcessGetRequest(w, r, order)

	case http.MethodPost, http.MethodPut:
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			http.Error(w, "could not parse Content-Type: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "reading request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		unpacked, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			http.Error(w, "unpacking request: "+err.Error(), http.StatusBadRequest)
			return
		}
		record, ok := unpacked.(*PenHolderOrder_v1)
		if !ok {
			http.Error(w, "expected PenHolderOrder_v1 body", http.StatusBadRequest)
			return
		}

		if record.OrderNumber <= 0 {
			// New order: insert and forward to downstream system.
			oN, err := InsertOrder(t.db, record)
			if err != nil {
				http.Error(w, "inserting order: "+err.Error(), http.StatusInternalServerError)
				return
			}
			record.OrderNumber = oN
			log.Printf("tracker: new order %d filed\n", oN)

			// Forward the new order to the addorder cervice (e.g. a production TSP).
			if t.owner != nil {
				packed, err := usecases.Pack(record, "application/json")
				if err == nil {
					if _, err := usecases.SetState(t.cervices["addorder"], t.owner, packed); err != nil {
						log.Printf("tracker: could not forward order %d to addorder service: %v\n", oN, err)
					}
				}
			}
		} else {
			// Existing order: update in place.
			if err := UpdateOrder(t.db, record); err != nil {
				http.Error(w, "updating order: "+err.Error(), http.StatusInternalServerError)
				return
			}
			log.Printf("tracker: order %d updated\n", record.OrderNumber)
		}

		confirmed, err := usecases.Pack(record, mediaType)
		if err != nil {
			http.Error(w, "marshalling response: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		w.WriteHeader(http.StatusOK)
		w.Write(confirmed) //nolint:errcheck

	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}
