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
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	SetPt     float64       `json:"setPoint"` // the set point for the level
	Period    time.Duration `json:"samplingPeriod"`
	Kp        float64       `json:"kp"`
	Lambda    float64       `json:"lambda"`
	Ki        float64       `json:"ki"`
	jitter    time.Duration
	deviation float64
	previousT float64
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
	setPointService := components.Service{
		Definition:  "setPoint",
		SubPath:     "setpoint",
		Details:     map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   100,
		CUnit:       "Eur/h",
		Description: "provides the current thermal setpoint (GET) or sets it (PUT)",
	}
	levelErrorService := components.Service{
		Definition:  "levelError",
		SubPath:     "levelerror",
		Details:     map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the current difference between the set point and the temperature (GET)",
	}
	jitterService := components.Service{
		Definition:  "jitter",
		SubPath:     "jitter",
		Details:     map[string][]string{"Unit": {"millisecond"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the current jitter or control algorithm execution calculated every period (GET)",
	}

	assetTraits := Traits{
		SetPt:  20,
		Period: 5,
		Kp:     5,
		Lambda: 0.5,
		Ki:     0,
	}
	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:    "Leveler_1",
		Details: map[string][]string{"Location": {"UpperTank"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			setPointService.SubPath:   &setPointService,
			levelErrorService.SubPath: &levelErrorService,
			jitterService.SubPath:     &jitterService,
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	// determine the protocols that the system supports
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	// instantiate the consumed services
	t := &components.Cervice{
		Definition: "level",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string, 0),
	}

	r := &components.Cervice{
		Definition: "pumpSpeed",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string, 0),
	}

	// instantiate the unit asset
	ua := &UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: components.Cervices{
			t.Definition: t,
			r.Definition: r,
		},
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	ua.CervicesMap["level"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}})
	ua.CervicesMap["pumpSpeed"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}})

	// start the unit asset(s)
	go ua.feedbackLoop(sys.Ctx)

	return ua, func() {
		log.Println("Shutting down thermostat ", ua.Name)
	}
}

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

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current level set point
func (ua *UnitAsset) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = ua.SetPt
	f.Unit = "millimeter"
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the level set point
func (ua *UnitAsset) setSetPoint(f forms.SignalA_v1a) {
	ua.SetPt = f.Value
	log.Printf("new set point: %.1f", f.Value)
}

// getErrror fills out a signal form with the current thermal setpoint and temperature
func (ua *UnitAsset) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = ua.deviation
	f.Unit = "millimeter"
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the current jitter
func (ua *UnitAsset) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(ua.jitter.Milliseconds())
	f.Unit = "millisecond"
	f.Timestamp = time.Now()
	return f
}

// feedbackLoop is THE control loop (IPR of the system)
func (ua *UnitAsset) feedbackLoop(ctx context.Context) {
	// Initialize a ticker for periodic execution
	ticker := time.NewTicker(ua.Period * time.Second)
	defer ticker.Stop()

	// start the control loop
	for {
		select {
		case <-ticker.C:
			ua.processFeedbackLoop()
		case <-ctx.Done():
			return
		}
	}
}

// processFeedbackLoop is called to execute the control process
func (ua *UnitAsset) processFeedbackLoop() {
	jitterStart := time.Now()

	// get the current level
	tf, err := usecases.GetState(ua.CervicesMap["level"], ua.Owner)
	if err != nil {
		log.Printf("\n unable to obtain a level reading error: %s\n", err)
		return
	}
	// Perform a type assertion to convert the returned Form to SignalA_v1a
	tup, ok := tf.(*forms.SignalA_v1a)
	if !ok {
		log.Println("problem unpacking the level signal form")
		return
	}

	// perform the control algorithm
	ua.deviation = ua.SetPt - tup.Value
	output := ua.calculateOutput(ua.deviation)

	// prepare the form to send
	var of forms.SignalA_v1a
	of.NewForm()
	of.Value = output
	of.Unit = ua.CervicesMap["pumpSpeed"].Details["Unit"][0]
	of.Timestamp = time.Now()

	// pack the new valve state form
	op, err := usecases.Pack(&of, "application/json")
	if err != nil {
		return
	}
	// send the new valve state request
	_, err = usecases.SetState(ua.CervicesMap["pumpSpeed"], ua.Owner, op)
	if err != nil {
		log.Printf("cannot update valve state: %s\n", err)
		return
	}

	if tup.Value != ua.previousT {
		log.Printf("the level is %.2f mm with an error %.2f°mm and the pumpSpeed set at %.2f%%\n", tup.Value, ua.deviation, output)
		ua.previousT = tup.Value
	}

	ua.jitter = time.Since(jitterStart)
}

// calculateOutput is the actual P controller
func (ua *UnitAsset) calculateOutput(thermDiff float64) float64 {
	pSpeed := ua.Kp*thermDiff + 50 // if the error is 0, the position is at 50%

	// limit the output between 0 and 100%
	if pSpeed < 0 {
		pSpeed = 0
	} else if pSpeed > 100 {
		pSpeed = 100
	}
	return pSpeed
}
