/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"math"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	SetPt         float64       `json:"setPoint"`
	Period        time.Duration `json:"samplingPeriod"`
	Kp            float64       `json:"kp"`
	Lambda        float64       `json:"lambda"`
	Ki            float64       `json:"ki"`
	jitter        time.Duration
	deviation     float64
	integral      float64
	previousLevel float64
	owner         *components.System  `json:"-"`
	cervices      components.Cervices `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
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

	return &components.UnitAsset{
		Name:    "Leveler_1",
		Mission: "control_level",
		Details: map[string][]string{"Location": {"UpperTank"}},
		ServicesMap: components.Services{
			setPointService.SubPath:   &setPointService,
			levelErrorService.SubPath: &levelErrorService,
			jitterService.SubPath:     &jitterService,
		},
		Traits: &Traits{
			SetPt:  20,
			Period: 5,
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
	levelCervice := &components.Cervice{
		Definition: "level",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string),
	}
	pumpCervice := &components.Cervice{
		Definition: "pumpSpeed",
		Protos:     sProtocols,
		Nodes:      make(map[string][]string),
	}
	cervMap := components.Cervices{
		levelCervice.Definition: levelCervice,
		pumpCervice.Definition:  pumpCervice,
	}

	t := &Traits{
		owner:    sys,
		cervices: cervMap,
	}

	for _, raw := range configuredAsset.Traits {
		if err := json.Unmarshal(raw, t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
		break
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

	ua.CervicesMap["level"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}, "Location": {"UpperTank"}})
	ua.CervicesMap["pumpSpeed"].Details = components.MergeDetails(ua.Details, map[string][]string{"Unit": {"Percent"}, "Forms": {"SignalA_v1a"}})

	go t.feedbackLoop(sys.Ctx)

	return ua, func() {
		log.Println("Shutting down leveler ", configuredAsset.Name)
	}
}

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current level set point
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.SetPt
	f.Unit = "Percent"
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the level set point
func (t *Traits) setSetPoint(f forms.SignalA_v1a) {
	t.SetPt = f.Value
	log.Printf("new set point: %.1f", f.Value)
}

// getError fills out a signal form with the current level error
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.deviation
	f.Unit = "Percent"
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

// feedbackLoop is THE control loop
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

	tf, err := usecases.GetState(t.cervices["level"], t.owner)
	if err != nil {
		log.Printf("\n unable to obtain a level reading error: %s\n", err)
		return
	}
	tup, ok := tf.(*forms.SignalA_v1a)
	if !ok {
		log.Println("problem unpacking the level signal form")
		return
	}

	t.deviation = t.SetPt - tup.Value
	output := t.calculateOutput(t.deviation)

	var of forms.SignalA_v1a
	of.NewForm()
	of.Value = output
	of.Unit = t.cervices["pumpSpeed"].Details["Unit"][0]
	of.Timestamp = time.Now()

	op, err := usecases.Pack(&of, "application/json")
	if err != nil {
		return
	}
	_, err = usecases.SetState(t.cervices["pumpSpeed"], t.owner, op)
	if err != nil {
		log.Printf("cannot update pump speed: %s\n", err)
		return
	}

	if tup.Value != t.previousLevel {
		log.Printf("the level is %.2f percent with an error %.2f percent and the pumpSpeed set at %.2f%%\n", tup.Value, t.deviation, output)
		t.previousLevel = tup.Value
	}

	t.jitter = time.Since(jitterStart)
}

// calculateOutput is the actual PI controller
func (t *Traits) calculateOutput(levelDiff float64) float64 {
	pTerm := t.Kp * levelDiff

	sampleSeconds := (t.Period * time.Second).Seconds()
	decay := math.Exp(-sampleSeconds / t.Lambda)
	t.integral = decay*t.integral + levelDiff*sampleSeconds

	iTerm := t.Ki * t.integral

	output := pTerm + iTerm

	if output < 0 {
		output = 0
	} else if output > 100 {
		output = 100
	}
	return output
}
