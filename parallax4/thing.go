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
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/stianeikeland/go-rpio/v4"
)

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	GpioPin  int      `json:"gpiopin"`
	rPi_Pin  rpio.Pin `json:"-"`
	position int      `json:"-"`
	dutyChan chan int  `json:"-"`
}

// assetConfig holds the JSON-configurable fields read from systemconfig.json
type assetConfig struct {
	Name    string              `json:"name"`
	Details map[string][]string `json:"details"`
	GpioPin int                 `json:"gpiopin"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	rotation := components.Service{
		Definition:  "rotation",
		SubPath:     "rotation",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}, "Unit": {"percent", "rotational"}},
		RegPeriod:   30,
		Description: "informs of the servo's current postion (GET) or updates the position (PUT)",
	}

	return &components.UnitAsset{
		Name:    "Servo_1",
		Details: map[string][]string{"Model": {"standard servo", "-90 to +90 degrees"}, "FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			rotation.SubPath: &rotation,
		},
		Traits: &Traits{GpioPin: 18},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(cfg assetConfig, sys *components.System, servs []components.Service) (*components.UnitAsset, func()) {
	t := &Traits{
		GpioPin:  cfg.GpioPin,
		dutyChan: make(chan int),
	}
	ua := &components.UnitAsset{
		Name:        cfg.Name,
		Owner:       sys,
		Details:     cfg.Details,
		ServicesMap: components.CloneServices(servs),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// Initialize the GPIO pin
	if err := rpio.Open(); err != nil {
		log.Fatalf("Failed to open GPIO %s\n", err)
		return ua, func() {}
	}
	t.rPi_Pin = rpio.Pin(t.GpioPin)
	t.rPi_Pin.Output()
	t.rPi_Pin.Mode(rpio.Pwm)
	t.rPi_Pin.Freq(1_000_000)        // µs in one s
	t.rPi_Pin.DutyCycle(620, 20_000) // 0°

	// start the unit asset(s)
	go func() {
		for pulseWidth := range t.dutyChan {
			fmt.Printf("Pulse width updated: %v\n", pulseWidth)
			t.rPi_Pin.DutyCycle(uint32(pulseWidth), 20_000) // Adjusting to the new pulse width
		}
	}()

	return ua, func() {
		log.Println("disconnecting from servos")
		rpio.Close()
	}
}

//-------------------------------------Unit asset's resource functions

// timing constants for the PWM (pulse width modulation)
// pulse widths:(620 µs, 1520 µs, 2420 µs) maps to (0°, 90°, 180°) with angles increasing from clockwise to counterclockwise
const (
	minPulseWidth    = 620
	centerPulseWidth = 1520
	maxPulseWidth    = 2420
)

// getPosition provides an analog signal form fit the srevo position in percent and a timestamp
func (t *Traits) getPosition() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.position)
	f.Unit = "percent"
	f.Timestamp = time.Now()
	return f
}

// setPosition update the PWM pulse size based on the requested position [0-100]%
func (t *Traits) setPosition(f forms.SignalA_v1a) {
	if t.position != int(f.Value) {
		log.Printf("The new position is %+v\n", f)
	}

	// Limit the value directly within the assignment to rsc.position
	position := int(f.Value)
	if position < 0 {
		position = 0
	} else if position > 100 {
		position = 100
	}
	t.position = position // Position is now guaranteed to be in the 0-100 % range

	// Calculate the width based on the position, scaled to pulse width range
	width := (t.position * (maxPulseWidth - minPulseWidth) / 100) + minPulseWidth

	// Send the calculated width to the duty cycle channel
	t.dutyChan <- width
}
