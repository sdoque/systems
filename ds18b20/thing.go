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
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// Define the types of requests the measurement manager can handle
type STray struct {
	Action string
	ValueP chan forms.SignalA_v1a
	Error  chan error
}

//-------------------------------------Define the unit asset

// UnitAsset type models the unit asset (interface) of the system.
type UnitAsset struct {
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	temperature float64    `json:"-"`
	tStamp      time.Time  `json:"-"`
	trayChan    chan STray `json:"-"` // Add a channel for temperature readings
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetType() string {
	return ua.Type
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
	temperature := components.Service{
		Definition:  "temperature",
		SubPath:     "temperature",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the temperature (GET) of the resource temperature sensor",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "sensor_Id",
		Details: map[string][]string{"Unit": {"Celsius"}, "Location": {"Kitchen"}},
		ServicesMap: components.Services{
			temperature.SubPath: &temperature, // Inline assignment of the temperature service
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(uac UnitAsset, sys *components.System, servs []components.Service) (components.UnitAsset, func()) {
	ua := &UnitAsset{ // this a struct that implements the UnitAsset interface
		Name:        uac.Name,
		Type:        uac.Type,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: components.CloneServices(servs),
		trayChan:    make(chan STray), // Initialize the channel
	}

	// start the unit asset(s)
	go ua.readTemperature(sys.Ctx)

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
	}
}

//-------------------------------------Unit asset's functionalities

// readTemperature obtains the temperature from respective ds18b20 resource at regular intervals
func (ua *UnitAsset) readTemperature(ctx context.Context) {
	defer close(ua.trayChan) // Ensure the channel is closed when the goroutine exits

	// Create a ticker that triggers every 2 seconds
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop() // Clean up the ticker when done

	tempChan := make(chan float64) // Channel for latest temperature readings
	tStampChan := make(chan time.Time)

	// Start a separate goroutine for temperature reading
	go func() {
		for {
			select {
			case <-ctx.Done(): // Stop when the context is canceled
				return

			case <-ticker.C: // Read temperature at regular intervals
				deviceFile := "/sys/bus/w1/devices/" + ua.Name + "/w1_slave"
				rawData, err := os.ReadFile(deviceFile)
				if err != nil {
					log.Printf("Error reading temperature file: %s, error: %v\n", deviceFile, err)
					continue // Retry on the next cycle
				}

				if len(rawData) == 0 {
					log.Printf("Empty data read from temperature file: %s\n", deviceFile)
					continue
				}

				rawValue := strings.Split(string(rawData), "\n")[1]
				if !strings.Contains(rawValue, "t=") {
					log.Printf("Invalid temperature data: %s\n", rawData)
					continue
				}

				tempStr := strings.Split(rawValue, "t=")[1]
				temp, err := strconv.ParseFloat(tempStr, 64)
				if err != nil {
					log.Printf("Error parsing temperature: %v\n", err)
					continue
				}

				// Send the temperature and timestamp back to the main loop
				select {
				case tempChan <- temp / 1000.0:
					tStampChan <- time.Now()
				case <-ctx.Done(): // Stop the goroutine if context is canceled
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done(): // Shutdown
			log.Println("Context canceled, stopping temperature readings.")
			return

		case temp := <-tempChan: // Update temperature and timestamp
			ua.temperature = temp
			ua.tStamp = <-tStampChan

		case order := <-ua.trayChan: // Address a GET request
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = ua.temperature
			f.Unit = "Celsius"
			f.Timestamp = ua.tStamp
			order.ValueP <- f
		}
	}
}
