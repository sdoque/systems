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
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable and runtime parameters for one electrical heater thermostat.
type Traits struct {
	// SetPt is written by the PUT handler on a net/http goroutine and read by
	// the control loop; deviation and jitter are written by the loop and read by
	// the diff and variations handlers. These run on Raspberry Pis, where a
	// 32-bit build stores a float64 in two words — a setpoint moving 20 to 22
	// while the loop reads it can yield a number that was never written, and
	// calculateOutput turns that into a fully open or fully closed valve.
	mu        sync.RWMutex
	SetPt     float64       `json:"setPoint"`
	Period    time.Duration `json:"samplingPeriod"`
	Kp        float64       `json:"kp"`
	jitter    time.Duration
	deviation float64
	previousT float64
	name      string
	owner     *components.System
	cervices  components.Cervices
	// Units the payloads report in, taken from the configured services so a
	// reading and the record that describes it cannot disagree. errorUnit is
	// not configured: it is the setpoint's.
	setpointUnit string `json:"-"`
	errorUnit    string `json:"-"`
	jitterUnit   string `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used to seed systemconfig.json.
func initTemplate() *components.UnitAsset {
	setPointService := components.Service{
		Definition:  "setpoint",
		SubPath:     "setpoint",
		Mission:     components.MissionState,
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/DEG_C>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
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
		Details:     map[string][]string{"QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "Measure": {"interval"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the current difference between the setpoint and the temperature (GET)",
	}
	jitterService := components.Service{
		Definition:  "jitter",
		SubPath:     "jitter",
		Mission:     components.MissionMeasurement,
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/MilliSEC>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/Time>"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   120,
		Description: "provides the control loop execution jitter in milliseconds (GET)",
	}

	return &components.UnitAsset{
		Name:    "KitchenHeater",
		Mission: components.MissionControl,
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			setPointService.SubPath:  &setPointService,
			deviationService.SubPath: &deviationService,
			jitterService.SubPath:    &jitterService,
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
// If the dependent services are not yet available it retries every 15 s until they
// are found or the system context is cancelled.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	defaults := parseTraitDefaults(uac)
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)

	// Checked here rather than per heater: discovery retries every 15 s until a
	// plug answers, so a misconfigured setpoint unit would otherwise surface
	// minutes later, or on a quiet cloud never at all.
	setpointUnit := configuredSetpointUnit(uac)
	if _, ok := usecases.LookupUnit(setpointUnit); !ok {
		log.Fatalf("ethermostat: the setpoint is configured in %q, which is not a QUDT unit this framework can convert a measurement into. Write an identifier such as <http://qudt.org/vocab/unit/DEG_C> in the setpoint service's details.\n", setpointUnit)
	}

	var assets []*components.UnitAsset
	for {
		assets = discoverHeaters(sys, sProtocols, defaults, uac)
		if len(assets) > 0 {
			break
		}
		log.Println("ethermostat: no heater plugs found — retrying in 15 s (waiting for beekeeper and meteorologue)")
		select {
		case <-time.After(15 * time.Second):
		case <-sys.Ctx.Done():
			return nil, func() {}
		}
	}

	return assets, func() {
		log.Println("ethermostat: shutting down")
	}
}

// traitDefaults is the configured starting point for every heater this system
// builds — three numbers read from the file, not a running controller.
//
// A type of its own because Traits carries the mutex that guards a live control
// loop, and passing that by value copies the lock: go vet's copylocks refuses
// it, which is what turned this into a build failure for every module after
// ethermostat in the Makefile. A bag of configured numbers has nothing to
// guard, so it should not be carrying the thing that does the guarding.
type traitDefaults struct {
	SetPt  float64       `json:"setPoint"`
	Period time.Duration `json:"samplingPeriod"`
	Kp     float64       `json:"kp"`
}

// parseTraitDefaults extracts and validates the trait defaults from the configurable asset.
func parseTraitDefaults(uac usecases.ConfigurableAsset) traitDefaults {
	d := traitDefaults{SetPt: 20, Period: 10, Kp: 5}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &d); err != nil {
			log.Println("ethermostat: warning — could not unmarshal traits:", err)
		}
	}
	if d.Period == 0 {
		d.Period = 10
	}
	if d.Kp == 0 {
		d.Kp = 5
	}
	return d
}

// discoverHeaters performs one round of service discovery and returns a UnitAsset
// for every beekeeper OnOff plug whose DisplayName ends in "Heater" and for which
// a matching meteorologue Temperature service can be found.
func discoverHeaters(sys *components.System, sProtocols []string, defaults traitDefaults, uac usecases.ConfigurableAsset) []*components.UnitAsset {
	onOffCer := &components.Cervice{
		Definition: "OnOff",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		// The plugs are switched, never read, so the tokens this discovery
		// obtains have to be write tokens. The nodes it finds are pinned to one
		// heater each below and are not rediscovered, so a token minted for the
		// wrong action would not be corrected later: every PUT would be refused
		// and the heaters would never switch.
		Mode: "set",
	}
	if err := usecases.Search4MultipleServices(onOffCer, sys); err != nil {
		log.Printf("ethermostat: could not discover OnOff services: %v\n", err)
		return nil
	}

	tempCer := &components.Cervice{
		Definition: "temperature",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}
	if err := usecases.Search4MultipleServices(tempCer, sys); err != nil {
		log.Printf("ethermostat: could not discover temperature services: %v\n", err)
		return nil
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

			heaterOnOff := &components.Cervice{
				Definition: "OnOff",
				Protos:     sProtocols,
				Nodes: map[string][]components.NodeInfo{
					sysNode: {ni},
				},
				Mode: "set",
			}

			tempSysNode, tempNI, ok := selectTempNode(tempCer.Nodes, location)
			if !ok {
				log.Printf("ethermostat: no temperature service found for %s — skipping\n", displayName)
				continue
			}
			heaterTemp := &components.Cervice{
				Definition: "temperature",
				Protos:     sProtocols,
				Nodes: map[string][]components.NodeInfo{
					tempSysNode: {tempNI},
				},
				Mode: "get",
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

	return assets
}

// buildHeaterAsset creates a UnitAsset for one heater thermostat.
func buildHeaterAsset(name, location string, t *Traits, sys *components.System, uac usecases.ConfigurableAsset) *components.UnitAsset {
	ua := &components.UnitAsset{
		Name:        name,
		Mission:     components.MissionActuation,
		Owner:       sys,
		Details:     map[string][]string{"FunctionalLocation": {location}},
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: t.cervices,
		Traits:      t,
	}
	t.adoptUnits(ua.ServicesMap)

	// The temperature cervice is built by discovery, so it carries the provider's
	// details and not a request. Stating the unit here is what makes the reading
	// arrive in the setpoint's: the loop subtracts one from the other, and a °F
	// module against a °C target gives a deviation that is wrong in both sign and
	// magnitude while looking like an ordinary number.
	if cer := t.cervices["temperature"]; cer != nil && t.setpointUnit != "" {
		if cer.Details == nil {
			cer.Details = make(map[string][]string)
		}
		cer.Details["QuantityKind"] = []string{"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}
		cer.Details["Unit"] = []string{t.setpointUnit}
	}

	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// configuredSetpointUnit reports the unit the setpoint service is configured in,
// before any asset exists to adopt it.
func configuredSetpointUnit(uac usecases.ConfigurableAsset) string {
	for _, s := range uac.Services {
		if s.Definition == "setpoint" {
			if unit := firstDetail(s.Details, "Unit"); unit != "" {
				return unit
			}
		}
	}
	unit := templateSetpointUnit()
	log.Printf("ethermostat: the setpoint service in systemconfig.json declares no unit; using %s from the template. Add a \"details\" block naming a Unit to state it explicitly.\n", unit)
	return unit
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

//-------------------------------------Helper functions for discovery

// extractLocation strips the "Heater" suffix to get the functional location prefix.
// E.g. "KitchenHeater" → "Kitchen", "DiningRoomHeater" → "DiningRoom".
func extractLocation(heaterName string) string {
	return strings.TrimSuffix(heaterName, "Heater")
}

// selectTempNode finds the best temperature NodeInfo for the given location using
// a three-tier priority:
//  1. A node whose FunctionalLocation detail contains the location string.
//  2. A node whose ModuleName detail contains the location string.
//  3. Fallback: any node that is not an outdoor module (avoids using outdoor
//     temperature for indoor heating control); last resort is any node at all.
func selectTempNode(nodes map[string][]components.NodeInfo, location string) (string, components.NodeInfo, bool) {
	// Tier 1: FunctionalLocation match.
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			for _, fl := range ni.Details["FunctionalLocation"] {
				if strings.Contains(strings.ToLower(fl), strings.ToLower(location)) {
					return sysNode, ni, true
				}
			}
		}
	}

	// Tier 2: ModuleName match (e.g. "Bathroom" heater → ModuleName "Bathroom").
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			for _, mn := range ni.Details["ModuleName"] {
				if strings.Contains(strings.ToLower(mn), strings.ToLower(location)) {
					return sysNode, ni, true
				}
			}
		}
	}

	// Tier 3a: Prefer the main indoor module — the one whose ModuleName contains
	// "indoor" (case-insensitive).  This picks the primary base-station sensor
	// over a room-specific secondary module (e.g. "Bathroom") when no exact
	// location match exists.
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			for _, mn := range ni.Details["ModuleName"] {
				if strings.Contains(strings.ToLower(mn), "indoor") {
					return sysNode, ni, true
				}
			}
		}
	}

	// Tier 3b: Any indoor module (not outdoor).
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			if isIndoorNode(ni) {
				return sysNode, ni, true
			}
		}
	}

	// Tier 3c: Last resort — any node available.
	for sysNode, nodeList := range nodes {
		if len(nodeList) > 0 {
			return sysNode, nodeList[0], true
		}
	}
	return "", components.NodeInfo{}, false
}

// isIndoorNode returns true when no ModuleName or FunctionalLocation detail
// contains the word "outdoor" (case-insensitive).
func isIndoorNode(ni components.NodeInfo) bool {
	for _, mn := range ni.Details["ModuleName"] {
		if strings.Contains(strings.ToLower(mn), "outdoor") {
			return false
		}
	}
	for _, fl := range ni.Details["FunctionalLocation"] {
		if strings.Contains(strings.ToLower(fl), "outdoor") {
			return false
		}
	}
	return true
}

//-------------------------------------Service handlers

// setpt handles GET (read setpoint) and PUT (update setpoint) requests.
func (t *Traits) setpt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "unreadable setpoint: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := t.setSetPoint(sig); err != nil {
			// Refusing is the only safe answer: a setpoint in an unexpected unit
			// is a number that will drive the heater for as long as nobody
			// notices it looks reasonable.
			log.Printf("ethermostat %s: %v\n", t.name, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		confirmed := t.getSetPoint()
		usecases.HTTPProcessGetRequest(w, r, &confirmed)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// diff handles GET requests for the current thermal error.
func (t *Traits) diff(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getError()
		usecases.HTTPProcessGetRequest(w, r, &f)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// variations handles GET requests for the control loop jitter.
func (t *Traits) variations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f := t.getJitter()
		usecases.HTTPProcessGetRequest(w, r, &f)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

//-------------------------------------Thing's resource methods

// getSetPoint fills out a signal form with the current thermal setpoint.
func (t *Traits) getSetPoint() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	f.Value = t.SetPt
	t.mu.RUnlock()
	f.Unit = t.setpointUnit
	f.Timestamp = time.Now()
	return f
}

// setSetPoint updates the thermal setpoint.
func (t *Traits) setSetPoint(f forms.SignalA_v1a) error {
	// The value arrives in whatever unit the sender works in. Writing it
	// straight into the loop is how a Fahrenheit target silently becomes a
	// Celsius one, and this controller drives real heaters.
	if err := usecases.AdoptUnit(&f, t.setpointUnit, false); err != nil {
		return fmt.Errorf("setpoint refused: %w", err)
	}
	t.mu.Lock()
	t.SetPt = f.Value
	t.mu.Unlock()
	log.Printf("ethermostat %s: new setpoint %.1f %s\n", t.name, f.Value, t.setpointUnit)
	return nil
}

// getError fills out a signal form with the current thermal error.
func (t *Traits) getError() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	f.Value = t.deviation
	t.mu.RUnlock()
	f.Unit = t.errorUnit
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the control loop execution jitter.
func (t *Traits) getJitter() (f forms.SignalA_v1a) {
	f.NewForm()
	t.mu.RLock()
	f.Value = float64(t.jitter.Milliseconds())
	t.mu.RUnlock()
	f.Unit = t.jitterUnit
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

	t.mu.Lock()
	t.deviation = t.SetPt - tup.Value
	deviation := t.deviation
	t.mu.Unlock()

	output := t.calculateOutput(deviation)
	plugOn := output > 50

	if tup.Value != t.previousT {
		state := "OFF"
		if plugOn {
			state = "ON"
		}
		log.Printf("ethermostat %s: temp=%.2f %s err=%.2f %s → plug %s\n",
			t.name, tup.Value, t.setpointUnit, deviation, t.errorUnit, state)
		t.previousT = tup.Value
	}

	t.updatePlugState(plugOn)

	t.mu.Lock()
	t.jitter = time.Since(jitterStart)
	t.mu.Unlock()
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
