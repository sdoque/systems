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
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
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
	Period int     `json:"samplingPeriod"`
	Kp     float64 `json:"kp"`

	// FrostGuard is how many minutes this controller may go without a
	// temperature before it drives the heat on regardless. Zero disables it.
	//
	// Absent from a configuration written before this existed, which unmarshals
	// as the default rather than as zero — on purpose. A controller that has
	// been upgraded should gain the protection without anyone editing a file,
	// and an operator who genuinely wants it off has to say so.
	FrostGuard int `json:"frostGuardMinutes"`

	// lastGood is when a temperature was last read, and guarding says the frost
	// guard is currently holding the plug on. Both under mu with the rest.
	lastGood  time.Time
	guarding  bool
	jitter    time.Duration
	deviation float64
	previousT float64
	name      string
	owner     *components.System
	cervices  components.Cervices
	// location and tempFrom are what the thermometer is chosen by, kept so it
	// can be chosen again by the same rule when a binding is lost.
	location string
	tempFrom string
	// discover asks the cloud for every provider of a definition. Nil means the
	// orchestrator; a test puts a cloud of its own here.
	discover func(cer *components.Cervice, action string) error
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
		Details:     map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/DEG_C>"}, "QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"}, "Forms": {"SignalA_v1a"}, "Methods": components.HTTPMethods("GET", "PUT")},
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
			SetPt:      20,
			Period:     10,
			Kp:         5,
			FrostGuard: 30,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResources discovers all ZigBee heater plugs from beekeeper via the Orchestrator,
// matches each one to a temperature service from meteorologue, and returns one
// UnitAsset per heater with its own feedback control loop.
// If the dependent services are not yet available it retries every 15 s until they
// are found or the system context is canceled.
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
	SetPt  float64 `json:"setPoint"`
	Period int     `json:"samplingPeriod"`
	Kp     float64 `json:"kp"`
	// Not defaulted back to 30 below when it reads zero, unlike Period and Kp:
	// zero is a real answer here and means the operator turned the guard off.
	FrostGuard int `json:"frostGuardMinutes"`

	// TemperatureFrom names, per location, the sensor a heater may use when
	// there is none in its own room: {"Kitchen": "IndoorModule"}. Without an
	// entry a heater whose room has no thermometer is not controlled at all,
	// and the system says so.
	//
	// This replaces a fallback that took whatever sensor came to hand. It chose
	// by Go map order, so which one it took could differ at every start, and at
	// the cottage all three heaters ended up driven by the bathroom's
	// thermometer — one room's temperature deciding the heat in three. Its last
	// resort was "any node at all", which could have been the outdoor module: in
	// a Norrbotten winter that is a heater that never switches off.
	TemperatureFrom map[string]string `json:"temperatureFrom"`
}

// parseTraitDefaults extracts and validates the trait defaults from the configurable asset.
func parseTraitDefaults(uac usecases.ConfigurableAsset) traitDefaults {
	d := traitDefaults{SetPt: 20, Period: 10, Kp: 5, FrostGuard: 30}
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
// for every beekeeper OnOff plug whose DisplayName ends in "Heater", whether or
// not its thermometer can be found yet.
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
	// Not finding the thermometers is not a reason to build no controllers. The
	// cottage's temperatures come from the Netatmo cloud, so after a power cut
	// they are missing until the router and the internet are back, and the
	// plugs have come back off. A controller without a thermometer still has a
	// frost guard; no controller has nothing.
	if err := usecases.Search4MultipleServices(tempCer, sys); err != nil {
		log.Printf("ethermostat: could not discover temperature services (%v); building the controllers without them\n", err)
	}

	var assets []*components.UnitAsset
	built := make(map[string]bool)

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
			// One controller per heater, even if the plug is offered twice —
			// over http and https by a provider that registered mid-start, say.
			// Two would fight over it.
			if built[plainName(displayName)] {
				continue
			}
			built[plainName(displayName)] = true

			location := extractLocation(displayName)

			heaterOnOff := &components.Cervice{
				Definition: "OnOff",
				Protos:     sProtocols,
				Nodes:      make(map[string][]components.NodeInfo),
				Mode:       "set",
			}
			heaterTemp := &components.Cervice{
				Definition: "temperature",
				Protos:     sProtocols,
				Nodes:      make(map[string][]components.NodeInfo),
				Mode:       "get",
			}

			// The URL has to name the device this node claimed to be. Discovery
			// hands over a name and an address together, and if they disagree
			// the controller would drive something it cannot name — which is how
			// a thermostat came to switch a bathroom light at the cottage for
			// days without anything failing. A node that disagrees is not
			// pinned; the controller looks for its plug by name when it first
			// switches, and switches nothing until it finds it.
			switching := ni.URL
			if urlNames(ni.URL, displayName) {
				pin(heaterOnOff, sysNode, ni)
			} else {
				log.Printf("ethermostat: REFUSING %s: discovery offered it at %s, which is not that device\n", displayName, ni.URL)
				switching = "(not yet found)"
			}

			// Without a thermometer the controller is built all the same, and
			// goes on looking for one by the same rule. Until it finds one its
			// frost guard is what heats the room. That costs electricity when
			// the cause is a configuration mistake; not building it costs the
			// pipes when the cause is a dead battery or no internet.
			tempSysNode, tempNI, why, ok := selectTempNode(tempCer.Nodes, location, defaults.TemperatureFrom[location])
			reading := tempNI.URL
			if ok {
				pin(heaterTemp, tempSysNode, tempNI)
			} else {
				reading = "(no thermometer yet)"
				log.Printf("ethermostat: %s has no temperature yet: %s. It keeps looking, and heats after %d minutes without one. "+
					`If it should use another room's thermometer, name it with "temperatureFrom": {%q: "<module>"}`+"\n",
					displayName, why, defaults.FrostGuard, location)
			}

			t := &Traits{
				SetPt:      defaults.SetPt,
				Period:     defaults.Period,
				Kp:         defaults.Kp,
				FrostGuard: defaults.FrostGuard,
				// Blind since now, not since the zero time: a controller that
				// has never had a reading should wait out the same grace period
				// as one that lost a working sensor, rather than firing on its
				// first failed poll.
				lastGood: time.Now(),
				name:     displayName,
				location: location,
				tempFrom: defaults.TemperatureFrom[location],
				owner:    sys,
				cervices: components.Cervices{
					"on_off":      heaterOnOff,
					"temperature": heaterTemp,
				},
			}

			ua := buildHeaterAsset(displayName, location, t, sys, uac)
			assets = append(assets, ua)
			go t.feedbackLoop(sys.Ctx)
			// The addresses, not just the names: a controller's name is what it
			// believes and the URL is what it will actually switch, and when
			// those two part company the log is the only place it shows.
			log.Printf("ethermostat: created thermostat %q (location=%q)\n    switching %s\n    reading   %s (%s)\n",
				displayName, location, switching, reading, why)
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
func selectTempNode(nodes map[string][]components.NodeInfo, location, named string) (string, components.NodeInfo, string, bool) {
	// Sorted, so the same cloud gives the same answer twice. Ranging over the
	// map put the choice in the hands of Go's map ordering, which is randomized:
	// the heater that got the right sensor on one run got another room's on the
	// next, and nothing in the log marked the difference.
	type candidate struct {
		sysNode string
		ni      components.NodeInfo
	}
	var all []candidate
	for sysNode, nodeList := range nodes {
		for _, ni := range nodeList {
			all = append(all, candidate{sysNode, ni})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sysNode != all[j].sysNode {
			return all[i].sysNode < all[j].sysNode
		}
		return all[i].ni.URL < all[j].ni.URL
	})

	// Tier 1: a sensor that says it is in this room.
	for _, c := range all {
		for _, fl := range c.ni.Details["FunctionalLocation"] {
			if strings.Contains(strings.ToLower(fl), strings.ToLower(location)) {
				return c.sysNode, c.ni, "its functional location", true
			}
		}
	}

	// Tier 2: a sensor named after this room.
	for _, c := range all {
		for _, mn := range c.ni.Details["ModuleName"] {
			if strings.Contains(strings.ToLower(mn), strings.ToLower(location)) {
				return c.sysNode, c.ni, "its module name", true
			}
		}
	}

	// Tier 3: the sensor the operator named for this room, and only that one.
	if named != "" {
		for _, c := range all {
			if nodeIsCalled(c.ni, named) {
				return c.sysNode, c.ni, fmt.Sprintf("configured as %q for %s", named, location), true
			}
		}
		return "", components.NodeInfo{}, fmt.Sprintf(
			"no thermometer in %s, and the configured %q is not among the %d temperature services offered",
			location, named, len(all)), false
	}

	return "", components.NodeInfo{}, fmt.Sprintf("no thermometer reports being in %s, and none is configured for it", location), false
}

// nodeIsCalled reports whether a temperature node answers to a name: its module
// name, its display name, or the asset in its URL.
func nodeIsCalled(ni components.NodeInfo, name string) bool {
	want := strings.ToLower(name)
	for _, key := range []string{"ModuleName", "DisplayName"} {
		for _, v := range ni.Details[key] {
			if strings.Contains(strings.ToLower(v), want) {
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(ni.URL), "/"+want+"/")
}

// urlNames reports whether a service URL belongs to the asset a discovery said
// it was. The comparison ignores case and anything but letters and digits,
// because a provider normalizes an asset name into its path: "lumi.remote.b28"
// is served at "lumi_remote_b28".
func urlNames(url, displayName string) bool {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	asset := parts[len(parts)-2] // .../<system>/<asset>/<service>
	return plainName(asset) == plainName(displayName)
}

func plainName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	f.Unit = usecases.UnitIRI(t.setpointUnit)
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
	f.Unit = usecases.UnitIRI(t.errorUnit)
	f.Timestamp = time.Now()
	return f
}

// getJitter fills out a signal form with the control loop execution jitter.
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

//-------------------------------------Feedback control loop

// feedbackLoop is the control goroutine for this heater thermostat.
//
// It runs on its own clock and also whenever a fresh reading arrives. Both,
// deliberately: the ticker guarantees the loop runs at all — a provider that has
// died says nothing, and a controller waiting only for news would wait for ever
// — while the arrival is what makes it act now rather than at the end of a
// period that has just begun. How often an arrival may wake it is the cervice's
// own business, so a value reported finely does not drive an actuator finely.
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

// processFeedbackLoop reads the temperature, calculates the P-controller output,
// and turns the plug ON (output > 50) or OFF (output ≤ 50).
func (t *Traits) processFeedbackLoop() {
	jitterStart := time.Now()

	if err := t.bindThermometer(); err != nil {
		log.Printf("ethermostat %s: unable to get temperature: %v\n", t.name, err)
		t.frostGuard()
		return
	}
	tf, err := usecases.GetState(t.cervices["temperature"], t.owner)
	if err != nil {
		log.Printf("ethermostat %s: unable to get temperature: %v\n", t.name, err)
		t.frostGuard()
		return
	}
	tup, ok := tf.(*forms.SignalA_v1a)
	if !ok {
		log.Printf("ethermostat %s: unexpected temperature form type\n", t.name)
		t.frostGuard()
		return
	}

	t.mu.Lock()
	blindFor := time.Since(t.lastGood)
	released := t.guarding
	t.lastGood = time.Now()
	t.guarding = false
	t.deviation = t.SetPt - tup.Value
	deviation := t.deviation
	t.mu.Unlock()

	if released {
		log.Printf("ethermostat %s: a temperature arrived after %v — frost guard released, normal control resumes\n",
			t.name, blindFor.Round(time.Second))
	}

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

// frostGuard drives the plug on when this controller has gone too long without
// a temperature, and is the difference between failing safe and failing quiet.
//
// Losing the reading leaves the loop with nothing to act on, and the previous
// answer was to return — holding the plug wherever it was. That is the right
// instinct while a plug remembers its state, and the wrong one here for a
// reason that only shows up on the day it matters: a ZigBee plug returns to
// *off* when mains power is restored, so after a power cut the state being
// faithfully held is "off". The cottage's temperatures come from the Netatmo
// cloud over a domestic line, and after a power cut the router has just
// rebooted too — so the reading is missing at exactly the moment the plugs are
// off and the house is cooling. Holding, there, means never heating again.
//
// So: on, and stay on, until a reading returns. The cost of being wrong is
// electricity. The cost of the previous behaviour is burst pipes, and those are
// not the same kind of wrong.
//
// It fires only after FrostGuard minutes of continuous blindness, so a single
// failed poll or a brief network hiccup changes nothing. Recovery needs no code
// here: the next successful read runs the ordinary control law and sets the
// plug from the measurement, which is what "revert when it arrives" means.
func (t *Traits) frostGuard() {
	t.mu.Lock()
	grace := time.Duration(t.FrostGuard) * time.Minute
	blindFor := time.Since(t.lastGood)
	if grace <= 0 || blindFor < grace {
		t.mu.Unlock()
		return
	}
	announce := !t.guarding
	t.guarding = true
	t.mu.Unlock()

	// Said once per episode rather than once per poll: at a ten-second period
	// this would otherwise write six lines a minute for as long as the outage
	// lasts, and bury the line that says when it started.
	if announce {
		log.Printf("ethermostat %s: no temperature for %v — driving the heat ON and holding it there until a reading returns (frost guard)\n",
			t.name, blindFor.Round(time.Second))
	}
	t.updatePlugState(true)
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
	if err := t.bindPlug(); err != nil {
		log.Printf("ethermostat %s: could not set plug state: %v\n", t.name, err)
		return
	}
	// A failure leaves the binding alone. Clearing it here is what switched the
	// cottage's bathroom light: the next call found no provider, asked the
	// orchestrator for "an OnOff", and was given the light. Whatever mbaigo
	// itself clears — on an unreachable provider, a 404, or a refused
	// credential it could not renew — bindPlug rebinds by name next time.
	if _, err := usecases.SetState(t.cervices["on_off"], t.owner, body); err != nil {
		log.Printf("ethermostat %s: could not set plug state: %v\n", t.name, err)
	}
}

//-------------------------------------Bindings

// The plug and the thermometer are bound by name and never by definition
// alone.
//
// mbaigo re-discovers a cervice that has lost its provider by asking the
// orchestrator for any provider of the definition, which is right for a
// consumer that will take any thermometer and wrong for one that must switch
// one particular plug. Every OnOff in the cottage is a candidate for "OnOff",
// the bathroom light included. So a controller never lets a cervice reach that
// state: before each call it drops anything that is not its own device, and if
// nothing is left it looks for its device by name among everything offered.
// Not finding it is an error and switches nothing.

// bindPlug makes sure the on_off cervice holds this heater's plug and nothing
// else, looking for it by name when the binding has been lost.
func (t *Traits) bindPlug() error {
	cer := t.cervices["on_off"]
	for _, url := range keepNamed(cer, t.name) {
		log.Printf("ethermostat %s: REFUSING %s, which is not this heater's plug\n", t.name, url)
	}
	if bound(cer) {
		return nil
	}
	offered := &components.Cervice{Definition: cer.Definition, Protos: cer.Protos, Mode: cer.Mode,
		Nodes: make(map[string][]components.NodeInfo)}
	if err := t.find(offered); err != nil {
		return fmt.Errorf("looking for its plug: %w", err)
	}
	sysNode, ni, ok := plugFor(offered.Nodes, t.name)
	if !ok {
		return fmt.Errorf("%s is not among the OnOff services offered; switching nothing", t.name)
	}
	pin(cer, sysNode, ni)
	log.Printf("ethermostat %s: switching %s\n", t.name, ni.URL)
	return nil
}

// bindThermometer makes sure the temperature cervice holds a thermometer chosen
// by selectTempNode, choosing again by the same rule when the binding has been
// lost — or was never made, because the thermometers were not there yet.
func (t *Traits) bindThermometer() error {
	cer := t.cervices["temperature"]
	if bound(cer) {
		return nil
	}
	offered := &components.Cervice{Definition: cer.Definition, Protos: cer.Protos, Mode: cer.Mode,
		Details: cer.Details, Nodes: make(map[string][]components.NodeInfo)}
	if err := t.find(offered); err != nil {
		return fmt.Errorf("looking for its thermometer: %w", err)
	}
	sysNode, ni, why, ok := selectTempNode(offered.Nodes, t.location, t.tempFrom)
	if !ok {
		return errors.New(why)
	}
	pin(cer, sysNode, ni)
	log.Printf("ethermostat %s: reading %s (%s)\n", t.name, ni.URL, why)
	return nil
}

func (t *Traits) find(cer *components.Cervice) error {
	action := usecases.ActionForMode(cer.Mode)
	if t.discover != nil {
		return t.discover(cer, action)
	}
	return usecases.Search4MultipleServicesAs(cer, t.owner, action)
}

// plugFor picks the node that is the named device: its display name and its
// URL must both say so. Sorted, so the same offer gives the same answer.
func plugFor(nodes map[string][]components.NodeInfo, name string) (string, components.NodeInfo, bool) {
	sysNodes := make([]string, 0, len(nodes))
	for sysNode := range nodes {
		sysNodes = append(sysNodes, sysNode)
	}
	sort.Strings(sysNodes)
	for _, sysNode := range sysNodes {
		for _, ni := range nodes[sysNode] {
			if plainName(firstDetail(ni.Details, "DisplayName")) == plainName(name) && urlNames(ni.URL, name) {
				return sysNode, ni, true
			}
		}
	}
	return "", components.NodeInfo{}, false
}

// keepNamed drops every node whose URL is not the named device and returns
// what it dropped.
func keepNamed(cer *components.Cervice, name string) []string {
	cer.Mutex.Lock()
	defer cer.Mutex.Unlock()
	var dropped []string
	for sysNode, nodes := range cer.Nodes {
		kept := nodes[:0]
		for _, ni := range nodes {
			if urlNames(ni.URL, name) {
				kept = append(kept, ni)
			} else {
				dropped = append(dropped, ni.URL)
			}
		}
		if len(kept) == 0 {
			delete(cer.Nodes, sysNode)
		} else {
			cer.Nodes[sysNode] = kept
		}
	}
	return dropped
}

func bound(cer *components.Cervice) bool {
	cer.Mutex.RLock()
	defer cer.Mutex.RUnlock()
	for _, nodes := range cer.Nodes {
		if len(nodes) > 0 {
			return true
		}
	}
	return false
}

func pin(cer *components.Cervice, sysNode string, ni components.NodeInfo) {
	cer.Mutex.Lock()
	defer cer.Mutex.Unlock()
	cer.Nodes = map[string][]components.NodeInfo{sysNode: {ni}}
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
	if t.setpointUnit == "" {
		// The same fallback the startup check applies. Without it the check
		// passed on the configured value while every heater built here kept an
		// empty one, and an empty unit is worse than a wrong one: AdoptUnit
		// returns immediately when asked to convert into nothing, so a setpoint
		// PUT as 68 °F was stored as 68. Against a room at 21.5 the error is
		// 46.5, the output saturates, and the heater is held on indefinitely.
		t.setpointUnit = templateSetpointUnit()
		log.Printf("ethermostat: the setpoint service in systemconfig.json declares no unit; using %s from the template. Add a \"details\" block naming a Unit to state it explicitly.\n",
			t.setpointUnit)
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
