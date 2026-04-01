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
	"io"
	"log"
	"mime"
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

// updateDB adds a new service record or extends its registration life.
func (t *Traits) updateDB(w http.ResponseWriter, r *http.Request) {
	if !t.leading {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("Service Unavailable")); err != nil {
			log.Printf("error occurred while writing to responsewriter: %v", err)
		}
		return
	}
	switch r.Method {
	case "POST", "PUT":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Println("Error parsing media type:", err)
			http.Error(w, "Error parsing media type", http.StatusBadRequest)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("Error reading registration request body: %v", err)
			http.Error(w, "Error reading registration request body", http.StatusBadRequest)
			return
		}
		record, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("Error extracting the registration request %v\n", err)
			http.Error(w, "Error extracting the registration request", http.StatusBadRequest)
			return
		}

		addRecord := ServiceRegistryRequest{
			Action: "add",
			Record: record,
			Error:  make(chan error),
		}
		t.requests <- addRecord
		err = <-addRecord.Error
		if err != nil {
			log.Printf("Error adding the new service: %v", err)
			http.Error(w, "Error registering service", http.StatusInternalServerError)
			return
		}
		updatedRecordBytes, err := usecases.Pack(record, mediaType)
		if err != nil {
			log.Printf("Error confirming new service: %s", err)
			http.Error(w, "Error registering service", http.StatusInternalServerError)
		}
		w.Header().Set("Content-Type", mediaType)
		w.WriteHeader(http.StatusOK)
		if _, err = w.Write([]byte(updatedRecordBytes)); err != nil {
			log.Printf("Error occurred while writing to response: %v", err)
		}

	default:
		fmt.Fprintf(w, "unsupported http request method")
	}
}

// queryDB looks for service records in the service registry.
func (t *Traits) queryDB(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.sseHandler(w, r)
			return
		}
		// Regular browser request: return a page that opens an EventSource connection.
		page := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Service Registry</title></head><body>` +
			`<p>The local cloud's currently available services are:</p>` +
			`<ul id="services"><li>Loading&#x2026;</li></ul>` +
			`<script>` +
			`var es=new EventSource(window.location.href);` +
			`es.onmessage=function(e){document.getElementById('services').innerHTML=e.data;};` +
			`es.onerror=function(){document.getElementById('services').innerHTML='<li>Connection lost \u2013 reconnecting\u2026</li>';};` +
			`</script></body></html>`
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte(page)); err != nil {
			log.Printf("Error writing query page: %v", err)
		}

	case "POST":
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Println("Error parsing media type:", err)
			http.Error(w, "Error parsing media type", http.StatusBadRequest)
			return
		}

		defer r.Body.Close()
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("Error reading service discovery request body: %v", err)
			http.Error(w, "Error reading service discovery request body", http.StatusBadRequest)
			return
		}
		record, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("Error extracting the service discovery request %v\n", err)
			http.Error(w, "Error extracting the service discovery request", http.StatusBadRequest)
			return
		}

		readRecord := ServiceRegistryRequest{
			Action: "read",
			Record: record,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
		t.requests <- readRecord

		select {
		case err := <-readRecord.Error:
			if err != nil {
				log.Printf("Error retrieving service records: %v", err)
				http.Error(w, "Error retrieving service records", http.StatusInternalServerError)
				return
			}
		case servicesList := <-readRecord.Result:
			var slForm forms.ServiceRecordList_v1
			slForm.NewForm()
			slForm.List = servicesList
			updatedRecordBytes, err := usecases.Pack(&slForm, mediaType)
			if err != nil {
				log.Printf("error confirming new service: %s", err)
				http.Error(w, "Error registering service", http.StatusInternalServerError)
			}
			w.Header().Set("Content-Type", mediaType)
			w.WriteHeader(http.StatusOK)
			if _, err = w.Write([]byte(updatedRecordBytes)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case <-time.After(5 * time.Second):
			log.Println("Failure to process service discovery request")
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			return
		}

	default:
		http.Error(w, "Unsupported HTTP request method", http.StatusMethodNotAllowed)
	}
}

// cleanDB deletes service records upon request (e.g., when a system shuts down).
func (t *Traits) cleanDB(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "DELETE":
		parts := strings.Split(r.URL.Path, "/")
		idStr := parts[len(parts)-1]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid record ID", http.StatusBadRequest)
			return
		}
		addRecord := ServiceRegistryRequest{
			Action: "delete",
			Id:     int64(id),
			Error:  make(chan error),
		}
		t.requests <- addRecord
		err = <-addRecord.Error
		if err != nil {
			log.Printf("Error deleting the service with id: %d, %s\n", id, err)
			http.Error(w, "Error deleting service", http.StatusInternalServerError)
			return
		}
	default:
		fmt.Fprintf(w, "unsupported http request method")
	}
}

// roleStatus returns the current role of the service registrar (leading or standby).
func (t *Traits) roleStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if t.leading {
			fmt.Fprintf(w, "lead Service Registrar since %s", t.leadingSince)
			return
		}
		if t.leadingRegistrar != nil {
			http.Error(w, fmt.Sprintf("On standby, leading registrar is %s", t.leadingRegistrar.Url), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("Service Unavailable")); err != nil {
			log.Printf("Error occurred while writing to responsewriter: %v", err)
		}
	default:
		fmt.Fprintf(w, "Unsupported http request method")
	}
}

// startRole repeatedly checks which service registrar in the local cloud is the leader.
func (t *Traits) startRole(sys *components.System) {
	peersList, err := peersList(sys)
	if err != nil {
		panic(err)
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		for {
			standby := false
		foundLead:
			for _, cSys := range peersList {
				resp, err := http.Get(cSys.Url + "/status")
				if err != nil {
					break
				}
				defer resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusOK:
					standby = true
					t.leading = false
					t.leadingSince = time.Time{}
					t.leadingRegistrar = cSys
					break foundLead
				case http.StatusServiceUnavailable:
					// Service unavailable
				default:
					log.Printf("Received unexpected status code: %d\n", resp.StatusCode)
				}
			}
			if !standby && !t.leading {
				t.leading = true
				t.leadingSince = time.Now()
				t.leadingRegistrar = nil
				log.Printf("Taking the service registry lead at %s\n", t.leadingSince)
			}
			<-ticker.C
		}
	}()
}

// systemList returns the list of unique systems registered in the local cloud.
func (t *Traits) systemList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		systemsList, err := getUniqueSystems(t)
		if err != nil {
			http.Error(w, fmt.Sprintf("System list error: %s", err), http.StatusInternalServerError)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, systemsList)
	default:
		http.Error(w, "Unsupported HTTP request method", http.StatusMethodNotAllowed)
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

// sseHandler keeps the connection open and pushes a fresh list whenever the registry changes.
func (t *Traits) sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Register this subscriber.
	ch := make(chan struct{}, 1)
	t.subMu.Lock()
	t.subSeq++
	id := t.subSeq
	t.subscribers[id] = ch
	t.subMu.Unlock()
	defer func() {
		t.subMu.Lock()
		delete(t.subscribers, id)
		t.subMu.Unlock()
	}()

	// sendSnapshot fetches the current registry and pushes one SSE event.
	sendSnapshot := func() {
		req := ServiceRegistryRequest{
			Action: "read",
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
		t.requests <- req
		select {
		case list := <-req.Result:
			fmt.Fprintf(w, "data: %s\n\n", renderListItems(list))
			flusher.Flush()
		case err := <-req.Error:
			log.Printf("SSE: error reading registry: %v", err)
		case <-time.After(5 * time.Second):
			log.Println("SSE: timeout reading registry")
		}
	}

	sendSnapshot() // send current state immediately on connect

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			sendSnapshot()
		}
	}
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
