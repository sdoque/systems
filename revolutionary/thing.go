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
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// Define the types of requests the measurement manager can handle
type STray struct {
	Action string
	Sample chan forms.SignalA_v1a
	Error  chan error
}

//-------------------------------------Define the unit asset

// UnitAsset type models the unit asset (interface) of the system.
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	value      float64    `json:"-"`
	tStamp     time.Time  `json:"-"`
	sampleChan chan STray `json:"-"` // Add a channel for signal reading
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

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	access := components.Service{
		Definition:  "access",
		SubPath:     "access",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "reads the input (GET) or chandes the outup (POST) of the channel",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "InputValue_1",
		Details: map[string][]string{"Unit": {"Volts"}, "Location": {"UpperTank"}},
		ServicesMap: components.Services{
			access.SubPath: &access, // add the service to the map
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(uac UnitAsset, sys *components.System, servs []components.Service) (components.UnitAsset, func()) {
	ua := &UnitAsset{ // this a struct that implements the UnitAsset interface
		Name:        uac.Name,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: components.CloneServices(servs),
		sampleChan:  make(chan STray), // Initialize the channel
	}

	// start the unit asset(s)
	go ua.sampleSignal(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
	}
}

//-------------------------------------Unit asset's functionalities

// sampleSignal obtains the temperature from respective Rev Pi AIO resource at regular intervals
func (ua *UnitAsset) sampleSignal(ctx context.Context) {
	defer close(ua.sampleChan) // Ensure the channel is closed when the goroutine exits

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
				v, err := readInputVoltage(ua.Name)
				if err != nil {
					fmt.Println("Read error:", err)
				} else {
					fmt.Printf("%s = %.2f V\n", ua.Name, v)
				}

				// Send the sampeled signal and timestamp back to the main loop
				select {
				case sigChan <- v:
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
			ua.value = sigValue
			ua.tStamp = <-tStampChan

		case order := <-ua.sampleChan:
			switch order.Action {
			case "read":
				// Send the latest signal value and timestamp to the channel
				var f forms.SignalA_v1a
				f.NewForm()
				f.Value = ua.value
				f.Unit = "Volts"
				f.Timestamp = ua.tStamp
				order.Sample <- f
			case "write":
				// Receive the form from the channel
				f := <-order.Sample
				ua.value = f.Value
				rawValue := voltageToRaw(ua.value)
				err := writeOutput(ua.Name, rawValue)
				if err != nil {
					order.Error <- fmt.Errorf("write error: %w", err)
				}
			default:
				order.Error <- fmt.Errorf("invalid action: %s", order.Action)
			}
		}
	}
}

// readInput reads the input value from the piTest command line tool.
func readInputVoltage(varName string) (float64, error) {
	fmt.Println("Reading input:", varName)
	cmd := exec.Command("/usr/bin/piTest", "-1", "-q", "-r", varName)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("reading the Rev Pi failed: %w", err)
	}

	valueStr := strings.TrimSpace(string(output))
	raw, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("invalid raw value: %w", err)
	}

	voltage := (float64(raw)/65535.0)*20.0 - 10.0
	return voltage, nil
}

// writeOutput writes the output value to the piTest command line tool.
func writeOutput(varName string, value int) error {
	fmt.Printf("Writing %d to %s\n", value, varName)
	cmd := exec.Command("/usr/bin/piTest", "-w", fmt.Sprintf("%s,%d", varName, value))
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// voltageToRaw converts a voltage value to a raw value for the piTest command line tool.
func voltageToRaw(voltage float64) int {
	if voltage < 0 {
		voltage = 0
	}
	if voltage > 10 {
		voltage = 10
	}
	return int((voltage / 10.0) * 65535.0)
}
