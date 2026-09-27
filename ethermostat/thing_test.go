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

// A sensor that says it is in the room wins.
func TestSelectTempNode_ExactMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Kälkholmen (Indoor)"}}},
			{URL: "http://host/meteorologue/KitchenModule/temperature", Details: map[string][]string{"FunctionalLocation": {"Kitchen (Indoor)"}}},
		},
	}
	sysNode, ni, why, ok := selectTempNode(nodes, "Kitchen", "")
	if !ok {
		t.Fatalf("expected a match, got none (%s)", why)
	}
	if sysNode != "meteorologue" || ni.URL != "http://host/meteorologue/KitchenModule/temperature" {
		t.Errorf("chose %s at %s", sysNode, ni.URL)
	}
}

// Failing that, a sensor named after the room.
func TestSelectTempNode_ModuleNameMatch(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"}, "ModuleName": {"Indoor"}}},
			{URL: "http://host/meteorologue/IndoorModule2/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"}, "ModuleName": {"Bathroom"}}},
		},
	}
	_, ni, _, ok := selectTempNode(nodes, "Bathroom", "")
	if !ok || ni.URL != "http://host/meteorologue/IndoorModule2/temperature" {
		t.Errorf("Bathroom got %s (ok=%v)", ni.URL, ok)
	}
}

// A room with no thermometer of its own is refused, not given somebody else's.
// This is what drove three of the cottage's heaters from the bathroom.
func TestSelectTempNode_NoSensorIsRefused(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{
				"FunctionalLocation": {"Kälkholmen (Indoor)"}, "ModuleName": {"Indoor"}}},
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{
				"ModuleName": {"Outdoor"}}},
		},
	}
	_, _, why, ok := selectTempNode(nodes, "Kitchen", "")
	if ok {
		t.Fatal("a kitchen with no thermometer was given one anyway")
	}
	if !strings.Contains(why, "Kitchen") {
		t.Errorf("the refusal does not say which room: %q", why)
	}
}

// The operator may name the sensor a room should use.
func TestSelectTempNode_Configured(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/IndoorModule/temperature", Details: map[string][]string{"ModuleName": {"Indoor"}}},
			{URL: "http://host/meteorologue/OutdoorModule/temperature", Details: map[string][]string{"ModuleName": {"Outdoor"}}},
		},
	}
	_, ni, why, ok := selectTempNode(nodes, "Kitchen", "IndoorModule")
	if !ok || !strings.Contains(ni.URL, "IndoorModule") {
		t.Fatalf("configured sensor gave %s (ok=%v, %s)", ni.URL, ok, why)
	}
	if !strings.Contains(why, "configured") {
		t.Errorf("the reason does not say it was configured: %q", why)
	}
	// A name that is not on offer is refused rather than approximated.
	if _, _, why, ok := selectTempNode(nodes, "Kitchen", "HallwayModule"); ok {
		t.Error("a configured sensor that does not exist was substituted")
	} else if !strings.Contains(why, "HallwayModule") {
		t.Errorf("the refusal does not name what was configured: %q", why)
	}
}

// The same cloud must give the same answer twice. Ranging over the map made
// this a coin toss, and the coin was tossed again at every restart.
func TestSelectTempNode_IsDeterministic(t *testing.T) {
	nodes := map[string][]components.NodeInfo{
		"meteorologue": {
			{URL: "http://host/meteorologue/A/temperature", Details: map[string][]string{"ModuleName": {"Kitchen north"}}},
			{URL: "http://host/meteorologue/B/temperature", Details: map[string][]string{"ModuleName": {"Kitchen south"}}},
		},
		"weatherman": {
			{URL: "http://host/weatherman/C/temperature", Details: map[string][]string{"ModuleName": {"Kitchen west"}}},
		},
	}
	_, first, _, ok := selectTempNode(nodes, "Kitchen", "")
	if !ok {
		t.Fatal("no match")
	}
	for i := 0; i < 200; i++ {
		if _, again, _, _ := selectTempNode(nodes, "Kitchen", ""); again.URL != first.URL {
			t.Fatalf("run %d chose %s, the first run chose %s", i, again.URL, first.URL)
		}
	}
}

// TestSelectTempNode_Empty verifies that an empty nodes map returns not-found.
func TestSelectTempNode_Empty(t *testing.T) {
	_, _, _, ok := selectTempNode(map[string][]components.NodeInfo{}, "Kitchen", "")
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
		// SetState would report an error for every command.
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
					"plug": {{URL: srv.URL + "/beekeeper/KitchenHeater/on_off", Tokens: map[string]string{"write": ""}}},
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

// A controller must not drive a device it cannot name. Discovery hands over a
// name and an address together; when they disagree, something has gone wrong
// between the registry and here, and the wrong device is about to be switched.
func TestURLNames(t *testing.T) {
	cases := []struct {
		url, name string
		want      bool
	}{
		{"https://h:30185/beekeeper/BathroomHeater/on_off", "BathroomHeater", true},
		{"https://h:30185/beekeeper/BathroomLight/on_off", "BathroomHeater", false},
		{"https://h:30185/beekeeper/DiningroomHeater/on_off", "BathroomHeater", false},
		// A provider normalizes a name into its path.
		{"https://h:30185/beekeeper/lumi_remote_b28ac1/on_off", "lumi.remote.b28ac1", true},
		{"https://h:30185/beekeeper/Bathroom_Heater/on_off", "Bathroom Heater", true},
		// Case is not what tells two devices apart.
		{"https://h:30185/beekeeper/bathroomheater/on_off", "BathroomHeater", true},
		{"nonsense", "BathroomHeater", false},
	}
	for _, c := range cases {
		if got := urlNames(c.url, c.name); got != c.want {
			t.Errorf("urlNames(%q, %q) = %v, want %v", c.url, c.name, got, c.want)
		}
	}
}

// ── bindings by name ──────────────────────────────────────────────────────────

// cottage is a beekeeper and a meteorologue on one test server. It records
// which paths were switched, and offers what a discovery would.
type cottage struct {
	srv      *httptest.Server
	mu       sync.Mutex
	switched []string
}

func newCottage(t *testing.T) *cottage {
	t.Helper()
	c := &cottage{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []byte
		if r.Method == http.MethodPut {
			c.mu.Lock()
			c.switched = append(c.switched, r.URL.Path)
			c.mu.Unlock()
			var echo forms.SignalB_v1a
			echo.NewForm()
			echo.Timestamp = time.Now()
			out, _ = usecases.Pack(&echo, "application/json")
		} else {
			var sig forms.SignalA_v1a
			sig.NewForm()
			sig.Value = 15 // cold: the control law wants the heat on
			sig.Unit = "<http://qudt.org/vocab/unit/DEG_C>"
			sig.Timestamp = time.Now()
			out, _ = usecases.Pack(&sig, "application/json")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *cottage) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.switched...)
}

// plug is a beekeeper OnOff node as discovery returns it.
func (c *cottage) plug(name string) components.NodeInfo {
	return components.NodeInfo{
		URL:     c.srv.URL + "/beekeeper/" + name + "/on_off",
		Details: map[string][]string{"DisplayName": {name}},
		Tokens:  map[string]string{"write": ""},
	}
}

func (c *cottage) thermometer(module, location string) components.NodeInfo {
	return components.NodeInfo{
		URL:     c.srv.URL + "/meteorologue/" + module + "/temperature",
		Details: map[string][]string{"ModuleName": {module}, "FunctionalLocation": {location}},
		Tokens:  map[string]string{"read": ""},
	}
}

// offering answers every discovery with the given nodes of that definition.
func offering(nodes map[string][]components.NodeInfo) func(*components.Cervice, string) error {
	return func(cer *components.Cervice, _ string) error {
		for _, ni := range nodes[cer.Definition] {
			cer.Nodes["node"] = append(cer.Nodes["node"], ni)
		}
		return nil
	}
}

func bathroomHeater(c *cottage, plugs, thermometers []components.NodeInfo) *Traits {
	sys := components.NewSystem("ethermostat", context.Background())
	return &Traits{
		SetPt: 20, Period: 10, Kp: 5, FrostGuard: 30,
		lastGood: time.Now(),
		name:     "BathroomHeater",
		location: "Bathroom",
		owner:    &sys,
		discover: offering(map[string][]components.NodeInfo{"OnOff": plugs, "temperature": thermometers}),
		cervices: components.Cervices{
			"on_off":      {Definition: "OnOff", Protos: []string{"http"}, Mode: "set", Nodes: map[string][]components.NodeInfo{}},
			"temperature": {Definition: "temperature", Protos: []string{"http"}, Mode: "get", Nodes: map[string][]components.NodeInfo{}},
		},
	}
}

// The cottage, 27 September: one refused call emptied the binding, and the next
// call asked for "an OnOff" and was given the bathroom light. A lost binding is
// found again by name, whatever else is offered — and offered first.
func TestALostPlugIsFoundAgainByName(t *testing.T) {
	c := newCottage(t)
	tr := bathroomHeater(c, []components.NodeInfo{c.plug("BathroomLight"), c.plug("BathroomHeater"), c.plug("KitchenHeater")}, nil)

	tr.updatePlugState(true)

	if got := c.paths(); len(got) != 1 || got[0] != "/beekeeper/BathroomHeater/on_off" {
		t.Errorf("switched %v, want only the bathroom heater", got)
	}
}

// A binding that is somebody else's device is dropped before it is used.
func TestACrossedPlugIsNotSwitched(t *testing.T) {
	c := newCottage(t)
	tr := bathroomHeater(c, []components.NodeInfo{c.plug("BathroomHeater")}, nil)
	pin(tr.cervices["on_off"], "node", c.plug("BathroomLight"))

	tr.updatePlugState(true)

	if got := c.paths(); len(got) != 1 || got[0] != "/beekeeper/BathroomHeater/on_off" {
		t.Errorf("switched %v, want only the bathroom heater", got)
	}
}

// When its own plug is not offered, a controller switches nothing at all.
func TestAMissingPlugSwitchesNothing(t *testing.T) {
	c := newCottage(t)
	tr := bathroomHeater(c, []components.NodeInfo{c.plug("BathroomLight"), c.plug("KitchenHeater")}, nil)

	tr.updatePlugState(true)

	if got := c.paths(); len(got) != 0 {
		t.Errorf("switched %v while the bathroom heater's plug was not offered", got)
	}
	if bound(tr.cervices["on_off"]) {
		t.Error("bound to a plug that is not the bathroom heater's")
	}
}

// A lost thermometer is chosen again by the same rule, not by whichever the
// orchestrator likes: the outdoor module, offered first, would keep a heater
// on all winter.
func TestALostThermometerIsChosenAgainByTheRule(t *testing.T) {
	c := newCottage(t)
	tr := bathroomHeater(c,
		[]components.NodeInfo{c.plug("BathroomHeater")},
		[]components.NodeInfo{c.thermometer("Outdoor", "Outdoor"), c.thermometer("BathroomModule", "Bathroom")})

	if err := tr.bindThermometer(); err != nil {
		t.Fatal(err)
	}
	for _, nodes := range tr.cervices["temperature"].Nodes {
		if len(nodes) != 1 || !strings.Contains(nodes[0].URL, "/BathroomModule/") {
			t.Errorf("reading %v, want the bathroom module", nodes)
		}
	}
}

// After a power cut the thermometers come from the Netatmo cloud and are the
// last thing back. A controller built without one heats once the grace period
// has passed, and starts controlling when one appears.
func TestAControllerWithoutAThermometerHeatsThenControls(t *testing.T) {
	c := newCottage(t)
	tr := bathroomHeater(c, []components.NodeInfo{c.plug("BathroomHeater")}, nil)
	tr.lastGood = time.Now().Add(-31 * time.Minute)

	tr.processFeedbackLoop()
	if !tr.guarding {
		t.Fatal("no thermometer for 31 minutes and the frost guard did not engage")
	}
	if got := c.paths(); len(got) != 1 {
		t.Fatalf("switched %v, want the heater once", got)
	}

	tr.discover = offering(map[string][]components.NodeInfo{
		"OnOff":       {c.plug("BathroomHeater")},
		"temperature": {c.thermometer("BathroomModule", "Bathroom")},
	})
	tr.processFeedbackLoop()
	if tr.guarding {
		t.Error("a thermometer appeared and the guard is still holding")
	}
}
