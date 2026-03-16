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
	"log"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	SetPt     float64       `json:"setPoint"`
	Period    time.Duration `json:"samplingPeriod"`
	Kp        float64       `json:"kp"`
	Lambda    float64       `json:"lambda"`
	Ki        float64       `json:"ki"`
	jitter    time.Duration `json:"-"`
	deviation float64       `json:"-"`
	previousT float64       `json:"-"`
	owner     *components.System  `json:"-"`
	cervices  components.Cervices `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	setPointService := components.Service{
		Definition:  "setpoint",
		SubPath:     "setpoint",
		Details:     map[string][]string{"Unit": {"Celsius"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		CUnit:       "Eur/h",
		Description: "provides the current thermal setpoint (GET) or sets it (PUT)",
	}
	thermalErrorService := components.Service{
		Definition:  "thermalerror",
		SubPath:     "thermalerror",
		Details:     map[string][]string{"Unit": {"Celsius"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the current difference between the set point and the temperature (GET)",
	}
	jitterService := components.Service{
		Definition:  "jitter",
		SubPath:     "jitter",
		Details:     map[string][]string{"Unit": {"millisecond"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the current jitter or control algorithm execution calculated every period (GET)",
	}

	return &components.UnitAsset{
		Name:    "controller_1",
		Mission: "control_heater",
		Details: map[string][]string{"Location": {"Kitchen"}},
		ServicesMap: components.Services{
			setPointService.SubPath:     &setPointService,
			thermalErrorService.SubPath: &thermalErrorService,
			jitterService.SubPath:       &jitterService,
		},
		Traits: &Traits{
			SetPt:  20,
			Period: 10,
			Kp:     5,
			Lambda: 0.5,
			Ki:     0,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	tempCervice := &components.Cervice{
		Definition: "temperature",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string),
	}
	rotCervice := &components.Cervice{
		Definition: "rotation",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string),
	}
	cervMap := components.Cervices{
		tempCervice.Definition: tempCervice,
		rotCervice.Definition:  rotCervice,
	}

	t := &Traits{
		owner:    sys,
		cervices: cervMap,
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
		CervicesMap: cervMap,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	ua.CervicesMap["temperature"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Celsius"}, "Forms": {"SignalA_v1a"}})
	ua.CervicesMap["rotation"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}})

	go t.feedbackLoop(sys.Ctx)

	return ua, func() {
		log.Println("Shutting down thermostat ", configuredAsset.Name)
	}
}

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current thermal setpoint
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.SetPt
	f.Unit = "Celsius"
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the thermal setpoint
func (t *Traits) setSetPoint(f forms.SignalA_v1a) {
	t.SetPt = f.Value
	log.Printf("new set point: %.1f", f.Value)
}

// getError fills out a signal form with the current thermal setpoint and temperature
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.deviation
	f.Unit = "Celsius"
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the current jitter
func (t *Traits) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.jitter.Milliseconds())
	f.Unit = "millisecond"
	f.Timestamp = time.Now()
	return f
}

// feedbackLoop is THE control loop (IPR of the system)
func (t *Traits) feedbackLoop(ctx context.Context) {
	ticker := time.NewTicker(t.Period * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.processFeedbackLoop()
		case <-ctx.Done():
			return
		}
	}
}

// processFeedbackLoop is called to execute the control process
func (t *Traits) processFeedbackLoop() {
	jitterStart := time.Now()

	tf, err := usecases.GetState(t.cervices["temperature"], t.owner)
	if err != nil {
		log.Printf("\n unable to obtain a temperature reading error: %s\n", err)
		t.updateValvePosition(50)
		return
	}
	tup, ok := tf.(*forms.SignalA_v1a)
	if !ok {
		log.Println("problem unpacking the temperature signal form")
		t.updateValvePosition(50)
		return
	}

	t.deviation = t.SetPt - tup.Value
	output := t.calculateOutput(t.deviation)

	if tup.Value != t.previousT {
		log.Printf("the temperature is %.2f °C with an error %.2f°C and valve set at %.2f%%\n", tup.Value, t.deviation, output)
		t.previousT = tup.Value
	}

	t.updateValvePosition(output)
	t.jitter = time.Since(jitterStart)
}

// calculateOutput is the actual P controller
func (t *Traits) calculateOutput(thermDiff float64) float64 {
	vPosition := t.Kp*thermDiff + 50

	if vPosition < 0 {
		vPosition = 0
	} else if vPosition > 100 {
		vPosition = 100
	}
	return vPosition
}

func (t *Traits) updateValvePosition(position float64) {
	var of forms.SignalA_v1a
	of.NewForm()
	of.Value = position
	of.Unit = t.cervices["rotation"].Details["Unit"][0]
	of.Timestamp = time.Now()

	op, err := usecases.Pack(&of, "application/json")
	if err != nil {
		return
	}
	_, err = usecases.SetState(t.cervices["rotation"], t.owner, op)
	_ = err
}
