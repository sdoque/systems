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
	"log"
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
	Address  string  `json:"address"`  // Address of the IO
	Value    float64 `json:"value"`    // Start up value of the IO
	MinValue float64 `json:"minValue"` // Minimum value of the IO
	MaxValue float64 `json:"maxValue"` // Maximum value of the IO
}

// UnitAsset type models the unit asset (interface) of the system.
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	// Asset-specific parameters
	Traits
	tStamp         time.Time        `json:"-"`
	serviceChannel chan ServiceTray `json:"-"` // Add a channel for signal reading
	outputChannel  chan float64     `json:"-"` // Channel for output signals
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
	access := components.Service{
		Definition:  "level",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "reads the input (GET) or changes the output (POST) of the channel",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "LevelSensor_1",
		Details: map[string][]string{"Unit": {"Percent"}, "Location": {"UpperTank"}, "Description": {"level"}},
		Traits: Traits{
			Address: "InputValue_1", // Default address for the Rev Pi AIO channel
			Value:   0.0,            // Default value for the output
		},
		ServicesMap: components.Services{
			access.SubPath: &access, // add the service to the map
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	ua := &UnitAsset{ // this a struct that implements the UnitAsset interface
		Name:           configuredAsset.Name,
		Owner:          sys,
		Details:        configuredAsset.Details,
		ServicesMap:    usecases.MakeServiceMap(configuredAsset.Services),
		serviceChannel: make(chan ServiceTray), // Initialize the channel
		outputChannel:  make(chan float64),     // Initialize the output channel
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	// start the unit asset(s)
	go ua.sampleSignal(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
		// close(ua.outputChannel)  // Ensure the output channel is closed when the goroutine exits
		// close(ua.serviceChannel) // Ensure the channel is closed when the goroutine exits
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

// sampleSignal obtains the temperature from respective Rev Pi AIO resource at regular intervals
func (ua *UnitAsset) sampleSignal(ctx context.Context) {
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
				v, err := readInputVoltage(ua.Address)
				if err != nil {
					fmt.Println("Read error:", err)
				} else {
					fmt.Printf("%s = %.2f V\n", ua.Name, v/1000)
				}
				nv := NormalizeToPercent(v, ua.MinValue, ua.MaxValue) // Normalize the value to a percentage

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
			ua.Value = sigValue
			ua.tStamp = <-tStampChan
		case order := <-ua.serviceChannel:
			// switch order.Action {
			// case "read":
			// Send the latest signal value and timestamp to the channel
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = ua.Value
			f.Unit = "Percent"
			f.Timestamp = ua.tStamp
			order.SampledDatum <- f
		case requestedOutup := <-ua.outputChannel:
			log.Printf("Received output request for %s: %.2f%%\n", ua.Name, requestedOutup)
			rawValue := PercentToRaw(requestedOutup)
			log.Printf("Converted output value to raw: %d\n", rawValue)
			err := writeOutput(ua.Address, rawValue)
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
	// if max == min {
	// 	return 0 // or return NaN/error to avoid division by zero
	// }
	percent := reading / 100 //* (reading - min) / (max - min)

	// Clamp to [0, 100] in case reading is outside the expected range
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}
