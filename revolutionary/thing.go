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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// Define the types of requests the measurement manager can handle
type ServiceTray struct {
	SampledDatum chan forms.SignalA_v1a
	Error        chan error
}

// -------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	Address        string           `json:"address"`  // Address of the IO
	Value          float64          `json:"value"`    // Start up value of the IO
	MinValue       float64          `json:"minValue"` // Minimum value of the IO
	MaxValue       float64          `json:"maxValue"` // Maximum value of the IO
	tStamp         time.Time        `json:"-"`
	serviceChannel chan ServiceTray `json:"-"`
	outputChannel  chan float64     `json:"-"`
	name           string           `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	access := components.Service{
		Definition:  "level",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "reads the input (GET) or changes the output (POST) of the channel",
	}

	return &components.UnitAsset{
		Name:    "LevelSensor_1",
		Mission: "measure_level",
		Details: map[string][]string{"Unit": {"Percent"}, "FunctionalLocation": {"UpperTank"}, "Description": {"level"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Traits: &Traits{
			Address: "InputValue_1",
			Value:   0.0,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		serviceChannel: make(chan ServiceTray),
		outputChannel:  make(chan float64),
		name:           configuredAsset.Name,
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

	go t.sampleSignal(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", configuredAsset.Name)
	}
}

//-------------------------------------Service handlers

// access gets the unit asset's AIO channel datum and sends it in a signal form
func (t *Traits) access(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Prepare a fresh tray for this request
		requestTray := ServiceTray{
			SampledDatum: make(chan forms.SignalA_v1a),
			Error:        make(chan error),
		}
		t.serviceChannel <- requestTray
		select {
		case err := <-requestTray.Error:
			log.Printf("Logic error in getting measurement: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		case signalForm := <-requestTray.SampledDatum:
			usecases.HTTPProcessGetRequest(w, r, &signalForm)
			return
		case <-time.After(5 * time.Second):
			http.Error(w, "Request timed out", http.StatusGatewayTimeout)
			log.Println("Timeout on GET access")
			return
		}

	case http.MethodPost, http.MethodPut:
		// Unpack the incoming form
		log.Printf("Unpacking output signal form for %s", t.name)
		contentType := r.Header.Get("Content-Type")
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			log.Printf("Error parsing media type: %v", err)
			http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
			return
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("Error reading request body: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		serviceReq, err := usecases.Unpack(bodyBytes, mediaType)
		if err != nil {
			log.Printf("Error unpacking output signal form: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		outputForm, ok := serviceReq.(*forms.SignalA_v1a)
		if !ok {
			log.Println("Unexpected form type in access")
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		t.outputChannel <- outputForm.Value // Send the value to the output channel for processing
		w.WriteHeader(http.StatusOK)        // Respond with 200 OK if the write is successful

	default:
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
	}
}

//-------------------------------------Unit asset's functionalities

// sampleSignal obtains the signal from the Rev Pi AIO resource at regular intervals
func (t *Traits) sampleSignal(ctx context.Context) {
	// Create a ticker that triggers every second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop() // Clean up the ticker when done

	sigChan := make(chan float64) // Channel for latest signal readings
	tStampChan := make(chan time.Time)

	// Start a separate goroutine for signal reading
	go func() {
		for {
			select {
			case <-ctx.Done(): // Stop when the context is canceled
				os.Exit(0)
				return

			case <-ticker.C: // sample the signal at regular intervals
				v, err := readInputVoltage(t.Address)
				if err != nil {
					fmt.Println("Read error:", err)
				} else {
					fmt.Printf("%s = %.2f V\n", t.name, v/1000)
				}
				nv := NormalizeToPercent(v, t.MinValue, t.MaxValue)

				// Send the sampled signal and timestamp back to the main loop
				select {
				case sigChan <- nv:
					tStampChan <- time.Now()
				case <-ctx.Done(): // Stop the goroutine if context is canceled
					return
				}
			}
		}
	}()

	for {
		select {
		case sigValue := <-sigChan: // Update signal value and timestamp
			t.Value = sigValue
			t.tStamp = <-tStampChan
		case order := <-t.serviceChannel:
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = t.Value
			f.Unit = "Percent"
			f.Timestamp = t.tStamp
			order.SampledDatum <- f
		case requestedOutput := <-t.outputChannel:
			log.Printf("Received output request for %s: %.2f%%\n", t.name, requestedOutput)
			rawValue := PercentToRaw(requestedOutput)
			log.Printf("Converted output value to raw: %d\n", rawValue)
			err := writeOutput(t.Address, rawValue)
			if err != nil {
				fmt.Printf("Error writing output: %v\n", err)
				return
			}
		}
	}
}

// readInput reads the input value from the piTest command line tool.
func readInputVoltage(varName string) (float64, error) {
	fmt.Println("Reading input:", varName)
	cmd := exec.Command("/usr/bin/piTest", "-1", "-q", "-r", varName)
	cmd.Stderr = os.Stderr
	reading, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("reading the Rev Pi failed: %w", err)
	}

	valueStr := strings.TrimSpace(string(reading))
	raw, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("invalid raw value: %w", err)
	}

	voltage := float64(raw) // the raw value is in millivolts, convert to volts
	return voltage, nil
}

// writeOutput writes the output value to the piTest command line tool.
func writeOutput(varName string, value int) error {
	fmt.Printf("Writing %d to %s\n", value, varName)
	cmd := exec.Command("/usr/bin/piTest", "-w", fmt.Sprintf("%s,%d", varName, value))
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PercentToRaw converts a percentage (0–100%) to a raw 16-bit value for the piTest tool.
func PercentToRaw(percent float64) int {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return int(percent * 100.0)
}

// NormalizeToPercent normalizes a reading to a percentage based on the provided min and max values.
func NormalizeToPercent(reading, min, max float64) float64 {
	percent := reading / 100

	// Clamp to [0, 100] in case reading is outside the expected range
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}
