/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
 ***************************************************************************SDG*/

package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable and runtime parameters for one electrical heater thermostat.
type Traits struct {
	SetPt     float64       `json:"setPoint"`
	Period    time.Duration `json:"samplingPeriod"`
	Kp        float64       `json:"kp"`
	jitter    time.Duration
	deviation float64
	previousT float64
	name      string
	owner     *components.System
	cervices  components.Cervices
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used to seed systemconfig.json.
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
		Description: "provides the current difference between the setpoint and the temperature (GET)",
	}
	jitterService := components.Service{
		Definition:  "jitter",
		SubPath:     "jitter",
		Details:     map[string][]string{"Unit": {"millisecond"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the control loop execution jitter in milliseconds (GET)",
	}

	return &components.UnitAsset{
		Name:    "KitchenHeater",
		Mission: "electric_heating",
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			setPointService.SubPath:     &setPointService,
			thermalErrorService.SubPath: &thermalErrorService,
			jitterService.SubPath:       &jitterService,
		},
		Traits: &Traits{
			SetPt:  20,
			Period: 10,
			Kp:     5,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResources discovers all ZigBee heater plugs from beekeeper via the Orchestrator,
// matches each one to a temperature service from meteorologue, and returns one
// UnitAsset per heater with its own feedback control loop.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	// Read default parameters from the single config entry.
	var defaults Traits
	defaults.SetPt = 20
	defaults.Period = 10
	defaults.Kp = 5
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &defaults); err != nil {
			log.Println("ethermostat: warning — could not unmarshal traits:", err)
		}
	}
	if defaults.Period == 0 {
		defaults.Period = 10
	}
	if defaults.Kp == 0 {
		defaults.Kp = 5
	}

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)

	// Discover all OnOff services (beekeeper smart plugs).
	onOffCer := &components.Cervice{
		Definition: "OnOff",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}
	if err := usecases.Search4MultipleServices(onOffCer, sys); err != nil {
		log.Printf("ethermostat: could not discover OnOff services: %v\n", err)
	}

	// Discover all Temperature services (meteorologue modules).
	tempCer := &components.Cervice{
		Definition: "Temperature",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}
	if err := usecases.Search4MultipleServices(tempCer, sys); err != nil {
		log.Printf("ethermostat: could not discover Temperature services: %v\n", err)
	}

	var assets []*components.UnitAsset

	for sysNode, nodeList := range onOffCer.Nodes {
		for _, ni := range nodeList {
			displayNames := ni.Details["DisplayName"]
			if len(displayNames) == 0 {
				continue
			}
			displayName := displayNames[0]
			if !strings.HasSuffix(displayName, "Heater") {
				continue
			}

			location := extractLocation(displayName)

			// Dedicated on_off cervice pointing only at this plug's URL.
			heaterOnOff := &components.Cervice{
				Definition: "OnOff",
				Protos:     sProtocols,
				Nodes: map[string][]components.NodeInfo{
					sysNode: {ni},
				},
			}

			// Find the best-matching temperature cervice for this location.
			tempSysNode, tempNI, ok := selectTempNode(tempCer.Nodes, location)
			if !ok {
				log.Printf("ethermostat: no temperature service found for %s — skipping\n", displayName)
				continue
			}
			heaterTemp := &components.Cervice{
				Definition: "Temperature",
				Protos:     sProtocols,
				Nodes: map[string][]components.NodeInfo{
					tempSysNode: {tempNI},
				},
			}

			t := &Traits{
				SetPt:  defaults.SetPt,
				Period: defaults.Period,
				Kp:     defaults.Kp,
				name:   displayName,
				owner:  sys,
				cervices: components.Cervices{
					"on_off":      heaterOnOff,
					"temperature": heaterTemp,
				},
			}

			ua := buildHeaterAsset(displayName, location, t, sys, uac)
			assets = append(assets, ua)
			go t.feedbackLoop(sys.Ctx)
			log.Printf("ethermostat: created thermostat %q (location=%q, temp from %q)\n",
				displayName, location, tempSysNode)
		}
	}

	if len(assets) == 0 {
		log.Println("ethermostat: no heater plugs found — check beekeeper and Orchestrator configuration")
	}

	return assets, func() {
		log.Println("ethermostat: shutting down")
	}
}

// buildHeaterAsset creates a UnitAsset for one heater thermostat.
func buildHeaterAsset(name, location string, t *Traits, sys *components.System, uac usecases.ConfigurableAsset) *components.UnitAsset {
	ua := &components.UnitAsset{
		Name:        name,
		Mission:     "electric_heating",
		Owner:       sys,
		Details:     map[string][]string{"FunctionalLocation": {location}},
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: t.cervices,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

//-------------------------------------Helper functions for discovery

// extractLocation strips the "Heater" suffix to get the functional location prefix.
// E.g. "KitchenHeater" → "Kitchen", "DiningRoomHeater" → "DiningRoom".
func extractLocation(heaterName string) string {
	return strings.TrimSuffix(heaterName, "Heater")
}

// selectTempNode finds the best temperature NodeInfo for the given location.
// It prefers a node whose FunctionalLocation detail contains the location string.
// If none matches it falls back to the first available temperature node.
func selectTempNode(nodes map[string][]components.NodeInfo, location string) (string, components.NodeInfo, bool) {
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			for _, fl := range ni.Details["FunctionalLocation"] {
				if strings.Contains(fl, location) {
					return sysNode, ni, true
				}
			}
		}
	}
	// Fallback: first available temperature node.
	for sysNode, nodeList := range nodes {
		if len(nodeList) > 0 {
			return sysNode, nodeList[0], true
		}
	}
	return "", components.NodeInfo{}, false
}

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current thermal setpoint.
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.SetPt
	f.Unit = "Celsius"
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the thermal setpoint.
func (t *Traits) setSetPoint(f forms.SignalA_v1a) {
	t.SetPt = f.Value
	log.Printf("ethermostat %s: new setpoint %.1f °C\n", t.name, f.Value)
}

// getError fills out a signal form with the current thermal error.
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.deviation
	f.Unit = "Celsius"
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the control loop execution jitter.
func (t *Traits) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.jitter.Milliseconds())
	f.Unit = "millisecond"
	f.Timestamp = time.Now()
	return f
}

//-------------------------------------Feedback control loop

// feedbackLoop is the control goroutine for this heater thermostat.
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

// processFeedbackLoop reads the temperature, calculates the P-controller output,
// and turns the plug ON (output > 50) or OFF (output ≤ 50).
func (t *Traits) processFeedbackLoop() {
	jitterStart := time.Now()

	tf, err := usecases.GetState(t.cervices["temperature"], t.owner)
	if err != nil {
		log.Printf("ethermostat %s: unable to get temperature: %v\n", t.name, err)
		return
	}
	tup, ok := tf.(*forms.SignalA_v1a)
	if !ok {
		log.Printf("ethermostat %s: unexpected temperature form type\n", t.name)
		return
	}

	t.deviation = t.SetPt - tup.Value
	output := t.calculateOutput(t.deviation)
	plugOn := output > 50

	if tup.Value != t.previousT {
		state := "OFF"
		if plugOn {
			state = "ON"
		}
		log.Printf("ethermostat %s: temp=%.2f°C err=%.2f°C → plug %s\n",
			t.name, tup.Value, t.deviation, state)
		t.previousT = tup.Value
	}

	t.updatePlugState(plugOn)
	t.jitter = time.Since(jitterStart)
}

// calculateOutput is the P-controller: output = Kp × error + 50, clamped to [0, 100].
func (t *Traits) calculateOutput(thermDiff float64) float64 {
	v := t.Kp*thermDiff + 50
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// updatePlugState sends a SignalB_v1a PUT to the beekeeper on_off service.
func (t *Traits) updatePlugState(on bool) {
	var f forms.SignalB_v1a
	f.NewForm()
	f.Value = on
	f.Timestamp = time.Now()

	body, err := usecases.Pack(&f, "application/json")
	if err != nil {
		log.Printf("ethermostat %s: could not pack plug command: %v\n", t.name, err)
		return
	}
	if _, err := usecases.SetState(t.cervices["on_off"], t.owner, body); err != nil {
		log.Printf("ethermostat %s: could not set plug state: %v\n", t.name, err)
		t.cervices["on_off"].Nodes = make(map[string][]components.NodeInfo)
	}
}
