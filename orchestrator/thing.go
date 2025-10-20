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

	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the Thing's resource

// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	leadingRegistrar string
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
		leadingRegistrar: "", // Initialize the leading registrar to nil
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
// If no leading registrar is found, it iterates through the system's core services
// to find one.
// Once a valid registrar is found, it sends a query to the registrar to get the
// service URL.
//
// Parameters:
// - newQuest: The ServiceQuest_v1 containing the service request details.
//
// Returns:
// - servLoc: A byte slice containing the service location in JSON format.
// - err: An error if any issues occur during the process.
func (ua *UnitAsset) getServiceURL(newQuest forms.ServiceQuest_v1) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sys := ua.Owner
	if ua.leadingRegistrar == "" {
		ua.leadingRegistrar, err = components.GetRunningCoreSystemURL(sys, "serviceregistrar")
		if err != nil {
			return servLoc, err
		}
	}

	// Create a new HTTP request to the the Service Registrar
	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := ua.leadingRegistrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ua.leadingRegistrar = ""
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return servLoc, err
	}
	serviceListf, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return servLoc, err
	}

	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		return nil, fmt.Errorf("problem asserting the type of the service list form")
	}

	if len(serviceList.List) == 0 {
		return nil, fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
	}

	serviceLocation := selectService(*serviceList)
	payload, err := json.MarshalIndent(serviceLocation, "", "  ")
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

func (ua *UnitAsset) getServicesURL(newQuest forms.ServiceQuest_v1) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sys := ua.Owner
	if ua.leadingRegistrar == "" {
		ua.leadingRegistrar, err = components.GetRunningCoreSystemURL(sys, "serviceregistrar")
		if err != nil {
			return servLoc, err
		}
	}

	// Create a new HTTP request to the the Service Registrar
	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := ua.leadingRegistrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ua.leadingRegistrar = ""
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return servLoc, err
	}
	serviceListf, err := usecases.Unpack(respBytes, mediaType)
	if err != nil {
		return servLoc, err
	}

	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		return nil, fmt.Errorf("problem asserting the type of the service list form")
	}

	if len(serviceList.List) == 0 {
		return nil, fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
	}

	payload, err := json.MarshalIndent(serviceList, "", "  ")
	return payload, err
}
