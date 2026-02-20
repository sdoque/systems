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
	trayChan chan STray `json:"-"` // Add a channel for samples signal information transfer
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
		Definition:  "signal",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the (value, timestamp) signal as a service from the input file",
	}

	assetTraits := Traits{
		InputFile: "data/signal_data.json",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "signal",
		Details: map[string][]string{"Unit": {"Celsius"}, "Location": {"Kitchen"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			stream.SubPath: &stream,
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
		trayChan:    make(chan STray), // Initialize the channel
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}
	// start the unit asset(s)
	go ua.emulateAsset(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
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
func (ua *UnitAsset) emulateAsset(ctx context.Context) {
	samples, err := loadSamples(ua.InputFile)
	if err != nil {
		log.Fatalf("failed to load samples from %s: %v", ua.InputFile, err)
	}
	if len(samples) == 0 {
		log.Fatalf("no samples found in %s", ua.InputFile)
	}

	interval := detectInterval(samples)

	var sigUnit string
	if ua.Details != nil {
		if sigU, ok := ua.Details["Unit"]; ok && len(sigU) > 0 {
			sigUnit = sigU[0]
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	i := 0
	// initialize immediately
	ua.sample = samples[i].Value
	ua.tStamp = time.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			i = (i + 1) % len(samples) // or stop at end if you prefer
			ua.sample = samples[i].Value
			ua.tStamp = time.Now()

		case order := <-ua.trayChan:
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = ua.sample
			f.Unit = sigUnit
			f.Timestamp = ua.tStamp
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
