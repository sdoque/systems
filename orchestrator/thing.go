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
	owner            *components.System `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	return &components.UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		Traits:  &Traits{},
		ServicesMap: components.Services{
			squest.SubPath: &squest,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the template
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
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

	return ua, func() {
		log.Println("Ending orchestration services")
	}
}

//-------------------------------------Thing's resource functions

// getServiceURL retrieves the service URL for a given ServiceQuest_v1.
func (t *Traits) getServiceURL(newQuest forms.ServiceQuest_v1) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if t.leadingRegistrar == "" {
		t.leadingRegistrar, err = components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
		if err != nil {
			return servLoc, err
		}
	}

	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := t.leadingRegistrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingRegistrar = ""
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

func (t *Traits) getServicesURL(newQuest forms.ServiceQuest_v1) (servLoc []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if t.leadingRegistrar == "" {
		t.leadingRegistrar, err = components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
		if err != nil {
			return servLoc, err
		}
	}

	mediaType := "application/json"
	jsonQF, err := usecases.Pack(&newQuest, mediaType)
	if err != nil {
		return servLoc, err
	}

	srURL := t.leadingRegistrar + "/query"
	req, err := http.NewRequest(http.MethodPost, srURL, bytes.NewBuffer(jsonQF))
	if err != nil {
		return servLoc, err
	}
	req.Header.Set("Content-Type", mediaType)
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.leadingRegistrar = ""
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
