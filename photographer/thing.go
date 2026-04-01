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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	owner *components.System `json:"-"`
	name  string             `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	photograph := components.Service{
		Definition:  "photograph",
		SubPath:     "photograph",
		Details:     map[string][]string{"Forms": {"jpeg_v1a"}},
		Description: " takes a picture (GET) and saves it as a file",
	}

	return &components.UnitAsset{
		Name:    "PiCam",
		Mission: "capture_photographe",
		Details: map[string][]string{"Model": {"PiCam v2"}, "FunctionalLocation": {"Entrance"}},
		ServicesMap: components.Services{
			photograph.SubPath: &photograph,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
		name:  configuredAsset.Name,
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
		log.Println("disconnecting from sensors")
	}
}

//-------------------------------------Unit asset's resource functions

func (t *Traits) takePicture() (f forms.FileForm_v1, err error) {
	err = os.MkdirAll("./files", os.ModePerm)
	if err != nil {
		return f, fmt.Errorf("failed to create directory: %w", err)
	}

	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("/files/image_%s.jpg", timestamp)

	cmd := exec.Command("libcamera-still", "-o", "."+filename)

	if err := cmd.Run(); err != nil {
		return f, fmt.Errorf("failed to take picture: %w", err)
	}
	urlPath := "http://" + t.owner.Husk.Host.IPAddresses[0] + ":" + strconv.Itoa(int(t.owner.Husk.ProtoPort["http"])) + "/" + t.owner.Name + "/" + t.name
	f.NewForm()
	f.FileURL = urlPath + filename
	f.Timestamp = time.Now()
	return f, nil
}
