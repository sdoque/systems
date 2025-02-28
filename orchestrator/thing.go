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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

//-------------------------------------Define the Thing's resource

// UnitAsset type models the unit asset (interface) of the system.
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	leadingRegistrar *components.CoreSystem
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

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instatiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Location": {"LocalCloud"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		ServicesMap: components.Services{
			squest.SubPath: &squest, // Inline assignment of the temperature service
		},
	}
	return uat
}

//-------------------------------------Instatiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConig structs
func newResource(uac UnitAsset, sys *components.System, servs []components.Service) (components.UnitAsset, func()) {
	// var ua components.UnitAsset // this is an interface, which we then initialize
	ua := &UnitAsset{ // this is an interface, which we then initialize
		Name:        uac.Name,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: components.CloneServices(servs),
	}

	// start the unit asset(s)
	// no need to start the algorithm asset

	return ua, func() {
		log.Println("Ending orchestration services")
	}
}

//-------------------------------------Thing's resource functions

// getServiceURL (works only on an RPi) reads the input file w1_slave for the specific 1 wire sensor
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

		// Read from resp.Body and then close it directly after
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
					fmt.Println("Error checking service registar status:", err)
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
	jsonQF, err := json.MarshalIndent(newQuest, "", "  ")
	if err != nil {
		log.Printf("problem encountered when marshalling the service quest\n")
		return servLoc, err
	}

	srURL := ua.leadingRegistrar.Url + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", "application/json") // set the Content-Type header
	req = req.WithContext(ctx)                         // associate the cancellable context with the request

	// forward the request /////////////////////////////////
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		ua.leadingRegistrar = nil
		return servLoc, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading discoverry response body: %v", err)
		return servLoc, err
	}
	serviceList, err := extractServList(respBytes)
	if err != nil {
		log.Print("Error extracting discoverry reply", err)
		return servLoc, err
	}
	if len(serviceList.List) == 0 {
		err = fmt.Errorf("unable to locate any such service: %s", newQuest.ServiceDefinition)
		return
	}

	fmt.Printf("/n the length of the service list is: %d\n", len(serviceList.List))
	serviceLocation, err := selectService(serviceList, newQuest)
	if err != nil {
		return
	}
	payload, err := json.MarshalIndent(serviceLocation, "", "  ")
	fmt.Printf("the service location is %+v\n", serviceLocation)
	return payload, err
}

func extractServList(bodyBytes []byte) (rec forms.ServiceRecordList_v1, err error) {
	var jsonData map[string]interface{}
	err = json.Unmarshal(bodyBytes, &jsonData)
	if err != nil {
		log.Printf("Error unmarshaling JSON data: %v", err)
		return
	}
	formVersion, ok := jsonData["Version"].(string)
	if !ok {
		log.Printf("Error: 'version' key not found in JSON data")
		return
	}
	switch formVersion {
	case "ServiceRecordList_v1":
		var f forms.ServiceRecordList_v1
		err = json.Unmarshal(bodyBytes, &f)
		if err != nil {
			log.Println("Unable to extract discoverry reply")
			return
		}
		rec = f
	default:
		err = errors.New("unsupported service discoverry form version")
	}
	return
}

func find(needle string, stack []string) bool {
	for _, s := range stack {
		if s == needle {
			return true
		}
	}
	return false
}

func findService(location string, list []forms.ServiceRecord_v1) (rec forms.ServiceRecord_v1) {
	for _, rec := range list {
		if find(location, rec.Details["Location"]) {
			return rec
		}
	}
	return
}

func selectService(serviceList forms.ServiceRecordList_v1, newQuest forms.ServiceQuest_v1) (serv forms.ServicePoint_v1, err error) {
	// Try to find a service with a matching location as the "questioner".
	var rec forms.ServiceRecord_v1
	for _, loc := range newQuest.Details["Location"] {
		rec = findService(loc, serviceList.List)
		if len(rec.ServiceDefinition) > 0 {
			// Found it!
			serv.NewForm()
			serv.ProviderName = rec.SystemName
			serv.ProviderCertificate = rec.Certificate
			serv.ServiceDefinition = rec.ServiceDefinition
			serv.Details = rec.Details
			serv.ServLocation = "http://" + rec.IPAddresses[0] + ":" + strconv.Itoa(rec.ProtoPort["http"]) + "/" + rec.SystemName + "/" + rec.SubPath
			return
		}
	}
	err = fmt.Errorf("couldn't find matching service for: %v", newQuest)
	return
}
