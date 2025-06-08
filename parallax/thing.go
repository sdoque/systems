/*******************************************************************************
 * Copyright (c) 2024 Jan van Deventer
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
	"encoding/json"
	"fmt"
	"log"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/host/v3"
	"periph.io/x/host/v3/rpi"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters
type Traits struct {
	GpioPin  gpio.PinIO `json:"-"`
	position int        `json:"-"`
	dutyChan chan int   `json:"-"`
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
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

// ensure UnitAsset implements components.UnitAsset
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	rotation := components.Service{
		Definition:  "rotation",
		SubPath:     "rotation",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}, "Unit": {"Percent", "Rotational"}},
		RegPeriod:   30,
		Description: "informs of the servo's current position (GET) or updates the position (PUT)",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "Servo_1",
		Details: map[string][]string{"Model": {"standard servo", "half_circle"}, "Location": {"Kitchen"}},
		ServicesMap: components.Services{
			rotation.SubPath: &rotation, // Inline assignment of the rotation service
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConfig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	// ua components.UnitAsset is an interface, which is implemented and initialized
	ua := &UnitAsset{
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

	ua.Traits.dutyChan = make(chan int)

	// Initialize the periph.io host
	if _, err := host.Init(); err != nil {
		log.Fatalf("Failed to initialize periph: %v\n", err)
		return ua, func() {}
	}

	// Access GPIO pin 18 (Pin 12 on Raspberry Pi header)
	ua.GpioPin = rpi.P1_12
	ua.GpioPin.Out(gpio.Low)

	// Initialize with a neutral position (90°)
	setServoDutyCycle(ua.GpioPin, 1520) // Set 1520 µs for neutral (90°)

	// Start the unit asset(s)
	go func() {
		for pulseWidth := range ua.dutyChan {
			fmt.Printf("Pulse width updated: %v µs\n", pulseWidth)
			setServoDutyCycle(ua.GpioPin, pulseWidth) // Adjusting to the new pulse width
		}
	}()

	return ua, func() {
		log.Println("disconnecting from servos")
		ua.GpioPin.Out(gpio.Low)
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

// timing constants for the PWM (pulse width modulation)
// pulse widths:(620 µs, 1520 µs, 2420 µs) maps to (0°, 90°, 180°) with angles increasing from clockwise to counterclockwise
const (
	minPulseWidth    = 620
	centerPulseWidth = 1520
	maxPulseWidth    = 2420
)

// getPosition provides an analog signal for the servo position in percent and a timestamp
func (ua *UnitAsset) getPosition() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(ua.position)
	f.Unit = "Percent"
	f.Timestamp = time.Now()
	return f
}

// setPosition updates the PWM pulse size based on the requested position [0-100]%
func (ua *UnitAsset) setPosition(f forms.SignalA_v1a) {
	if ua.position != int(f.Value) {
		log.Printf("The new position is %+v\n", f)
	}

	// Limit the value directly within the assignment to rsc.position
	position := int(f.Value)
	if position < 0 {
		position = 0
	} else if position > 100 {
		position = 100
	}
	ua.position = position // Position is now guaranteed to be in the 0-100% range

	// Calculate the width based on the position, scaled to pulse width range
	width := (ua.position * (maxPulseWidth - minPulseWidth) / 100) + minPulseWidth

	// Send the calculated width to the duty cycle channel
	ua.dutyChan <- width
}

// setServoDutyCycle sets the duty cycle on the given GPIO pin using the pulse width in microseconds.
func setServoDutyCycle(pin gpio.PinIO, pulseWidth int) {
	// Calculate the time duration for the pulse width
	onDuration := time.Duration(pulseWidth) * time.Microsecond
	offDuration := time.Duration(20000-pulseWidth) * time.Microsecond // 20ms period minus the pulse width

	// Set pin high for pulse width duration
	pin.Out(gpio.High)
	time.Sleep(onDuration)

	// Set pin low for the rest of the period
	pin.Out(gpio.Low)
	time.Sleep(offDuration)
}
