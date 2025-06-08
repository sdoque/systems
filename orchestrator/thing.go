/*******************************************************************************
 * Copyright (c) 2023 Jan van Deventer
 *
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-2.0/
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the Thing's resource

// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	leadingRegistrar *components.CoreSystem
}

// UnitAsset type models the unit asset (interface) of the system.
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	Traits
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
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Location": {"LocalCloud"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	assetTraits := Traits{
		leadingRegistrar: nil, // Initialize the leading registrar to nil
	}

	// create the unit asset template
	uat := &UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			squest.SubPath: &squest, // Inline assignment of the temperature service
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the template
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	// var ua components.UnitAsset // this is an interface, which we then initialize
	ua := &UnitAsset{ // this is an interface, which we then initialize
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

	// start the unit asset(s)
	// no need to start the algorithm asset

	return ua, func() {
		log.Println("Ending orchestration services")
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

//-------------------------------------Thing's resource functions

// getServiceURL retrieves the service URL for a given ServiceQuest_v1.
// It first checks if the leading registrar is still valid and updates it if necessary.
// If no leading registrar is found, it iterates through the system's core services to find one.
// Once a valid registrar is found, it sends a query to the registrar to get the service URL.
//
// Parameters:
// - newQuest: The ServiceQuest_v1 containing the service request details.
//
// Returns:
// - servLoc: A byte slice containing the service location in JSON format.
// - err: An error if any issues occur during the process.
func (ua *UnitAsset) getServiceURL(newQuest forms.ServiceQuest_v1) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // Create a new context, with a 2-second timeout
	defer cancel()
	sys := ua.Owner
	if ua.leadingRegistrar != nil {

		// verify that this leading registrar is still leading
		resp, errs := http.Get(ua.leadingRegistrar.Url + "/status")
		if errs != nil {
			log.Println("lost leading registrar status:", errs)
			ua.leadingRegistrar = nil
			err = errs
			return // Skip to the next iteration of the loop
		}

		// Read from status resp.Body and then close it directly after
		bodyBytes, errs := io.ReadAll(resp.Body)
		resp.Body.Close() // Close the body directly after reading from it
		if errs != nil {
			log.Println("\rError reading response from leading registrar:", errs)
			ua.leadingRegistrar = nil
			err = errs
			return // Skip to the next iteration of the loop
		}

		// reset the pointer if the registrar lost its leading status
		if !strings.HasPrefix(string(bodyBytes), "lead Service Registrar since") {
			ua.leadingRegistrar = nil
			log.Println("lost previous leading registrar")
		}
	} else {
		for _, cSys := range sys.CoreS {
			core := cSys
			if core.Name == "serviceregistrar" {
				resp, err := http.Get(core.Url + "/status")
				if err != nil {
					fmt.Println("Error checking service registrar status:", err)
					ua.leadingRegistrar = nil // clear the leading registrar record
					continue                  // Skip to the next iteration of the loop
				}

				// Read from resp.Body and then close it directly after
				bodyBytes, err := io.ReadAll(resp.Body)
				resp.Body.Close() // Close the body directly after reading from it
				if err != nil {
					fmt.Println("Error reading service registrar response body:", err)
					continue // Skip to the next iteration of the loop
				}

				if strings.HasPrefix(string(bodyBytes), "lead Service Registrar since") {
					ua.leadingRegistrar = core
					fmt.Printf("\nlead registrar found at: %s\n", ua.leadingRegistrar.Url)
				}
			}
		}
	}

	// Create a new HTTP request to the the Service Registrar

	// Create buffer to save a copy of the request body
	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		log.Printf("problem encountered when marshalling the service quest\n")
		return servLoc, err
	}

	srURL := ua.leadingRegistrar.Url + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType) // set the Content-Type header
	req = req.WithContext(ctx)                // associate the cancellable context with the request

	// forward the request to the leading Service Registrar/////////////////////////////////
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		ua.leadingRegistrar = nil
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading discovery response body: %v", err)
		return servLoc, err
	}
	fmt.Printf("\n%v\n", string(respBytes))
	serviceListf, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		log.Print("Error extracting discovery reply ", err)
		return servLoc, err
	}

	// Perform a type assertion to convert the returned Form to SignalA_v1a
	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		log.Println("problem asserting the type of the service list form")
		return
	}

	if len(serviceList.List) == 0 {
		err = fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
		return
	}

	fmt.Printf("/n the length of the service list is: %d\n", len(serviceList.List))
	serviceLocation := selectService(*serviceList)
	payload, err := json.MarshalIndent(serviceLocation, "", "  ")
	fmt.Printf("the service location is %+v\n", serviceLocation)
	return payload, err
}

func selectService(serviceList forms.ServiceRecordList_v1) (sp forms.ServicePoint_v1) {
	rec := serviceList.List[0]
	sp.NewForm()
	sp.ProviderName = rec.SystemName
	sp.ServiceDefinition = rec.ServiceDefinition
	sp.Details = rec.Details
	sp.ServLocation = "http://" + rec.IPAddresses[0] + ":" + strconv.Itoa(rec.ProtoPort["http"]) + "/" + rec.SystemName + "/" + rec.SubPath
	sp.ServNode = rec.ServiceNode
	return
}
