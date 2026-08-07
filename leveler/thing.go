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
	"fmt"
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
	// Units the payloads report in, taken from the configured services so a
	// reading and the record that describes it cannot disagree. errorUnit is
	// not configured: it is the setpoint's.
	setpointUnit string `json:"-"`
	errorUnit    string `json:"-"`
	jitterUnit   string `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	setPointService := components.Service{
		Definition:  "setpoint",
		SubPath:     "setpoint",
		Mission:     components.MissionState,
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/PERCENT>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   100,
		CUnit:       "Eur/h",
		Description: "provides the current thermal setpoint (GET) or sets it (PUT)",
	}
	deviationService := components.Service{
		Definition: "deviation",
		SubPath:    "deviation",
		Mission:    components.MissionMeasurement,
		// No Unit: the deviation is the difference between the setpoint
		// and the measurement, so it is in the setpoint's unit. adoptUnits
		// copies it, and the two cannot drift apart.
		Details:     map[string][]string{"QuantityKind": {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"}, "Measure": {"interval"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the current difference between the set point and the temperature (GET)",
	}
	jitterService := components.Service{
		Definition:  "jitter",
		SubPath:     "jitter",
		Mission:     components.MissionMeasurement,
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/MilliSEC>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/Time>"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the current jitter or control algorithm execution calculated every period (GET)",
	}

	return &components.UnitAsset{
		Name:    "Leveler_1",
		Mission: components.MissionControl,
		Details: map[string][]string{"FunctionalLocation": {"UpperTank"}},
		ServicesMap: components.Services{
			setPointService.SubPath:  &setPointService,
			deviationService.SubPath: &deviationService,
			jitterService.SubPath:    &jitterService,
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
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "get",
	}
	pumpCervice := &components.Cervice{
		Definition: "pumpSpeed",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "set",
	}
	cervMap := components.Cervices{
		levelCervice.Definition: levelCervice,
		pumpCervice.Definition:  pumpCervice,
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
	t.adoptUnits(ua.ServicesMap)

	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// The level is consumed by quantity kind, not by unit: a gauge reporting a
	// fraction rather than a percentage is still the right sensor, and the
	// conversion is the framework's job.
	//
	// The unit is the setpoint's rather than a constant, because the loop
	// subtracts the measurement from the setpoint. Two units there and the
	// deviation is arithmetic on unlike quantities, which still yields a number
	// and still drives the pump.
	if _, ok := usecases.LookupUnit(t.setpointUnit); !ok {
		log.Fatalf("leveler: the setpoint is configured in %q, which is not a QUDT unit this framework can convert a measurement into\n", t.setpointUnit)
	}
	ua.CervicesMap["level"].Details = components.MergeDetails(ua.Details, map[string][]string{
		"QuantityKind":       {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"},
		"Unit":               {t.setpointUnit},
		"Forms":              {"SignalA_v1a"},
		"FunctionalLocation": {"UpperTank"},
	})
	ua.CervicesMap["pumpSpeed"].Details = components.MergeDetails(ua.Details, map[string][]string{
		"QuantityKind": {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"},
		"Unit":         {"<http://qudt.org/vocab/unit/PERCENT>"},
		"Forms":        {"SignalA_v1a"},
	})

	go t.feedbackLoop(sys.Ctx)

	return ua, func() {
		log.Println("Shutting down leveler ", configuredAsset.Name)
	}
}

//-------------------------------------Service handlers

func (t *Traits) setpt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		setPointForm := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &setPointForm)
	case "PUT":
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "unreadable setpoint: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := t.setSetPoint(sig); err != nil {
			// Refusing is the only safe answer: a level target in an unexpected
			// unit is a number that will drive the pump for as long as nobody
			// notices it looks reasonable.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		confirmed := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &confirmed)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) diff(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		signalErr := t.getError()
		usecases.HTTPProcessGetRequest(w, r, &signalErr)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) variations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		signalErr := t.getJitter()
		usecases.HTTPProcessGetRequest(w, r, &signalErr)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current level set point
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.SetPt
	f.Unit = t.setpointUnit
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the level set point
func (t *Traits) setSetPoint(f forms.SignalA_v1a) error {
	// The value arrives in whatever unit the sender works in. Writing it
	// straight into the loop is how a fraction becomes a percentage: 0.8 of a
	// tank read as 0.8% empties it, and every reported figure looks plausible.
	if err := usecases.AdoptUnit(&f, t.setpointUnit, false); err != nil {
		return fmt.Errorf("setpoint refused: %w", err)
	}
	t.SetPt = f.Value
	log.Printf("new set point: %.1f", f.Value)
	return nil
}

// getError fills out a signal form with the current level error
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = t.deviation
	f.Unit = t.errorUnit
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the current jitter
func (t *Traits) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.jitter.Milliseconds())
	f.Unit = t.jitterUnit
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

// adoptUnits takes the units this controller reports in from its configured
// services, and gives the deviation the setpoint's.
//
// The deviation is the difference between the setpoint and the measurement, so
// it is in the setpoint's unit by construction rather than by convention.
// Configuring it separately would let the two disagree, and a controller
// reporting a deviation in one unit against a setpoint in another would look
// plausible for a long time.
//
// The unit is used as configured, whatever it says. A pre-QUDT deployment keeps
// working; a QUDT one gets an IRI a consumer can convert from.
func (t *Traits) adoptUnits(services components.Services) {
	setpoint := findService(services, "setpoint")
	deviation := findService(services, "deviation")
	jitter := findService(services, "jitter")

	if setpoint != nil {
		t.setpointUnit = firstDetail(setpoint.Details, "Unit")
	}
	if jitter != nil {
		t.jitterUnit = firstDetail(jitter.Details, "Unit")
	}

	t.errorUnit = t.setpointUnit
	if deviation != nil {
		// Advertise it too: a consumer converts using the unit in the service
		// record, so leaving that blank would leave the reading unusable.
		if deviation.Details == nil {
			deviation.Details = make(map[string][]string)
		}
		if t.errorUnit != "" {
			deviation.Details["Unit"] = []string{t.errorUnit}
		}
	}
}

// findService looks a service up by definition, since the subpath an operator
// configures need not be the definition the code knows it by.
func findService(services components.Services, definition string) *components.Service {
	for _, s := range services {
		if s.Definition == definition {
			return s
		}
	}
	return nil
}

// firstDetail returns the first value recorded under a detail key.
func firstDetail(details map[string][]string, key string) string {
	if values := details[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}
