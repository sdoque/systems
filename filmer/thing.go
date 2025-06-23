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
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters and variables
type Traits struct {
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
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
	stream := components.Service{
		Definition:  "stream",
		SubPath:     "start",
		Details:     map[string][]string{"Forms": {"mpeg"}},
		Description: " provides a video stream from the camera",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "PiCam",
		Details: map[string][]string{"Model": {"PiCam v3 NoIR"}},
		ServicesMap: components.Services{
			stream.SubPath: &stream, // Inline assignment of the temperature service
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	ua := &UnitAsset{ // this a struct that implements the UnitAsset interface
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

	return ua, func() {
		log.Println("disconnecting from sensors")
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

//-------------------------------------Unit asset's resource functions

// StartStreamURL returns the URL to start the video stream.
func (ua *UnitAsset) StartStreamURL() string {
	ip := ua.Owner.Host.IPAddresses[0]
	port := ua.Owner.Husk.ProtoPort["http"]
	return fmt.Sprintf("http://%s:%d/filmer/%s/stream", ip, port, ua.Name)
}

// StreamTo streams the video from the camera to the HTTP response writer.
// It uses libcamera-vid to capture video and sends it as a multipart MJPEG stream.
func (ua *UnitAsset) StreamTo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	cmd := exec.Command("libcamera-vid",
		"-t", "0",
		"--codec", "mjpeg",
		"--width", "640",
		"--height", "480",
		"--framerate", "15",
		"-o", "-")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to create pipe: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start libcamera-vid: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		<-r.Context().Done()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	buffer := make([]byte, 0)
	temp := make([]byte, 4096)

	for {
		n, err := stdout.Read(temp)
		if err != nil {
			log.Println("stream read error:", err)
			break
		}
		buffer = append(buffer, temp[:n]...)

		for {
			start := bytes.Index(buffer, []byte{0xFF, 0xD8}) // JPEG SOI
			end := bytes.Index(buffer, []byte{0xFF, 0xD9})   // JPEG EOI
			if start >= 0 && end > start {
				frame := buffer[start : end+2]
				buffer = buffer[end+2:]

				fmt.Fprintf(w, "--frame\r\n")
				fmt.Fprintf(w, "Content-Type: image/jpeg\r\n")
				fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(frame))
				w.Write(frame)

				if f, ok := w.(http.Flusher); ok {
					f.Flush() // very important!
				}
			} else {
				break
			}
		}
	}
}
