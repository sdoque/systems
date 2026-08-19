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
	"encoding/json"
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

//-------------------------------------The layout's vocabulary

// Locomotive is what the Central Station knows about one engine: its identity,
// and which of its functions do what.
//
// The station holds this list persistently — a locomotive that registered once
// stays known whether or not it is on the track — which is why this system can
// build its unit assets at startup instead of waiting for something to be placed
// on the rails.
type Locomotive struct {
	UID       uint32
	Name      string      // the station's name for it, e.g. "421 393-0"
	Kind      DecoderKind // which protocol its decoder speaks
	Functions []Function
}

// Function is one of a locomotive's switchable features.
//
// The number is what goes on the wire; the name is what the Central Station
// calls it, and it is the reason a service can be called "horn" rather than
// "f3". A locomotive whose station entry names nothing falls back to the number.
type Function struct {
	Number uint8
	Name   string
}

// LocoSource is where the locomotive list comes from. The Central Station is the
// real one; a test supplies its own, so everything below this line can be
// exercised without a layout, a CAN interface, or a locomotive.
type LocoSource interface {
	Locomotives() ([]Locomotive, error)
}

//-------------------------------------Define the unit asset

// Traits holds one locomotive's state and its way onto the bus.
type Traits struct {
	loco Locomotive
	bus  Bus

	mu        sync.RWMutex
	speed     float64 // percent of full scale
	forward   bool
	functions map[uint8]bool
	// onTrack becomes true when the layout is heard from — see Observe. Nothing
	// hears from it yet: Bus is send-only and openBus returns a silentBus, so
	// until the CAN receive path exists this stays false and every command is
	// refused. The refusal is correct; what was not was answering a GET with the
	// zero value as though it were a reading.
	onTrack  bool
	lastSeen time.Time

	owner *components.System
}

// Bus is the way out to the layout, kept behind an interface so the protocol and
// the transport can be tested apart from each other.
type Bus interface {
	Send(Frame) error
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns the asset a fresh deployment starts from, describing one
// locomotive so an operator can see the shape before the station is reachable.
func initTemplate() *components.UnitAsset {
	example := Locomotive{
		UID:  0x4001,
		Name: "421 393-0",
		Kind: DecoderMFX,
		Functions: []Function{
			{Number: 0, Name: "light"},
			{Number: 3, Name: "horn"},
		},
	}
	return unitAssetFor(example, assetName(example), nil, nil)
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResources builds one unit asset per locomotive the Central Station knows.
//
// The station's list is authoritative and persistent, so this is the whole set —
// including locomotives currently in their boxes. Placing one on the track
// changes whether its services answer, not whether they exist, which keeps the
// registry stable as engines come and go.
func newResources(configuredAsset usecases.ConfigurableAsset, sys *components.System, source LocoSource, bus Bus) ([]*components.UnitAsset, func()) {
	locomotives, err := source.Locomotives()
	if err != nil {
		log.Fatalf(`hobbyist: cannot read the locomotive list: %v

Write locomotives.json beside the binary, listing what the Central Station
knows. Its uid is hexadecimal and its function keys are the numbers the
decoder uses:

[
  {"uid": "0x4001", "name": "421 393-0", "functions": {"0": "Light", "3": "Horn"}}
]
`, err)
	}
	if len(locomotives) == 0 {
		log.Println("hobbyist: the Central Station knows no locomotives yet — place one on the track and restart")
	}

	// Said once and plainly. Without a transport every command is refused and
	// every read reports nothing heard, and an operator watching a rack of 503s
	// deserves to know the cause is a missing driver rather than a locomotive in
	// its box.
	if _, silent := bus.(silentBus); silent {
		log.Println("hobbyist: no CAN transport is open, so nothing can be commanded and no locomotive state can be read — the services are registered and will answer 503 until openBus returns a real bus")
	}

	names := disambiguate(locomotives)

	assets := make([]*components.UnitAsset, 0, len(locomotives))
	for _, loco := range locomotives {
		ua := unitAssetFor(loco, names[loco.UID], bus, sys)
		assets = append(assets, ua)
		log.Printf("hobbyist: %s (%s, uid %#x) with %d function(s)\n", loco.Name, loco.Kind, loco.UID, len(loco.Functions))
	}

	return assets, func() {
		log.Println("hobbyist: leaving the layout")
	}
}

// unitAssetFor turns one locomotive into a unit asset with a service per thing it
// can be told to do.
//
// The functional location is the locomotive's own identity. A locomotive moves,
// so no place on the layout describes it — but its light and its horn are on
// *that* engine, and a consumer paired to one locomotive should reach that one's
// horn and no other's.
func unitAssetFor(loco Locomotive, name string, bus Bus, sys *components.System) *components.UnitAsset {
	t := &Traits{
		loco:      loco,
		bus:       bus,
		forward:   true,
		functions: make(map[uint8]bool),
		owner:     sys,
	}

	services := components.Services{}
	for _, serv := range servicesFor(loco) {
		s := serv
		services[s.SubPath] = &s
	}

	ua := &components.UnitAsset{
		Name:    name,
		Mission: components.MissionActuation,
		Owner:   sys,
		Details: map[string][]string{
			"FunctionalLocation": {loco.Name},
			"Decoder":            {string(loco.Kind)},
		},
		ServicesMap: services,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// servicesFor describes everything a locomotive can be told to do.
//
// Speed is a fraction of the decoder's full scale rather than a physical speed,
// so it is a ratio with its range stated — the same shape as a servo's travel,
// and for the same reason: 50 means nothing without knowing what it is half of.
func servicesFor(loco Locomotive) []components.Service {
	services := []components.Service{
		{
			Definition: "speed",
			SubPath:    "speed",
			Mission:    components.MissionActuation,
			Details: map[string][]string{
				"Forms":        {"SignalA_v1a"},
				"Unit":         {"<http://qudt.org/vocab/unit/PERCENT>"},
				"QuantityKind": {"<http://qudt.org/vocab/quantitykind/DimensionlessRatio>"},
				// The service's range, not the decoder's. SpeedMax is the
				// wire full-scale value, and advertising it beside a unit of
				// PERCENT told a consumer it could ask for 1000 % — which
				// SpeedFrame then refuses. The two disagreed and the consumer
				// was told the wrong one.
				"Range":   {"0", "100"},
				"Methods": components.HTTPMethods("GET", "PUT"),
			},
			RegPeriod:   30,
			Description: "reads the current speed (GET) or sets it (PUT), as a percentage of the decoder's full scale",
		},
		{
			Definition: "direction",
			SubPath:    "direction",
			Mission:    components.MissionActuation,
			Details: map[string][]string{
				"Forms": {"SignalB_v1a"}, "Methods": components.HTTPMethods("GET", "PUT")},
			RegPeriod:   30,
			Description: "reads the direction of travel (GET) or sets it (PUT), true being forward",
		},
	}

	for _, f := range functionsInOrder(loco.Functions) {
		services = append(services, components.Service{
			Definition: functionName(f),
			SubPath:    functionName(f),
			Mission:    components.MissionActuation,
			Details: map[string][]string{
				"Forms":    {"SignalB_v1a"},
				"Function": {fmt.Sprint(f.Number)},
				"Methods":  components.HTTPMethods("GET", "PUT"),
			},
			RegPeriod:   30,
			Description: fmt.Sprintf("switches function %d (GET reads it, PUT sets it)", f.Number),
		})
	}
	return services
}

// functionName is what the station calls a function, reduced to something usable
// as a service definition and a URL segment. A function the station has not named
// keeps its number, because "f3" is at least honest.
func functionName(f Function) string {
	name := strings.ToLower(strings.TrimSpace(f.Name))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_':
			return '_'
		}
		return -1
	}, name)
	name = strings.Trim(name, "_")
	if name == "" {
		return fmt.Sprintf("f%d", f.Number)
	}
	return name
}

// functionsInOrder sorts by function number so the services a locomotive
// registers do not depend on map iteration or the order the station happened to
// send them.
func functionsInOrder(functions []Function) []Function {
	ordered := append([]Function(nil), functions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	return ordered
}

// assetName identifies the locomotive in URLs and in the registry. The station's
// name is what an operator recognizes; the UID keeps two engines with the same
// name apart.
func assetName(loco Locomotive) string {
	name := strings.TrimSpace(loco.Name)
	if name == "" {
		return fmt.Sprintf("loco_%X", loco.UID)
	}
	return strings.ReplaceAll(name, " ", "_")
}

// disambiguate gives every locomotive a name no other locomotive answers to.
//
// The uid was used only when the name was empty, so two engines named "421
// 393-0" — the same model twice, which a layout has every reason to hold —
// produced one asset name and sys.UAssets silently kept whichever was built
// last. The other locomotive was on the track and unreachable.
//
// A name that does not collide is left alone, so an existing deployment's URLs
// do not move. Only the duplicates take the uid, which is the identity the
// station uses anyway.
func disambiguate(locomotives []Locomotive) map[uint32]string {
	counts := make(map[string]int, len(locomotives))
	for _, loco := range locomotives {
		counts[assetName(loco)]++
	}

	names := make(map[uint32]string, len(locomotives))
	for _, loco := range locomotives {
		name := assetName(loco)
		if counts[name] > 1 {
			name = fmt.Sprintf("%s_%X", name, loco.UID)
			log.Printf("hobbyist: more than one locomotive is called %q — this one answers to %q\n",
				assetName(loco), name)
		}
		names[loco.UID] = name
	}
	return names
}

//-------------------------------------Service handlers

func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "speed":
		t.serveSpeed(w, r)
	case "direction":
		t.serveDirection(w, r)
	default:
		t.serveFunction(w, r, servicePath)
	}
}

// available reports whether the locomotive can be commanded.
//
// A locomotive in its box is still a locomotive the station knows, so its
// services exist and answer honestly rather than vanishing from the registry
// every time an engine is lifted off the rails.
func (t *Traits) available() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.onTrack
}

func (t *Traits) unavailable(w http.ResponseWriter) {
	http.Error(w, fmt.Sprintf("%s is not on the track", t.loco.Name), http.StatusServiceUnavailable)
}

// observed reports whether the layout has ever said anything about this
// locomotive. Until it has, there is no state to serve.
func (t *Traits) observed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.lastSeen.IsZero()
}

// unobserved refuses a read for which there is no reading.
//
// The alternative was what this code used to do: serve speed 0, forward true and
// a zero timestamp, which is not "unknown" but a specific and false claim about
// where the locomotive is and what it is doing. A consumer cannot tell that
// apart from a stationary engine.
func (t *Traits) unobserved(w http.ResponseWriter) {
	http.Error(w, fmt.Sprintf("nothing has been heard from %s: the layout has not reported its state",
		t.loco.Name), http.StatusServiceUnavailable)
}

func (t *Traits) serveSpeed(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !t.observed() {
			t.unobserved(w)
			return
		}
		t.mu.RLock()
		f := t.signalA(t.speed, "<http://qudt.org/vocab/unit/PERCENT>")
		t.mu.RUnlock()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		if !t.available() {
			t.unavailable(w)
			return
		}
		var f forms.SignalA_v1a
		if err := decode(r, &f); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := t.setSpeed(f.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

func (t *Traits) serveDirection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !t.observed() {
			t.unobserved(w)
			return
		}
		t.mu.RLock()
		f := t.signalB(t.forward)
		t.mu.RUnlock()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		if !t.available() {
			t.unavailable(w)
			return
		}
		var f forms.SignalB_v1a
		if err := decode(r, &f); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := t.setDirection(f.Value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

func (t *Traits) serveFunction(w http.ResponseWriter, r *http.Request, servicePath string) {
	number, ok := t.functionNumber(servicePath)
	if !ok {
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !t.observed() {
			t.unobserved(w)
			return
		}
		t.mu.RLock()
		f := t.signalB(t.functions[number])
		t.mu.RUnlock()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		if !t.available() {
			t.unavailable(w)
			return
		}
		var f forms.SignalB_v1a
		if err := decode(r, &f); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := t.setFunction(number, f.Value); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

//-------------------------------------Unit asset's functionalities

// setSpeed commands the locomotive and remembers what was asked for.
func (t *Traits) setSpeed(percent float64) error {
	frame, err := SpeedFrame(t.loco.UID, percent)
	if err != nil {
		return err
	}
	if err := t.send(frame); err != nil {
		return err
	}
	t.mu.Lock()
	t.speed = percent
	t.mu.Unlock()
	return nil
}

func (t *Traits) setDirection(forward bool) error {
	if err := t.send(DirectionFrame(t.loco.UID, forward)); err != nil {
		return err
	}
	t.mu.Lock()
	t.forward = forward
	t.mu.Unlock()
	return nil
}

func (t *Traits) setFunction(number uint8, on bool) error {
	if err := t.send(FunctionFrame(t.loco.UID, number, on)); err != nil {
		return err
	}
	t.mu.Lock()
	t.functions[number] = on
	t.mu.Unlock()
	return nil
}

func (t *Traits) send(f Frame) error {
	if t.bus == nil {
		return fmt.Errorf("no connection to the layout")
	}
	return t.bus.Send(f)
}

// functionNumber maps a service subpath back to the function number on the wire.
func (t *Traits) functionNumber(servicePath string) (uint8, bool) {
	for _, f := range t.loco.Functions {
		if functionName(f) == servicePath {
			return f.Number, true
		}
	}
	return 0, false
}

// Observe records what the layout reports about this locomotive, which is how a
// service knows the difference between a speed that was commanded and one the
// engine is actually running at.
func (t *Traits) Observe(f Frame) {
	uid, err := UIDOf(f.Data)
	if err != nil || uid != t.loco.UID {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.onTrack = true
	t.lastSeen = time.Now()

	switch f.Command {
	case cmdLocoSpeed:
		if percent, err := SpeedPercent(f.Data); err == nil {
			t.speed = percent
		}
	case cmdLocoDirection:
		if len(f.Data) >= 5 {
			switch f.Data[4] {
			case dirForward:
				t.forward = true
			case dirReverse:
				t.forward = false
			}
		}
	case cmdLocoFunction:
		if len(f.Data) >= 6 {
			t.functions[f.Data[4]] = f.Data[5] != 0
		}
	}
}

func (t *Traits) signalA(value float64, unit string) forms.SignalA_v1a {
	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = value
	f.Unit = unit
	f.Timestamp = t.lastSeen
	return f
}

func (t *Traits) signalB(value bool) forms.SignalB_v1a {
	var f forms.SignalB_v1a
	f.NewForm()
	f.Value = value
	f.Timestamp = t.lastSeen
	return f
}

func decode(r *http.Request, into any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		return fmt.Errorf("unreadable request: %w", err)
	}
	return nil
}
