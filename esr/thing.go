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
	serviceRegistry  map[int]forms.ServiceRecord_v1
	recCount         int64
	requests         chan ServiceRegistryRequest
	sched            *Scheduler
	leading          bool
	leadingSince     time.Time
	leadingRegistrar *components.CoreSystem
	mu               sync.Mutex
	subscribers      map[int]chan struct{} // SSE listeners, keyed by connection ID
	subMu            sync.Mutex
	subSeq           int
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	registerService := components.Service{
		Definition:  "register",
		SubPath:     "register",
		Details:     map[string][]string{"Forms": usecases.ServiceRegistrationFormsList()},
		Description: "registers a service (POST) or updates its expiration time (PUT)",
	}
	queryService := components.Service{
		Definition:  "query",
		SubPath:     "query",
		Details:     map[string][]string{"Forms": usecases.ServQuestForms()},
		Description: "retrieves all currently available services using a GET request [accessed via a browser by a deployment technician] or retrieves a specific set of services using a POST request with a payload [initiated by the Orchestrator]",
	}
	unregisterService := components.Service{
		Definition:  "unregister",
		SubPath:     "unregister",
		Details:     map[string][]string{"Forms": {"ID_only"}},
		Description: "removes a record (DELETE) based on record ID",
	}
	statusService := components.Service{
		Definition:  "status",
		SubPath:     "status",
		Details:     map[string][]string{"Forms": {"none"}},
		Description: "reports (GET) the role of the Service Registrar as leading or on stand by",
	}

	return &components.UnitAsset{
		Name:    "registry",
		Details: map[string][]string{"Type": {"ephemeral"}},
		ServicesMap: components.Services{
			registerService.SubPath:   &registerService,
			queryService.SubPath:      &queryService,
			unregisterService.SubPath: &unregisterService,
			statusService.SubPath:     &statusService,
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
		subscribers:     make(map[int]chan struct{}),
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
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
			t.notify()
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
			request.Result <- t.FilterByServiceDefinitionAndDetails(qform.ServiceDefinition, qform.Details)

		case "delete":
			t.mu.Lock()
			t.sched.RemoveTask(int(request.Id))
			delete(t.serviceRegistry, int(request.Id))
			if _, exists := t.serviceRegistry[int(request.Id)]; !exists {
				log.Printf("The service with ID %d has been deleted.", request.Id)
			}
			t.mu.Unlock()
			t.notify()
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

// FilterByServiceDefinitionAndDetails returns services matching the given definition and details.
func (t *Traits) FilterByServiceDefinitionAndDetails(desiredDefinition string, requiredDetails map[string][]string) []forms.ServiceRecord_v1 {
	t.mu.Lock()
	defer t.mu.Unlock()

	var matchingRecords []forms.ServiceRecord_v1
	for _, record := range t.serviceRegistry {
		if record.ServiceDefinition != desiredDefinition {
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
		t.notify()
	}
}

// notify wakes all active SSE subscribers with a non-blocking send.
func (t *Traits) notify() {
	t.subMu.Lock()
	defer t.subMu.Unlock()
	for _, ch := range t.subscribers {
		select {
		case ch <- struct{}{}:
		default: // subscriber goroutine is busy; it will catch up on the next event
		}
	}
}

//-------------------------------------Service handlers

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
