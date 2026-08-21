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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// Define the types of requests the serviceRegistry manager can handle
type ServiceRegistryRequest struct {
	Action string
	Record forms.Form
	Id     int64
	Result chan []forms.ServiceRecord_v1
	Error  chan error
}

// -------------------------------------Define the unit asset

// Traits holds all asset-specific state for the service registrar.
// mu protects the fields it shares with concurrent goroutines.
type Traits struct {
	serviceRegistry map[int]forms.ServiceRecord_v1
	recCount        int64
	requests        chan ServiceRegistryRequest
	sched           *Scheduler
	// role is who leads the registry. Written by the election goroutine and read
	// by every request handler, so it is swapped as a whole rather than held as
	// three fields: read separately, /status could answer "leading since" the
	// zero time, or "on standby" naming a registrar the election had just
	// cleared. That endpoint is what GetRunningCoreSystemURL polls to decide
	// which registrar a whole cloud talks to.
	role        atomic.Pointer[registrarRole]
	mu          sync.Mutex
	subscribers map[int]*subscriber // SSE listeners, keyed by connection ID
	subMu       sync.Mutex
	subSeq      int
}

// registrarRole is one election outcome: this registrar leads since a moment, or
// it is on standby behind a peer.
type registrarRole struct {
	leading   bool
	since     time.Time
	registrar *components.CoreSystem
}

// leads reports whether this registrar is currently the lead, tolerating the
// startup window before the first election has run.
func (t *Traits) leads() bool {
	r := t.role.Load()
	return r != nil && r.leading
}

// subscriber is one open SSE connection.
//
// The channel is buffered and the registry never blocks on it: a slow consumer
// must not be able to stall a registration. What it may not do is silently lose
// an event, which a bare drop would — a subscriber that missed a registration
// would be wrong about the cloud until something else happened to wake it. So a
// drop sets resync instead, and the subscriber answers by re-reading everything
// rather than by trusting the events it did receive.
type subscriber struct {
	events chan forms.RegistryEvent_v1
	// resync says events were dropped and the stream no longer describes the
	// registry. Read and cleared by the subscriber, set by the registry.
	resync atomic.Bool
}

// subscriberBuffer is how many events a subscriber may fall behind before the
// registry gives up on describing the changes and tells it to re-read. Large
// enough that a whole cloud starting at once does not trip it — 37 systems of a
// few services each — and small enough to bound what one stalled connection
// costs in memory.
const subscriberBuffer = 256

// keepAlive is how often the registry writes a comment line to an idle stream.
//
// Without one, a subscriber cannot tell a cloud where nothing is happening from
// a connection that no longer exists. A stream is idle most of the time by
// design — registrations renew, and renewals are deliberately not events — so
// the silent case is the normal case, and a NAT idle timeout, a partition or a
// registrar killed without a FIN all leave the subscriber blocked on a read
// that will never return. It keeps its stale graph and logs nothing.
//
// A comment line rather than an event: SSE parsers ignore lines beginning with
// a colon, so this costs subscribers nothing to receive and nothing to handle.
const keepAlive = 20 * time.Second

// maxSubscribers bounds the open streams.
//
// Each holds a buffer of whole service records, and nothing stops a client in a
// reconnect loop — or anyone at all in a cloud with no authorizer — from opening
// them until the registrar runs out of memory. notify also walks every one of
// them under a lock on each registration, so the cost of an idle subscriber is
// paid by every system that registers.
const maxSubscribers = 64

//-------------------------------------Instantiate a unit asset template

// systemListPath is the sub-path the registrar lists and streams itself on. A
// constant because it is named in three places — the declaration, the backfill
// for configurations written before it existed, and the dispatch in esr.go — and
// a path that answers but is not declared behaves differently depending on
// whether the cloud has an authorizer.
const systemListPath = "syslist"

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	registerService := components.Service{
		Definition:  "register",
		SubPath:     "register",
		Details:     map[string][]string{"Forms": usecases.ServiceRegistrationFormsList(), "Methods": components.HTTPMethods("POST")},
		Description: "registers a service (POST) or updates its expiration time (PUT)",
	}
	queryService := components.Service{
		Definition:  "query",
		SubPath:     "query",
		Details:     map[string][]string{"Forms": usecases.ServQuestForms(), "Methods": components.HTTPMethods("GET", "POST")},
		Description: "retrieves all currently available services using a GET request [accessed via a browser by a deployment technician] or retrieves a specific set of services using a POST request with a payload [initiated by the Orchestrator]",
	}
	unregisterService := components.Service{
		Definition:  "unregister",
		SubPath:     "unregister",
		Details:     map[string][]string{"Forms": {"ID_only"}, "Methods": components.HTTPMethods("DELETE")},
		Description: "removes a record (DELETE) based on record ID",
	}
	statusService := components.Service{
		Definition:  "status",
		SubPath:     "status",
		Details:     map[string][]string{"Forms": {"none"}},
		Description: "reports (GET) the role of the Service Registrar as leading or on stand by",
	}

	// Declared like the other four, and for the same reason. A path that answers
	// without being declared is outside everything the cloud knows about itself:
	// the authorizer has no policy to apply to it, the Orchestrator cannot tell a
	// subscriber where it is, and it appears nowhere in the knowledge graph. It
	// answered at all only in a cloud whose registrar had no authorizer to ask —
	// where an undeclared path is let through — and returned 404 in one that did.
	systemListService := components.Service{
		Definition:  systemListPath,
		SubPath:     systemListPath,
		Details:     map[string][]string{"Forms": {"RegistryEvent_v1"}},
		Description: "lists the registered systems (GET), as a page for a deployment technician or, when the request asks for text/event-stream, as a subscription that reports each registration and deregistration as it happens",
	}

	return &components.UnitAsset{
		Name:    "registry",
		Mission: components.MissionCore,
		Details: map[string][]string{"Type": {"ephemeral"}, "Mobility": {components.MobilityMovable}},
		ServicesMap: components.Services{
			registerService.SubPath:   &registerService,
			queryService.SubPath:      &queryService,
			unregisterService.SubPath: &unregisterService,
			statusService.SubPath:     &statusService,
			systemListService.SubPath: &systemListService,
		},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cleaningScheduler := NewScheduler()

	t := &Traits{
		serviceRegistry: make(map[int]forms.ServiceRecord_v1),
		recCount:        1, // 0 is used for non-registered services
		sched:           cleaningScheduler,
		requests:        make(chan ServiceRegistryRequest),
		subscribers:     make(map[int]*subscriber),
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	// Added if the configuration does not already have it, because a template is
	// only ever written to a systemconfig.json that does not exist yet — it never
	// merges into one that does. Every registrar deployed before this service
	// existed has a configuration without it, and left to the file alone they
	// would answer 404 on a path the framework itself depends on.
	//
	// Safe to add rather than to overwrite: an operator who has configured
	// syslist keeps what they configured. This is the registrar describing how it
	// exposes itself, not an asset anyone is expected to opt into.
	if _, configured := ua.ServicesMap[systemListPath]; !configured {
		s := initTemplate().ServicesMap[systemListPath]
		ua.ServicesMap[systemListPath] = s
	}

	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	t.startRole(sys)
	go t.serviceRegistryHandler()

	return ua, func() {
		t.mu.Lock()
		close(t.requests)
		cleaningScheduler.Stop()
		t.mu.Unlock()
		log.Println("Closing the service registry database connection")
	}
}

//-------------------------------------Unit asset's data management methods

// serviceRegistryHandler manages all service registry operations via channels.
func (t *Traits) serviceRegistryHandler() {
	for request := range t.requests {
		now := time.Now()
		switch request.Action {
		case "add":
			rec, ok := request.Record.(*forms.ServiceRecord_v1)
			if !ok {
				fmt.Println("Problem unpacking the service registration request")
				request.Error <- fmt.Errorf("invalid record type")
				continue
			}
			t.mu.Lock()

			if _, exists := t.serviceRegistry[rec.Id]; !exists {
				rec.Id = 0
			}

			// A zero Id means the registry has never seen this service. Anything
			// else is the periodic re-registration that only extends the
			// validity window — the same service, still there, saying so. That
			// is not a change and subscribers are not told about it.
			newRegistration := rec.Id == 0

			if rec.Id == 0 {
				for {
					currentCount := atomic.LoadInt64(&t.recCount)
					_, exists := t.serviceRegistry[int(currentCount)]
					if !exists {
						atomic.StoreInt64(&t.recCount, currentCount)
						rec.Id = int(currentCount)
						break
					}
					atomic.AddInt64(&t.recCount, 1)
				}
				rec.Id = int(t.recCount)
				rec.Created = now.Format(time.RFC3339)
				rec.Updated = now.Format(time.RFC3339)
				rec.EndOfValidity = now.Add(time.Duration(rec.RegLife) * time.Second).Format(time.RFC3339)
				log.Printf("The new service %s from system %s has been registered\n", rec.ServiceDefinition, rec.SystemName)
			} else {
				dbRec := t.serviceRegistry[rec.Id]
				if dbRec.ServiceDefinition != rec.ServiceDefinition {
					request.Error <- errors.New("mismatch between definition received record and database record")
					t.mu.Unlock()
					continue
				}
				if dbRec.SubPath != rec.SubPath {
					request.Error <- errors.New("mismatch between path received record and database record")
					t.mu.Unlock()
					continue
				}
				recCreated, err := time.Parse(time.RFC3339, rec.Created)
				if err != nil {
					request.Error <- errors.New("time parsing problem with updated record")
					t.mu.Unlock()
					continue
				}
				dbCreated, err := time.Parse(time.RFC3339, dbRec.Created)
				if err != nil {
					request.Error <- errors.New("time parsing problem with archived record")
					t.mu.Unlock()
					continue
				}
				if !recCreated.Equal(dbCreated) {
					request.Error <- errors.New("mismatch between created received record and database record")
					t.mu.Unlock()
					continue
				}
				rec.EndOfValidity = now.Add(time.Duration(dbRec.RegLife) * time.Second).Format(time.RFC3339)
			}
			t.sched.AddTask(now.Add(time.Duration(rec.RegLife)*time.Second), func() { checkExpiration(t, rec.Id) }, rec.Id)
			t.serviceRegistry[rec.Id] = *rec
			request.Record = rec
			t.mu.Unlock()
			if newRegistration {
				t.notify(forms.RegistryRegistered, *rec)
			}
			request.Error <- nil

		case "read":
			if request.Record == nil {
				var result []forms.ServiceRecord_v1
				t.mu.Lock()
				for _, record := range t.serviceRegistry {
					result = append(result, record)
				}
				t.mu.Unlock()
				request.Result <- result
				continue
			}
			qform, ok := request.Record.(*forms.ServiceQuest_v1)
			if !ok {
				log.Println("Problem unpacking the service quest request")
				request.Error <- fmt.Errorf("invalid record type")
				continue
			}
			request.Result <- t.FilterRecords(*qform)

		case "delete":
			t.mu.Lock()
			t.sched.RemoveTask(int(request.Id))
			// Kept before the delete: the event carries the record as it last
			// stood, so a subscriber can act on what left without having held
			// its own copy of the registry.
			gone, existed := t.serviceRegistry[int(request.Id)]
			delete(t.serviceRegistry, int(request.Id))
			if _, exists := t.serviceRegistry[int(request.Id)]; !exists {
				log.Printf("The service with ID %d has been deleted.", request.Id)
			}
			t.mu.Unlock()
			if existed {
				t.notify(forms.RegistryDeregistered, gone)
			}
			request.Error <- nil
		}
	}
}

func compareDetails(reqDetails []string, availDetails []string) bool {
	for _, requiredValue := range reqDetails {
		if slices.Contains(availDetails, requiredValue) {
			return true
		}
	}
	return false
}

// FilterRecords returns the registered services a quest matches on: its service
// definition, its provider, and its details. Every criterion the quest states
// must hold; criteria it leaves empty do not narrow anything.
//
// An empty ServiceDefinition matches any definition, which is what lets the
// authorizer ask for everything one system provides in order to read that
// system's own attributes. A quest that states *neither* a definition nor a
// provider returns nothing rather than the whole registry: a request that
// narrows nothing is a malformed one, and answering it with the entire cloud
// would turn a typo into a disclosure.
func (t *Traits) FilterRecords(quest forms.ServiceQuest_v1) []forms.ServiceRecord_v1 {
	if quest.ServiceDefinition == "" && quest.ProviderName == "" {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	requiredDetails := quest.Details

	var matchingRecords []forms.ServiceRecord_v1
	for _, record := range t.serviceRegistry {
		if quest.ServiceDefinition != "" && record.ServiceDefinition != quest.ServiceDefinition {
			continue
		}
		if quest.ProviderName != "" && record.SystemName != quest.ProviderName {
			continue
		}
		matchesAllDetails := true
		for key, values := range requiredDetails {
			recordValues, exists := record.Details[key]
			if !exists || !compareDetails(values, recordValues) {
				matchesAllDetails = false
				break
			}
		}
		if matchesAllDetails {
			matchingRecords = append(matchingRecords, record)
		}
	}
	return matchingRecords
}

// checkExpiration deletes a service record if its validity has lapsed.
func checkExpiration(t *Traits, servId int) {
	t.mu.Lock()
	dbRec := t.serviceRegistry[servId]
	expiration, err := time.Parse(time.RFC3339, dbRec.EndOfValidity)
	if err != nil {
		t.mu.Unlock()
		log.Printf("Time parsing problem when checking service expiration")
		return
	}
	deleted := false
	if time.Now().After(expiration) {
		if _, exists := t.serviceRegistry[servId]; exists {
			delete(t.serviceRegistry, servId)
			t.sched.RemoveTask(servId)
			deleted = true
			log.Printf("The service with ID %d has been deleted because it was not renewed.", servId)
		}
	}
	t.mu.Unlock()
	if deleted {
		// A lapsed registration is a deregistration as far as a subscriber is
		// concerned: the service is not available, and whether it withdrew or
		// simply stopped saying it was there is the registry'''s business, not
		// the subscriber'''s.
		t.notify(forms.RegistryDeregistered, dbRec)
	}
}

// notify tells every subscriber what changed.
//
// Called only for a registration or a deregistration. A service re-registers
// every RegPeriod seconds to confirm it is still there, and waking subscribers
// for that meant more than one full snapshot a second across a cloud of this
// size, every one identical to the last.
func (t *Traits) notify(change string, rec forms.ServiceRecord_v1) {
	event := forms.RegistryEvent_v1{
		Change:    change,
		Record:    rec,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	event.NewForm()

	t.subMu.Lock()
	defer t.subMu.Unlock()
	for _, sub := range t.subscribers {
		select {
		case sub.events <- event:
		default:
			// Never block the registry on a consumer. The subscriber will be
			// told to re-read rather than left believing a stale stream.
			sub.resync.Store(true)
		}
	}
}

//-------------------------------------Service handlers

// updateDB adds a new service record or extends its registration life.
func (t *Traits) updateDB(w http.ResponseWriter, r *http.Request) {
	if !t.leads() {
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
		// One load, so the answer describes a single election outcome.
		role := t.role.Load()
		if role != nil && role.leading {
			fmt.Fprintf(w, "lead Service Registrar since %s", role.since)
			return
		}
		if role != nil && role.registrar != nil {
			http.Error(w, fmt.Sprintf("On standby, leading registrar is %s", role.registrar.Url), http.StatusServiceUnavailable)
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
				status := resp.StatusCode
				// Closed here rather than deferred: this loop never returns, so
				// a deferred close held one response body per peer per tick for
				// the life of the process.
				resp.Body.Close()

				switch status {
				case http.StatusOK:
					standby = true
					t.role.Store(&registrarRole{registrar: cSys})
					break foundLead
				case http.StatusServiceUnavailable:
					// Service unavailable
				default:
					log.Printf("Received unexpected status code: %d\n", resp.StatusCode)
				}
			}
			if !standby && !t.leads() {
				now := time.Now()
				t.role.Store(&registrarRole{leading: true, since: now})
				log.Printf("Taking the service registry lead at %s\n", now)
			}
			<-ticker.C
		}
	}()
}

// systemList returns the list of unique systems registered in the local cloud.
func (t *Traits) systemList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		// Same resource, two representations: ask for the list and you get it
		// once; ask for a stream and you get it once and then every change to
		// it. A subscriber wanting to know when the cloud's shape moves is
		// asking about this resource, so it is the honest place to subscribe.
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.systemListStream(w, r)
			return
		}
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

// systemListStream is the machine subscription: a snapshot of what is
// registered, then one event per registration or deregistration.
//
// The snapshot comes first so a subscriber that reconnects — SSE drops and
// retries on any network hiccup, and events emitted during the gap are gone —
// re-synchronizes rather than carrying on from a state it can no longer trust.
// The same snapshot is re-sent if the registry had to drop events for being too
// far ahead of this connection.
func (t *Traits) systemListStream(w http.ResponseWriter, r *http.Request) {
	// Taken before the headers, so a refusal can still be a refusal. sseHeaders
	// commits the response to 200 and text/event-stream, and http.Error after
	// that writes its message into the stream body under a status the client has
	// already been given.
	sub, remove := t.addSubscriber()
	if sub == nil {
		log.Printf("registry stream: refusing a subscription; %d are already open\n", maxSubscribers)
		http.Error(w, "too many subscriptions are open on this registrar", http.StatusServiceUnavailable)
		return
	}
	defer remove()

	flusher, ok := sseHeaders(w)
	if !ok {
		return
	}

	send := func(eventName string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			log.Printf("registry stream: encoding %s: %v\n", eventName, err)
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, body); err != nil {
			return false // the subscriber has gone
		}
		flusher.Flush()
		return true
	}

	sendSnapshot := func() bool {
		list, err := getUniqueSystems(t)
		if err != nil {
			log.Printf("registry stream: reading the registry: %v\n", err)
			return false
		}
		return send("snapshot", list)
	}

	if !sendSnapshot() {
		return
	}

	// Written to an idle stream so the subscriber can tell silence from death.
	beat := time.NewTicker(keepAlive)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			// A comment line: every SSE parser ignores it, and a write is what
			// discovers that the connection has gone.
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event := <-sub.events:
			if sub.resync.Swap(false) {
				// Events were dropped, so the ones still queued describe an
				// incomplete story. A snapshot supersedes all of them.
				drainEvents(sub.events)
				if !sendSnapshot() {
					return
				}
				continue
			}
			if !send("change", event) {
				return
			}
		}
	}
}

// addSubscriber registers one open connection and returns it with the function
// that removes it again.
func (t *Traits) addSubscriber() (*subscriber, func()) {
	sub := &subscriber{events: make(chan forms.RegistryEvent_v1, subscriberBuffer)}
	t.subMu.Lock()
	if len(t.subscribers) >= maxSubscribers {
		t.subMu.Unlock()
		return nil, nil
	}
	t.subSeq++
	id := t.subSeq
	t.subscribers[id] = sub
	t.subMu.Unlock()
	return sub, func() {
		t.subMu.Lock()
		delete(t.subscribers, id)
		t.subMu.Unlock()
	}
}

// sseHeaders prepares a response for server-sent events and returns the flusher
// each event must be pushed through.
func sseHeaders(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return nil, false
	}
	// Without this the server's WriteTimeout cuts the stream after a minute,
	// however healthy it is. A subscriber then reconnects, and a reconnection
	// opens with a snapshot it cannot tell from news — which is how kgrapher
	// came to rebuild the cloud's knowledge graph twenty-five times an hour
	// through a night in which nothing changed.
	if err := usecases.UnlimitStreamWrite(w); err != nil {
		log.Printf("registry stream: %v\n", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return flusher, true
}

// sseHandler keeps the connection open and pushes a fresh list whenever the registry changes.
//
// This is the browser view: it re-renders the whole list on any change rather
// than following the events, because a page showing what is available wants the
// list, not the deltas. The events only tell it when to look.
func (t *Traits) sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := sseHeaders(w)
	if !ok {
		return
	}

	sub, remove := t.addSubscriber()
	defer remove()

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
		case <-sub.events:
			// The event itself is ignored here and resync needs no special
			// handling: a fresh snapshot is the answer to any number of changes,
			// dropped or delivered.
			sub.resync.Store(false)
			drainEvents(sub.events)
			sendSnapshot()
		}
	}
}

// drainEvents empties a subscriber's backlog without blocking. Used where a
// single snapshot supersedes whatever was queued.
func drainEvents(ch <-chan forms.RegistryEvent_v1) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// getUniqueSystems builds the list of unique system addresses from the registry.
func getUniqueSystems(t *Traits) (*forms.SystemRecordList_v1, error) {
	uniqueSystems := make(map[string]struct{})
	var systemList []string

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, record := range t.serviceRegistry {
		var sAddress string
		if port := record.ProtoPort["https"]; port != 0 {
			sAddress = "https://" + record.IPAddresses[0] + ":" + strconv.Itoa(port) + "/" + record.SystemName
		} else if port := record.ProtoPort["http"]; port != 0 {
			sAddress = "http://" + record.IPAddresses[0] + ":" + strconv.Itoa(port) + "/" + record.SystemName
		} else {
			fmt.Printf("Warning: %s cannot be modeled\n", record.SystemName)
			continue
		}
		if _, added := uniqueSystems[sAddress]; !added {
			uniqueSystems[sAddress] = struct{}{}
			systemList = append(systemList, sAddress)
		}
	}
	return &forms.SystemRecordList_v1{
		List:    systemList,
		Version: "SystemRecordList_v1",
	}, nil
}
