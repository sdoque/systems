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
	"net/http"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	// SetPt is written by the PUT handler on a net/http goroutine and read by
	// the control loop; deviation and jitter are written by the loop and read by
	// the diff and variations handlers. These run on Raspberry Pis, where a
	// 32-bit build stores a float64 in two words — a setpoint moving 20 to 22
	// while the loop reads it can yield a number that was never written, and
	// calculateOutput turns that into a fully open or fully closed valve.
	mu    sync.RWMutex
	SetPt float64 `json:"setPoint"`
	// Period is the sampling period in seconds.
	//
	// An int rather than a time.Duration, because a Duration holding the number
	// 10 means ten nanoseconds and only becomes ten seconds when multiplied by
	// time.Second — which works, and reads as if the field were already a
	// duration. Anyone writing the obvious `Period: time.Second` for one second
	// would get 10^9 seconds, about 31 years, and the compiler would not object.
	// The unit belongs in the name and the conversion belongs at the point of
	// use.
	Period    int                 `json:"samplingPeriod"`
	Kp        float64             `json:"kp"`
	Lambda    float64             `json:"lambda"`
	Ki        float64             `json:"ki"`
	jitter    time.Duration       `json:"-"`
	deviation float64             `json:"-"`
	previousT float64             `json:"-"`
	owner     *components.System  `json:"-"`
	cervices  components.Cervices `json:"-"`
	// Units reported in the payloads, taken from the configured services so the
	// value a consumer receives and the unit registered for it are the same
	// string. errorUnit is not configured: it is the setpoint's.
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
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/DEG_C>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "Forms": {"SignalA_v1a"}, "Methods": components.HTTPMethods("GET", "PUT")},
		RegPeriod:   120,
		CUnit:       "Eur/h",
		Description: "provides the current thermal setpoint (GET) or sets it (PUT)",
	}
	deviationService := components.Service{
		Mission:    components.MissionMeasurement,
		Definition: "deviation",
		SubPath:    "deviation",
		// No Unit here on purpose: an error is the difference between the setpoint
		// and the measurement, so it is in whatever unit the setpoint is in.
		// newResource copies it, and the two cannot drift apart.
		Details:     map[string][]string{"QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "Measure": {"interval"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
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
		Name:    "controller_1",
		Mission: components.MissionControl,
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}, "Mobility": {components.MobilityMovable}},
		ServicesMap: components.Services{
			setPointService.SubPath:  &setPointService,
			deviationService.SubPath: &deviationService,
			jitterService.SubPath:    &jitterService,
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
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "get",
	}
	rotCervice := &components.Cervice{
		Definition: "rotation",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "set",
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
	t.adoptUnits(ua.ServicesMap)

	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// The thermostat consumes a temperature, not a Celsius reading: the quantity
	// kind is what the registrar matches on, so a Fahrenheit sensor is found too.
	//
	// The unit is the setpoint's, not a constant. The control loop subtracts the
	// measurement from the setpoint, so the two must be in one unit or the
	// deviation is meaningless — commission the setpoint in °F against a
	// hardcoded °C measurement and 68 minus 20 is 48, the valve saturates, and
	// every reported figure looks plausible.
	if _, ok := usecases.LookupUnit(t.setpointUnit); !ok {
		log.Fatalf("thermostat: the setpoint is configured in %q, which is not a QUDT unit this framework can convert a measurement into. Write an identifier such as <http://qudt.org/vocab/unit/DEG_C> in the setpoint service's details.\n", t.setpointUnit)
	}
	ua.CervicesMap["temperature"].Details = components.MergeDetails(ua.Details, map[string][]string{
		"QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"},
		"Unit":         {t.setpointUnit},
		"Forms":        {"SignalA_v1a"},
	})
	// As with the temperature: the valve is asked for by what it is, not by the
	// unit it speaks, so a servo advertising a QUDT identifier is still found.
	ua.CervicesMap["rotation"].Details = components.MergeDetails(ua.Details, map[string][]string{
		"QuantityKind": {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"},
		"Unit":         {"<http://qudt.org/vocab/unit/PERCENT>"},
		"Forms":        {"SignalA_v1a"},
	})

	go t.feedbackLoop(sys.Ctx)

	return ua, func() {
		log.Println("Shutting down thermostat ", configuredAsset.Name)
	}
}

//-------------------------------------Service handlers

// setpt handles the get and set requests for the thermostat set point
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
			// Refusing is the only safe answer: a setpoint in an unexpected unit
			// is a number that will drive the valve for as long as nobody
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

// diff handles the get requests for the thermostat error signal
func (t *Traits) diff(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		signalErr := t.getError()
		usecases.HTTPProcessGetRequest(w, r, &signalErr)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// variations handles the get requests for the thermostat jitter signal
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

// getSetPoint fills out a signal form with the current thermal setpoint
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	f.Value = t.SetPt
	t.mu.RUnlock()
	f.Unit = usecases.UnitIRI(t.setpointUnit)
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the thermal setpoint
func (t *Traits) setSetPoint(f forms.SignalA_v1a) error {
	// The value arrives in whatever unit the sender works in. Writing it
	// straight into the loop is how a Fahrenheit target silently becomes a
	// Celsius one — the one write path that moves a control target was the only
	// place with no unit discipline.
	if err := usecases.AdoptUnit(&f, t.setpointUnit, false); err != nil {
		return fmt.Errorf("setpoint refused: %w", err)
	}
	t.mu.Lock()
	t.SetPt = f.Value
	t.mu.Unlock()
	log.Printf("new set point: %.1f", f.Value)
	return nil
}

// getError fills out a signal form with the current thermal setpoint and temperature
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	f.Value = t.deviation
	t.mu.RUnlock()
	f.Unit = usecases.UnitIRI(t.errorUnit)
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the current jitter
func (t *Traits) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	// Microseconds divided out rather than Milliseconds, which truncates.
	//
	// This measured whole milliseconds from when every cycle made an HTTP
	// request for its reading. A followed value is answered from cache, so the
	// loop now runs in well under a millisecond and the service reported 0 —
	// the metric losing its resolution to the improvement it was watching. The
	// unit is unchanged, so nothing consuming it needs to know.
	f.Value = float64(t.jitter.Microseconds()) / 1000
	t.mu.RUnlock()
	f.Unit = usecases.UnitIRI(t.jitterUnit)
	f.Timestamp = time.Now()
	return f
}

// feedbackLoop is THE control loop (IPR of the system)
//
// It runs on its own clock and also whenever a fresh temperature arrives. Both,
// deliberately: the ticker is what guarantees the loop runs at all — a provider
// that has died says nothing, and a controller waiting only for news would wait
// for ever — while the arrival is what makes it act *now* rather than at the end
// of a period that has just begun.
//
// Without the second arm, following a temperature only made the reading cheaper
// to fetch. The valve still moved on the period, so a sensor plunged into warm
// water took a full cycle to reach it, and an update nobody acted on bought
// only network traffic. How often an arrival may wake this is the cervice's own
// business (components.DefaultWakeFloor): a tenth of a degree is worth
// reporting and not worth moving a valve for.
func (t *Traits) feedbackLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(t.Period) * time.Second)
	defer ticker.Stop()

	fresh := t.cervices["temperature"].Updated()

	for {
		select {
		case <-ticker.C:
			t.processFeedbackLoop()
		case <-fresh:
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

	t.mu.Lock()
	t.deviation = t.SetPt - tup.Value
	deviation := t.deviation
	t.mu.Unlock()

	output := t.calculateOutput(deviation)

	if tup.Value != t.previousT {
		log.Printf("the temperature is %.2f %s with an error %.2f %s and valve set at %.2f%%\n",
			tup.Value, t.setpointUnit, deviation, t.errorUnit, output)
		t.previousT = tup.Value
	}

	t.updateValvePosition(output)

	t.mu.Lock()
	t.jitter = time.Since(jitterStart)
	t.mu.Unlock()
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
	of.Unit = usecases.UnitIRI(t.cervices["rotation"].Details["Unit"][0])
	of.Timestamp = time.Now()

	op, err := usecases.Pack(&of, "application/json")
	if err != nil {
		log.Printf("cannot pack the valve position: %s\n", err)
		return
	}
	// Reported, not discarded. With the servo unreachable, SetState resets the
	// node cache and returns an error; saying nothing about it left the loop
	// printing "valve set at 100.00%%" every ten seconds while nothing had been
	// set at all — and made the discovery half of the same fault, which this
	// controller was also carrying, impossible to notice from the log.
	if _, err := usecases.SetState(t.cervices["rotation"], t.owner, op); err != nil {
		log.Printf("cannot set the valve position: %s\n", err)
	}
}

// adoptUnits takes the units this controller reports in from its configured
// services, and gives the thermal error the setpoint's.
//
// The error is the difference between the setpoint and the measurement, so it is
// in the setpoint's unit by construction rather than by convention. Configuring
// it separately would let the two disagree, and a controller reporting a °C error
// against a °F setpoint would look plausible for a long time.
//
// The unit is used as configured, whatever it says. A pre-QUDT deployment naming
// "Celsius" keeps working; a QUDT one gets an IRI a consumer can convert from.
func (t *Traits) adoptUnits(services components.Services) {
	setpoint := findService(services, "setpoint")
	deviation := findService(services, "deviation")
	jitter := findService(services, "jitter")

	if setpoint != nil {
		t.setpointUnit = firstDetail(setpoint.Details, "Unit")
	}
	if t.setpointUnit == "" {
		t.setpointUnit = templateSetpointUnit()
		log.Printf("%s: the setpoint service in systemconfig.json declares no unit; using %s from the template. Add a \"details\" block naming a Unit to state it explicitly.\n",
			"thermostat", t.setpointUnit)
	}
	if jitter != nil {
		t.jitterUnit = firstDetail(jitter.Details, "Unit")
	}

	if setpoint != nil && t.setpointUnit != "" {
		// Written back into the service, not just held here. A consumer converts
		// using the unit in the registration record, so a controller working in
		// °C while registering a setpoint with no unit invites a PUT in °F that
		// is then believed — the fallback fixes what this system does with the
		// number and leaves everyone else guessing.
		if setpoint.Details == nil {
			setpoint.Details = make(map[string][]string)
		}
		setpoint.Details["Unit"] = []string{t.setpointUnit}
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

// templateSetpointUnit is the unit the shipped template declares for the
// setpoint.
//
// Configure builds a unit asset entirely from systemconfig.json and never
// merges the template into it, so a services array written without a details
// block leaves the setpoint with no unit at all. The README documented exactly
// such an array, so an operator following it got a system that refused to
// start, saying the setpoint was configured in "".
//
// Falling back is the lesser of the two wrongs: a system that runs on the unit
// its own template names, and says so, beats one that will not run. A unit that
// is present but unresolvable is still fatal — that is a statement the operator
// made and got wrong, rather than one they never made.
func templateSetpointUnit() string {
	for _, s := range initTemplate().GetServices() {
		if s.Definition == "setpoint" {
			return firstDetail(s.Details, "Unit")
		}
	}
	return ""
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
