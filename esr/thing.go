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
	"log"
	"net/http"
	"slices"
	"strconv"
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
