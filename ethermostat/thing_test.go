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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// TestGetSetPoint verifies that getSetPoint returns a form with the correct Value and Unit.
func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 21.5, setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}
	f := tr.getSetPoint()
	if f.Value != 21.5 {
		t.Errorf("expected Value 21.5, got %f", f.Value)
	}
	if f.Unit != "http://qudt.org/vocab/unit/DEG_C" {
		t.Errorf("expected Unit %q, got %q", "<http://qudt.org/vocab/unit/DEG_C>", f.Unit)
	}
}

// TestSetSetPoint verifies that setSetPoint updates SetPt.
func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 20.0, name: "KitchenHeater", setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}
	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 22.0
	f.Unit = "<http://qudt.org/vocab/unit/DEG_C>"
	f.Timestamp = time.Now()
	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}
	if tr.SetPt != 22.0 {
		t.Errorf("expected SetPt 22.0, got %f", tr.SetPt)
	}
}

// TestGetError verifies that getError returns a form with the correct deviation.
func TestGetError(t *testing.T) {
	tr := &Traits{deviation: -2.0, errorUnit: "<http://qudt.org/vocab/unit/DEG_C>"}
	f := tr.getError()
	if f.Value != -2.0 {
		t.Errorf("expected Value -2.0, got %f", f.Value)
	}
	if f.Unit != "http://qudt.org/vocab/unit/DEG_C" {
		t.Errorf("expected Unit %q, got %q", "<http://qudt.org/vocab/unit/DEG_C>", f.Unit)
	}
}

// TestGetJitter verifies that getJitter returns the jitter in milliseconds.
func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 37 * time.Millisecond, jitterUnit: "<http://qudt.org/vocab/unit/MilliSEC>"}
	f := tr.getJitter()
	if f.Value != 37.0 {
		t.Errorf("expected Value 37.0, got %f", f.Value)
	}
	if f.Unit != "http://qudt.org/vocab/unit/MilliSEC" {
		t.Errorf("expected Unit %q, got %q", "<http://qudt.org/vocab/unit/MilliSEC>", f.Unit)
	}
}

// TestCalculateOutput tests the P-controller clamped to [0, 100].
func TestCalculateOutput(t *testing.T) {
	cases := []struct {
		name     string
		kp       float64
		diff     float64
		expected float64
	}{
		{"clamp high: Kp=5 diff=10 → 100", 5, 10, 100},
		{"neutral: Kp=5 diff=0 → 50", 5, 0, 50},
		{"clamp low: Kp=5 diff=-20 → 0", 5, -20, 0},
		{"proportional: Kp=1 diff=5 → 55", 1, 5, 55},
		{"temp above setpoint: Kp=5 diff=-2 → 40", 5, -2, 40},
		{"temp below setpoint: Kp=5 diff=2 → 60", 5, 2, 60},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tr := &Traits{Kp: tc.kp}
			got := tr.calculateOutput(tc.diff)
			if got != tc.expected {
				t.Errorf("calculateOutput(%f) with Kp=%f: expected %f, got %f",
					tc.diff, tc.kp, tc.expected, got)
			}
		})
	}
}

// TestCalculateOutput_BooleanThreshold verifies the ON/OFF threshold at output=50.
func TestCalculateOutput_BooleanThreshold(t *testing.T) {
	tr := &Traits{Kp: 5}
	// diff=0 → output=50 → OFF (not > 50)
	if tr.calculateOutput(0) > 50 {
		t.Error("expected output=50 to map to OFF (not > 50)")
	}
	// diff=0.1 → output=50.5 → ON
	if !(tr.calculateOutput(0.1) > 50) {
		t.Error("expected output=50.5 to map to ON (> 50)")
	}
}

// TestExtractLocation verifies that the "Heater" suffix is stripped correctly.
func TestExtractLocation(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"KitchenHeater", "Kitchen"},
		{"DiningRoomHeater", "DiningRoom"},
		{"BathroomHeater", "Bathroom"},
		{"Heater", ""},
	}
	for _, tc := range cases {
		got := extractLocation(tc.input)
		if got != tc.expected {
			t.Errorf("extractLocation(%q): expected %q, got %q", tc.input, tc.expected, got)
		}
	}
}

// TestSelectTempNode_ExactMatch verifies that a node with a matching FunctionalLocation is preferred.
func TestSelectTempNode_ExactMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Källkälchen (Indoor)"}}},
			{URL: "http://host/meteorologue/KitchenModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Kitchen (Indoor)"}}},
		},
	}
	sysNode, ni, ok := selectTempNode(nodes, "Kitchen")
	if !ok {
		t.Fatal("expected a match, got none")
	}
	if sysNode != "meteorologue" {
		t.Errorf("expected sysNode 'meteorologue', got %q", sysNode)
	}
	if ni.URL != "http://host/meteorologue/KitchenModule/temperature" {
		t.Errorf("unexpected URL: %s", ni.URL)
	}
}

// TestSelectTempNode_ModuleNameMatch verifies that ModuleName is used when FunctionalLocation has no match.
func TestSelectTempNode_ModuleNameMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Indoor"},
			}},
			{URL: "http://host/meteorologue/IndoorModule2/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Bathroom"},
			}},
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"},
				"ModuleName":         {"Outdoor"},
			}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "Bathroom")
	if !ok {
		t.Fatal("expected ModuleName match, got none")
	}
	if ni.URL != "http://host/meteorologue/IndoorModule2/temperature" {
		t.Errorf("expected Bathroom module URL, got %s", ni.URL)
	}
}

// TestSelectTempNode_IndoorPreferredOverOutdoor verifies that the fallback prefers indoor nodes.
func TestSelectTempNode_IndoorPreferredOverOutdoor(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{
				"ModuleName": {"Outdoor"},
			}},
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"ModuleName": {"Indoor"},
			}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "Kitchen")
	if !ok {
		t.Fatal("expected fallback match, got none")
	}
	if strings.Contains(ni.URL, "Outdoor") {
		t.Errorf("expected indoor fallback, got outdoor URL: %s", ni.URL)
	}
}

// TestSelectTempNode_Fallback verifies that the first available node is used when no location matches.
func TestSelectTempNode_Fallback(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Källkälchen (Indoor)"}}},
		},
	}
	_, ni, ok := selectTempNode(nodes, "DiningRoom")
	if !ok {
		t.Fatal("expected fallback match, got none")
	}
	if ni.URL != "http://host/meteorologue/IndoorModule/temperature" {
		t.Errorf("unexpected fallback URL: %s", ni.URL)
	}
}

// TestSelectTempNode_Empty verifies that an empty nodes map returns not-found.
func TestSelectTempNode_Empty(t *testing.T) {
	_, _, ok := selectTempNode(map[string][]components.NodeInfo{}, "Kitchen")
	if ok {
		t.Error("expected not-found for empty nodes map")
	}
}

// TestSetpt_GET verifies the setpoint handler returns 200 for GET.
func TestSetpt_GET(t *testing.T) {
	tr := &Traits{SetPt: 20.0}
	req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
	w := httptest.NewRecorder()
	tr.setpt(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestSetpt_InvalidMethod verifies the setpoint handler returns 405 for DELETE.
func TestSetpt_InvalidMethod(t *testing.T) {
	tr := &Traits{SetPt: 20.0}
	req := httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
	w := httptest.NewRecorder()
	tr.setpt(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestDiff_GET verifies the deviation handler returns 200 for GET.
func TestDiff_GET(t *testing.T) {
	tr := &Traits{deviation: 1.0}
	req := httptest.NewRequest(http.MethodGet, "/deviation", nil)
	w := httptest.NewRecorder()
	tr.diff(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestDiff_InvalidMethod verifies the deviation handler returns 405 for POST.
func TestDiff_InvalidMethod(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodPost, "/deviation", nil)
	w := httptest.NewRecorder()
	tr.diff(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestVariations_GET verifies the jitter handler returns 200 for GET.
func TestVariations_GET(t *testing.T) {
	tr := &Traits{jitter: 5 * time.Millisecond}
	req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
	w := httptest.NewRecorder()
	tr.variations(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
}

// TestVariations_InvalidMethod verifies the jitter handler returns 405 for DELETE.
func TestVariations_InvalidMethod(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodDelete, "/jitter", nil)
	w := httptest.NewRecorder()
	tr.variations(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestServing_InvalidPath verifies that an unknown path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	w := httptest.NewRecorder()
	serving(tr, w, req, "unknown")
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

// The mission the authorizer evaluates is the service's, not the asset's. A
// controller is one asset whose services differ in kind: writing the setpoint
// reconfigures the loop, while the error and jitter readings only observe it.
// Collapsing them onto the asset's "control" would mean any policy letting a
// consumer move a setpoint also let it write everything else here.
func TestInitTemplateServiceMissions(t *testing.T) {
	ua := initTemplate()

	if ua.Mission.String() != "control" {
		t.Errorf("asset mission = %q; want %q", ua.Mission, "control")
	}

	want := map[string]string{
		"setpoint":  "state",
		"deviation": "measurement",
		"jitter":    "measurement",
	}

	for subPath, mission := range want {
		serv, ok := ua.ServicesMap[subPath]
		if !ok {
			t.Errorf("service %q missing from the template", subPath)
			continue
		}
		if got := components.EffectiveMission(ua, serv); got.String() != mission {
			t.Errorf("service %q effective mission = %q; want %q", subPath, got, mission)
		}
	}
}

// TestSetpointAdoptsTheConfiguredUnit is the defect this test was written for:
// setSetPoint wrote f.Value into the control loop without ever reading f.Unit,
// so a Fahrenheit target became a Celsius one. This controller switches real
// heaters, and 68 taken for °C is a room held at 68 °C.
func TestSetpointAdoptsTheConfiguredUnit(t *testing.T) {
	degC := "<http://qudt.org/vocab/unit/DEG_C>"
	tr := &Traits{SetPt: 20, name: "KitchenHeater", setpointUnit: degC}

	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 68
	f.Unit = "<http://qudt.org/vocab/unit/DEG_F>"
	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}
	if tr.SetPt < 19.99 || tr.SetPt > 20.01 {
		t.Errorf("68 °F is 20 °C, got %v", tr.SetPt)
	}

	// A percentage is not a temperature.
	var wrong forms.SignalA_v1a
	wrong.NewForm()
	wrong.Value = 50
	wrong.Unit = "<http://qudt.org/vocab/unit/PERCENT>"
	if err := tr.setSetPoint(wrong); err == nil {
		t.Errorf("a percentage was accepted as a temperature: SetPt = %v", tr.SetPt)
	}

	// A bare number says nothing, and this loop switches a heater.
	var silent forms.SignalA_v1a
	silent.NewForm()
	silent.Value = 22
	if err := tr.setSetPoint(silent); err == nil {
		t.Errorf("a setpoint with no unit was accepted: SetPt = %v", tr.SetPt)
	}
}

// TestSetpointHandlerRefusesAWrongUnit checks the refusal reaches the caller.
// The handler used to discard the error and answer 200, so a sender had no way
// to learn its setpoint had not been taken.
func TestSetpointHandlerRefusesAWrongUnit(t *testing.T) {
	tr := &Traits{SetPt: 20, name: "KitchenHeater", setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}

	body := `{"value":50,"unit":"<http://qudt.org/vocab/unit/PERCENT>","version":"SignalA_v1.0"}`
	req := httptest.NewRequest(http.MethodPut, "/ethermostat/KitchenHeater/setpoint", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	tr.setpt(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a setpoint in the wrong unit, got %d", rec.Code)
	}
	if tr.SetPt != 20 {
		t.Errorf("the refused setpoint was written anyway: SetPt = %v", tr.SetPt)
	}
}

// TestAHeaterNeverRunsWithoutASetpointUnit is the failure the louder one was
// hiding.
//
// The startup check gained a fallback so a configuration without a unit no
// longer refuses to start. adoptUnits did not, so every heater built from that
// configuration kept an empty setpointUnit — and an empty unit is worse than a
// wrong one. AdoptUnit returns immediately when asked to convert into nothing,
// so a setpoint PUT as 68 with unit DEG_F is stored as 68. Against a room at
// 21.5 the error is 46.5, the proportional output saturates, and the heater is
// held on for as long as the system runs. The fatal it replaced was safer.
//
// The unit is also written back into the service, because a consumer converts
// using the unit in the registration record. Fixing only what this system does
// with the number leaves every consumer guessing, and a consumer that guesses
// °C for a °F setpoint is the same accident from the other side.
func TestAHeaterNeverRunsWithoutASetpointUnit(t *testing.T) {
	// A configuration whose setpoint service declares no unit, which is what the
	// README shipped before the units work.
	setpoint := &components.Service{Definition: "setpoint", SubPath: "setpoint"}
	services := components.Services{"setpoint": setpoint}

	traits := &Traits{}
	traits.adoptUnits(services)

	if traits.setpointUnit == "" {
		t.Fatal("the heater runs with no setpoint unit, so a PUT in any unit is " +
			"stored verbatim and the controller saturates")
	}
	if _, ok := usecases.LookupUnit(traits.setpointUnit); !ok {
		t.Errorf("the setpoint unit is %q, which is not a unit the framework can "+
			"convert into", traits.setpointUnit)
	}
	if traits.errorUnit != traits.setpointUnit {
		t.Errorf("the deviation is reported in %q while the setpoint is in %q; the "+
			"deviation is a difference of two temperatures in the setpoint's unit",
			traits.errorUnit, traits.setpointUnit)
	}
	if got := firstDetail(setpoint.Details, "Unit"); got != traits.setpointUnit {
		t.Errorf("the setpoint service registers unit %q while the controller works "+
			"in %q, so a consumer has nothing to convert from", got, traits.setpointUnit)
	}
}

// ── the frost guard ───────────────────────────────────────────────────────────

// heaterWatchingItsPlug builds a controller whose plug is an httptest server, so
// a test can see what it was actually told to do.
func heaterWatchingItsPlug(t *testing.T, blindFor time.Duration, graceMinutes int) (*Traits, func() []bool) {
	t.Helper()

	var mu sync.Mutex
	var commands []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if f, err := usecases.Unpack(body, "application/json"); err == nil {
			if sig, ok := f.(*forms.SignalB_v1a); ok {
				mu.Lock()
				commands = append(commands, sig.Value)
				mu.Unlock()
			}
		}
		// Answer like a real provider. An empty 200 cannot be unpacked, so
		// SetState reports an error and updatePlugState clears the discovered
		// nodes — after which the controller has nowhere to write and every
		// later command silently goes nowhere.
		var echo forms.SignalB_v1a
		echo.NewForm()
		echo.Timestamp = time.Now()
		out, _ := usecases.Pack(&echo, "application/json")
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	t.Cleanup(srv.Close)

	sys := components.NewSystem("ethermostat", context.Background())
	tr := &Traits{
		SetPt: 20, Period: 10, Kp: 5,
		FrostGuard: graceMinutes,
		lastGood:   time.Now().Add(-blindFor),
		name:       "KitchenHeater",
		owner:      &sys,
		cervices: components.Cervices{
			"on_off": {
				Definition: "OnOff",
				Protos:     []string{"http"},
				Mode:       "set",
				// An entry with an empty token means "discovered, and this cloud
				// issued none" — an unauthorized cloud, which is what a test is.
				Nodes: map[string][]components.NodeInfo{
					"plug": {{URL: srv.URL, Tokens: map[string]string{"write": ""}}},
				},
			},
		},
	}
	return tr, func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), commands...)
	}
}

// TestTheFrostGuardHoldsTheHeatOnWhenBlind is the whole point of the thing.
//
// A ZigBee plug returns to off when mains power is restored, and the cottage's
// temperatures come from a cloud API over a domestic line — so after a power cut
// the reading is missing at exactly the moment the plugs are off and the house
// is cooling. Holding the last state, which is what this used to do, means
// holding "off" for ever.
func TestTheFrostGuardHoldsTheHeatOnWhenBlind(t *testing.T) {
	tr, commands := heaterWatchingItsPlug(t, 45*time.Minute, 30)
	tr.frostGuard()

	got := commands()
	if len(got) != 1 {
		t.Fatalf("the plug was commanded %d time(s); want 1", len(got))
	}
	if !got[0] {
		t.Error("the frost guard turned the heat OFF")
	}
}

// TestTheFrostGuardWaitsOutTheGracePeriod: a single failed poll or a brief
// network hiccup must change nothing, or the guard would fight normal control.
func TestTheFrostGuardWaitsOutTheGracePeriod(t *testing.T) {
	tr, commands := heaterWatchingItsPlug(t, 29*time.Minute, 30)
	tr.frostGuard()

	if got := commands(); len(got) != 0 {
		t.Errorf("the plug was commanded %v before the grace period elapsed", got)
	}
	if tr.guarding {
		t.Error("the guard engaged early")
	}
}

// TestTheFrostGuardCanBeTurnedOff: zero is a real answer and means the operator
// does not want it — a summer house with the water drained, say.
func TestTheFrostGuardCanBeTurnedOff(t *testing.T) {
	tr, commands := heaterWatchingItsPlug(t, 100*time.Hour, 0)
	tr.frostGuard()

	if got := commands(); len(got) != 0 {
		t.Errorf("a disabled frost guard commanded the plug: %v", got)
	}
}

// TestTheFrostGuardAnnouncesOnceAndKeepsHolding: at a ten-second period an
// announcement per poll would write six lines a minute for the length of the
// outage and bury the line saying when it began. The holding itself continues.
func TestTheFrostGuardAnnouncesOnceAndKeepsHolding(t *testing.T) {
	tr, commands := heaterWatchingItsPlug(t, 45*time.Minute, 30)

	tr.frostGuard()
	if !tr.guarding {
		t.Fatal("the guard did not engage")
	}
	tr.frostGuard()
	tr.frostGuard()

	got := commands()
	if len(got) != 3 {
		t.Errorf("the plug was commanded %d time(s); want 3 — holding is per poll", len(got))
	}
	for i, on := range got {
		if !on {
			t.Errorf("command %d turned the heat off while guarding", i)
		}
	}
}

// TestAReadingReleasesTheGuard is the half that keeps this from being a heater
// that never switches off again. Recovery needs no code in the guard: the next
// successful read runs the ordinary control law and sets the plug from the
// measurement.
func TestAReadingReleasesTheGuard(t *testing.T) {
	tr, commands := heaterWatchingItsPlug(t, 45*time.Minute, 30)
	tr.frostGuard()
	if !tr.guarding {
		t.Fatal("the guard did not engage")
	}

	// A room comfortably above the setpoint: normal control would switch off.
	temp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var sig forms.SignalA_v1a
		sig.NewForm()
		sig.Value = 24
		sig.Unit = "<http://qudt.org/vocab/unit/DEG_C>"
		sig.Timestamp = time.Now()
		body, _ := usecases.Pack(&sig, "application/json")
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer temp.Close()
	tr.cervices["temperature"] = &components.Cervice{
		Definition: "temperature",
		Protos:     []string{"http"},
		Mode:       "get",
		Nodes: map[string][]components.NodeInfo{
			"sensor": {{URL: temp.URL, Tokens: map[string]string{"read": ""}}},
		},
	}

	tr.processFeedbackLoop()

	if tr.guarding {
		t.Error("the guard was still engaged after a temperature arrived")
	}
	got := commands()
	if len(got) < 2 {
		t.Fatalf("expected a guard command then a control command, got %v", got)
	}
	if got[len(got)-1] {
		t.Error("after a reading of 24 °C against a setpoint of 20 °C the heat is still on — control did not resume")
	}
}
