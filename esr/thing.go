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
	"log"
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
	Result chan []forms.ServiceRecord_v1 // For returning records
	Error  chan error
}

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	serviceRegistry map[int]forms.ServiceRecord_v1

	recCount int64
	requests chan ServiceRegistryRequest
	// Error            chan error // For error handling
	sched            *Scheduler
	leading          bool
	leadingSince     time.Time
	leadingRegistrar *components.CoreSystem // if not leading this points to the current leader
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	Traits
	mu sync.Mutex
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetServices() components.Services {
	return ua.ServicesMap
}

// GetCervices returns the list of consumed services by the Resource.
func (ua *UnitAsset) GetCervices() components.Cervices {
	return ua.CervicesMap
}

// GetDetails returns the details of the Resource.
func (ua *UnitAsset) GetDetails() map[string][]string {
	return ua.Details
}

// GetTraits returns the traits of the Resource.
func (ua *UnitAsset) GetTraits() any {
	return ua.Traits
}

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
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

	// Create the UnitAsset with the defined services
	uat := &UnitAsset{
		Name:    "registry",
		Details: map[string][]string{"Type": {"ephemeral"}},
		ServicesMap: components.Services{
			registerService.SubPath:   &registerService,
			queryService.SubPath:      &queryService,
			unregisterService.SubPath: &unregisterService,
			statusService.SubPath:     &statusService,
		},
	}
	return uat
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration using the uaConfig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	// Start the registration expiration check scheduler
	cleaningScheduler := NewScheduler()

	// Initialize the UnitAsset
	ua := &UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	assetTraits := Traits{
		serviceRegistry: make(map[int]forms.ServiceRecord_v1),
		recCount:        1, // 0 is used for non registered services
		sched:           cleaningScheduler,
		requests:        make(chan ServiceRegistryRequest), // Initialize the requests channel
		// Error:           make(chan error),                  // Initialize the error channel
	}

	ua.Traits = assetTraits // Assign the traits to the UnitAsset

	// Start to repeatedly check which is the leading registrar
	ua.Role()

	// Start the service registry manager goroutine
	go ua.serviceRegistryHandler()

	return ua, func() {
		ua.mu.Lock()
		close(ua.requests)       // Close channels before exiting (cleanup)
		cleaningScheduler.Stop() // Gracefully stop the scheduler
		ua.mu.Unlock()
		log.Println("Closing the service registry database connection")
	}
}

// UnmarshalTraits unmarshals a slice of json.RawMessage into a slice of Traits.
func UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("failed to unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

//-------------------------------------Unit's resource methods

// There are really two assets here: the database  and the scheduler
// The scheduler is (protected) in a third file: scheduler.go

// ServiceRegistryManager manages all service registry operations via channels
func (ua *UnitAsset) serviceRegistryHandler() {
	for request := range ua.requests {
		now := time.Now()
		switch request.Action {
		case "add":
			rec, ok := request.Record.(*forms.ServiceRecord_v1)
			if !ok {
				fmt.Println("Problem unpacking the service registration request")
				request.Error <- fmt.Errorf("invalid record type")
				continue
			}
			ua.mu.Lock() // Lock the serviceRegistry map

			// Check if the ID exists in the serviceRegistry
			if _, exists := ua.serviceRegistry[rec.Id]; !exists {
				rec.Id = 0
			}

			if rec.Id == 0 {
				// In the case recCount had looped, check that there is no record at that position
				for {
					currentCount := atomic.LoadInt64(&ua.recCount)
					_, exists := ua.serviceRegistry[int(currentCount)]
					if !exists {
						atomic.StoreInt64(&ua.recCount, currentCount)
						rec.Id = int(currentCount)
						break
					}
					atomic.AddInt64(&ua.recCount, 1)
				}

				// Update the record
				rec.Id = int(ua.recCount)
				rec.Created = now.Format(time.RFC3339)
				rec.Updated = now.Format(time.RFC3339)
				rec.EndOfValidity = now.Add(time.Duration(rec.RegLife) * time.Second).Format(time.RFC3339)
				log.Printf("The new service %s from system %s has been registered\n", rec.ServiceDefinition, rec.SystemName)
			} else {
				// Validate and update existing record
				dbRec := ua.serviceRegistry[rec.Id]
				if dbRec.ServiceDefinition != rec.ServiceDefinition {
					request.Error <- errors.New("mismatch between definition received record and database record")
					ua.mu.Unlock()
					continue
				}
				if dbRec.SubPath != rec.SubPath {
					request.Error <- errors.New("mismatch between path received record and database record")
					ua.mu.Unlock()
					continue
				}
				recCreated, err := time.Parse(time.RFC3339, rec.Created)
				if err != nil {
					request.Error <- errors.New("time parsing problem with updated record")
					ua.mu.Unlock()
					continue
				}
				dbCreated, err := time.Parse(time.RFC3339, dbRec.Created)
				if err != nil {
					request.Error <- errors.New("time parsing problem with archived record")
					ua.mu.Unlock()
					continue
				}
				if !recCreated.Equal(dbCreated) {
					request.Error <- errors.New("mismatch between created received record and database record")
					ua.mu.Unlock()
					continue
				}
				nextExpiration := now.Add(time.Duration(dbRec.RegLife) * time.Second).Format(time.RFC3339)
				rec.EndOfValidity = nextExpiration
			}
			ua.sched.AddTask(now.Add(time.Duration(rec.RegLife)*time.Second), func() { checkExpiration(ua, rec.Id) }, rec.Id)
			ua.serviceRegistry[rec.Id] = *rec // Add record to the registry
			request.Record = rec
			ua.mu.Unlock()
			request.Error <- nil // Send success response

		case "read":
			// Handle read records
			if request.Record == nil {
				var result []forms.ServiceRecord_v1
				ua.mu.Lock() // Lock the serviceRegistry map
				for _, record := range ua.serviceRegistry {
					result = append(result, record)
				}
				ua.mu.Unlock() // Unlock access to the service registry map
				request.Result <- result
				continue
			}
			qform, ok := request.Record.(*forms.ServiceQuest_v1)
			if !ok {
				log.Println("Problem unpacking the service quest request")
				request.Error <- fmt.Errorf("invalid record type")
				continue
			}
			matchingRecords := ua.FilterByServiceDefinitionAndDetails(qform.ServiceDefinition, qform.Details)
			request.Result <- matchingRecords

		case "delete":
			// Handle delete record
			ua.mu.Lock()
			ua.sched.RemoveTask(int(request.Id))
			delete(ua.serviceRegistry, int(request.Id))
			if _, exists := ua.serviceRegistry[int(request.Id)]; !exists {
				log.Printf("The service with ID %d has been deleted.", request.Id)
			}
			ua.mu.Unlock()
			request.Error <- nil // Send success response
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

// FilterByServiceDefinitionAndDetails returns a list of services with the given service definition and details TODO: protocols
func (ua *UnitAsset) FilterByServiceDefinitionAndDetails(desiredDefinition string, requiredDetails map[string][]string) []forms.ServiceRecord_v1 {
	ua.mu.Lock() // Ensure thread safety
	defer ua.mu.Unlock()

	var matchingRecords []forms.ServiceRecord_v1

	for _, record := range ua.serviceRegistry {
		if record.ServiceDefinition == desiredDefinition {
			matchesAllDetails := true

			// Check if all required details match
			for key, values := range requiredDetails {
				recordValues, exists := record.Details[key]
				if !exists {
					matchesAllDetails = false
					break
				}

				// Ensure at least one value in requiredDetails matches record.Details
				if !compareDetails(values, recordValues) {
					matchesAllDetails = false
					break
				}
			}

			if matchesAllDetails {
				matchingRecords = append(matchingRecords, record)
			}
		}
	}

	return matchingRecords
}

// checkExpiration checks if a service has expired and deletes it if it has.
func checkExpiration(ua *UnitAsset, servId int) {
	ua.mu.Lock()
	defer ua.mu.Unlock()
	dbRec := ua.serviceRegistry[servId]
	expiration, err := time.Parse(time.RFC3339, dbRec.EndOfValidity)
	if err != nil {
		log.Printf("Time parsing problem when checking service expiration")
		return
	}

	if time.Now().After(expiration) {
		if _, exists := ua.serviceRegistry[servId]; !exists {
			return
		}
		delete(ua.serviceRegistry, int(servId))
		ua.sched.RemoveTask(int(servId))
		if _, exists := ua.serviceRegistry[servId]; !exists {
			log.Printf("The service with ID %d has been deleted because it was not renewed.", servId)
		}
	}
}

// getUniqueSystems populates the list of systems in a local cloud
func getUniqueSystems(ua *UnitAsset) (*forms.SystemRecordList_v1, error) {
	uniqueSystems := make(map[string]struct{}) // to ensure uniqueness
	var systemList []string                    // final list of unique systems

	ua.mu.Lock() // Ensure thread safety
	defer ua.mu.Unlock()
	for _, record := range ua.serviceRegistry {
		var sAddress string

		// Check for HTTPS
		if port := record.ProtoPort["https"]; port != 0 {
			sAddress = "https://" + record.IPAddresses[0] + ":" + strconv.Itoa(port) + "/" + record.SystemName
		} else if port := record.ProtoPort["http"]; port != 0 { // Check for HTTP
			sAddress = "http://" + record.IPAddresses[0] + ":" + strconv.Itoa(port) + "/" + record.SystemName
		} else {
			fmt.Printf("Warning: %s cannot be modeled\n", record.SystemName)
			continue
		}

		// Ensure uniqueness
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
