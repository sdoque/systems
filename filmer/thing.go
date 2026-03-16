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
	owner *components.System `json:"-"`
	name  string             `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	stream := components.Service{
		Definition:  "stream",
		SubPath:     "start",
		Details:     map[string][]string{"Forms": {"mpeg"}},
		Description: " provides a video stream from the camera",
	}

	return &components.UnitAsset{
		Name:    "PiCam",
		Mission: "capture_video",
		Details: map[string][]string{"Model": {"PiCam v3 NoIR"}},
		ServicesMap: components.Services{
			stream.SubPath: &stream,
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

// StartStreamURL returns the URL to start the video stream.
func (t *Traits) StartStreamURL() string {
	ip := t.owner.Husk.Host.IPAddresses[0]
	port := t.owner.Husk.ProtoPort["http"]
	return fmt.Sprintf("http://%s:%d/filmer/%s/stream", ip, port, t.name)
}

// StreamTo streams the video from the camera to the HTTP response writer.
func (t *Traits) StreamTo(w http.ResponseWriter, r *http.Request) {
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
					f.Flush()
				}
			} else {
				break
			}
		}
	}
}
