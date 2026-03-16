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
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// Define the types of requests the measurement manager can handle
type STray struct {
	Action string
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

// -------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	InputFile string    `json:"inputFile"`
	sample    float64   `json:"-"`
	tStamp    time.Time `json:"-"`
	trayChan  chan STray `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	stream := components.Service{
		Definition:  "signal",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the (value, timestamp) signal as a service from the input file",
	}

	return &components.UnitAsset{
		Name:    "signal",
		Mission: "replay_signal",
		Details: map[string][]string{"Unit": {"Celsius"}, "Location": {"Kitchen"}},
		ServicesMap: components.Services{
			stream.SubPath: &stream,
		},
		Traits: &Traits{
			InputFile: "data/signal_data.json",
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		trayChan: make(chan STray),
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
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

	go t.emulateAsset(sys.Ctx, configuredAsset.Details)

	return ua, func() {
		log.Printf("disconnecting from %s\n", configuredAsset.Name)
	}
}

//-------------------------------------Unit asset's functionalities

// Sample represents a single data point with a timestamp and value.
type Sample struct {
	Timestamp string  `json:"timestamp" xml:"timestamp"`
	Value     float64 `json:"value" xml:"value"`
}

// XMLSamples is a wrapper for multiple Sample entries in XML.
type XMLSamples struct {
	XMLName xml.Name `xml:"samples"`
	Items   []Sample `xml:"sample"`
}

// emulateAsset runs the emulation loop for the unit asset.
func (t *Traits) emulateAsset(ctx context.Context, details map[string][]string) {
	samples, err := loadSamples(t.InputFile)
	if err != nil {
		log.Fatalf("failed to load samples from %s: %v", t.InputFile, err)
	}
	if len(samples) == 0 {
		log.Fatalf("no samples found in %s", t.InputFile)
	}

	interval := detectInterval(samples)

	var sigUnit string
	if details != nil {
		if sigU, ok := details["Unit"]; ok && len(sigU) > 0 {
			sigUnit = sigU[0]
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	i := 0
	// initialize immediately
	t.sample = samples[i].Value
	t.tStamp = time.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			i = (i + 1) % len(samples)
			t.sample = samples[i].Value
			t.tStamp = time.Now()

		case order := <-t.trayChan:
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = t.sample
			f.Unit = sigUnit
			f.Timestamp = t.tStamp
			order.ValueP <- f
		}
	}
}

// loadSamples chooses the correct loader based on file extension.
func loadSamples(filename string) ([]Sample, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".csv":
		return loadCSV(filename)
	case ".json":
		return loadJSON(filename)
	case ".xml":
		return loadXML(filename)
	default:
		return nil, fmt.Errorf("unsupported file extension: %s", ext)
	}
}

func loadCSV(filename string) ([]Sample, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)

	// Read and discard header row (timestamp,value)
	if _, err := r.Read(); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}

	var samples []Sample

	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) < 2 {
			continue
		}

		ts := strings.TrimSpace(record[0])
		valStr := strings.TrimSpace(record[1])

		v, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q: %w", valStr, err)
		}

		samples = append(samples, Sample{
			Timestamp: ts,
			Value:     v,
		})
	}

	return samples, nil
}

func loadJSON(filename string) ([]Sample, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var samples []Sample
	if err := json.Unmarshal(data, &samples); err != nil {
		return nil, err
	}

	return samples, nil
}

// loadXML loads samples from an XML file.
func loadXML(filename string) ([]Sample, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var wrapper XMLSamples
	if err := xml.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	return wrapper.Items, nil
}

// detectInterval infers the time step between samples based on
// the first two timestamps. Falls back to 1 second if it cannot.
func detectInterval(samples []Sample) time.Duration {
	if len(samples) < 2 {
		return time.Second
	}

	t0, err0 := time.Parse(time.RFC3339, samples[0].Timestamp)
	t1, err1 := time.Parse(time.RFC3339, samples[1].Timestamp)
	if err0 != nil || err1 != nil {
		return time.Second
	}

	d := t1.Sub(t0)
	if d <= 0 {
		return time.Second
	}

	return d
}
