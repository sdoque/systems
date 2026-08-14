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
	"github.com/sdoque/mbaigo/usecases"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 42.5, setpointUnit: "<http://qudt.org/vocab/unit/PERCENT>"}
	f := tr.getSetPoint()

	if f.Value != 42.5 {
		t.Errorf("getSetPoint().Value = %v, want 42.5", f.Value)
	}
	if f.Unit != "<http://qudt.org/vocab/unit/PERCENT>" {
		t.Errorf("getSetPoint().Unit = %q, want %q", f.Unit, "<http://qudt.org/vocab/unit/PERCENT>")
	}
}

func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 10.0, setpointUnit: "<http://qudt.org/vocab/unit/PERCENT>"}

	var f forms.SignalA_v1a
	f.Value = 55.0
	f.Unit = "<http://qudt.org/vocab/unit/PERCENT>"
	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}

	if tr.SetPt != 55.0 {
		t.Errorf("SetPt after setSetPoint(55) = %v, want 55.0", tr.SetPt)
	}
}

func TestGetError(t *testing.T) {
	tr := &Traits{deviation: 7.3, errorUnit: "<http://qudt.org/vocab/unit/PERCENT>"}
	f := tr.getError()

	if f.Value != 7.3 {
		t.Errorf("getError().Value = %v, want 7.3", f.Value)
	}
	if f.Unit != "<http://qudt.org/vocab/unit/PERCENT>" {
		t.Errorf("getError().Unit = %q, want %q", f.Unit, "<http://qudt.org/vocab/unit/PERCENT>")
	}
}

func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 250 * time.Millisecond, jitterUnit: "<http://qudt.org/vocab/unit/MilliSEC>"}
	f := tr.getJitter()

	if f.Value != 250 {
		t.Errorf("getJitter().Value = %v, want 250", f.Value)
	}
	if f.Unit != "<http://qudt.org/vocab/unit/MilliSEC>" {
		t.Errorf("getJitter().Unit = %q, want %q", f.Unit, "<http://qudt.org/vocab/unit/MilliSEC>")
	}
}

func TestCalculateOutput(t *testing.T) {
	// Kp=2, Lambda=1, Ki=0, Period=1 (second)
	// decay = exp(-1*1/1) = exp(-1) ≈ 0.368
	// integral updated each call, but iTerm = Ki*integral = 0 always
	// output = pTerm = Kp * levelDiff, clamped to [0,100]

	tr := &Traits{Kp: 2, Lambda: 1, Ki: 0, Period: 1}

	// diff=10 → P-only = 2*10 = 20
	out := tr.calculateOutput(10)
	if out != 20 {
		t.Errorf("calculateOutput(10) = %v, want 20", out)
	}

	// Reset integral for independent sub-tests
	tr.integral = 0

	// diff=100 → 2*100=200, clamped to 100
	out = tr.calculateOutput(100)
	if out != 100 {
		t.Errorf("calculateOutput(100) = %v, want 100 (clamped)", out)
	}

	tr.integral = 0

	// diff=-100 → 2*(-100)=-200, clamped to 0
	out = tr.calculateOutput(-100)
	if out != 0 {
		t.Errorf("calculateOutput(-100) = %v, want 0 (clamped)", out)
	}
}

func TestSetpt(t *testing.T) {
	tr := &Traits{SetPt: 30.0}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
	rr := httptest.NewRecorder()
	tr.setpt(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("setpt GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
	rr = httptest.NewRecorder()
	tr.setpt(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("setpt DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestDiff(t *testing.T) {
	tr := &Traits{deviation: 5.0}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/deviation", nil)
	rr := httptest.NewRecorder()
	tr.diff(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("diff GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/deviation", nil)
	rr = httptest.NewRecorder()
	tr.diff(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("diff DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestVariations(t *testing.T) {
	tr := &Traits{jitter: 100 * time.Millisecond}

	// GET → 200
	req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
	rr := httptest.NewRecorder()
	tr.variations(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("variations GET status = %d, want %d", rr.Code, http.StatusOK)
	}

	// DELETE → 404
	req = httptest.NewRequest(http.MethodDelete, "/jitter", nil)
	rr = httptest.NewRecorder()
	tr.variations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("variations DELETE status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// The mission the authorizer evaluates is the service's, not the asset's. A
// controller is one asset whose services differ in kind: writing the setpoint
// reconfigures the loop, while the error and jitter readings only observe it.
// Collapsing them onto the asset's "control" would mean any policy letting a
// consumer move a setpoint also let it write everything else here.
func TestInitTemplateServiceMissions(t *testing.T) {
	ua := initTemplate()

	if ua.Mission != "control" {
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
		if got := components.EffectiveMission(ua, serv); got != mission {
			t.Errorf("service %q effective mission = %q; want %q", subPath, got, mission)
		}
	}
}

// TestSetpointAdoptsTheConfiguredUnit is the defect this test was written for:
// setSetPoint wrote f.Value into the control loop without ever reading f.Unit,
// so a fraction offered as a level became a percentage. 0.8 of a tank arrived as
// 0.8 % and the pump ran until the tank was empty.
func TestSetpointAdoptsTheConfiguredUnit(t *testing.T) {
	percent := "<http://qudt.org/vocab/unit/PERCENT>"

	// A ratio expressed as a plain number is 100 times the percentage.
	tr := &Traits{SetPt: 10, setpointUnit: percent}
	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 0.8
	f.Unit = "<http://qudt.org/vocab/unit/NUM>"
	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}
	if tr.SetPt != 80 {
		t.Errorf("0.8 as a ratio is 80 %%, got %v", tr.SetPt)
	}

	// A temperature is not a level, however plausible the number looks.
	var wrong forms.SignalA_v1a
	wrong.NewForm()
	wrong.Value = 21
	wrong.Unit = "<http://qudt.org/vocab/unit/DEG_C>"
	if err := tr.setSetPoint(wrong); err == nil {
		t.Errorf("a temperature was accepted as a tank level: SetPt = %v", tr.SetPt)
	}

	// A bare number says nothing, and this loop drives a pump.
	var silent forms.SignalA_v1a
	silent.NewForm()
	silent.Value = 55
	if err := tr.setSetPoint(silent); err == nil {
		t.Errorf("a setpoint with no unit was accepted: SetPt = %v", tr.SetPt)
	}
}

// TestSetpointHandlerRefusesAWrongUnit checks the refusal reaches the caller.
// The handler used to log the error and carry on, so a rejected setpoint was
// answered with 200 and the sender had no way to know.
func TestSetpointHandlerRefusesAWrongUnit(t *testing.T) {
	tr := &Traits{SetPt: 20, setpointUnit: "<http://qudt.org/vocab/unit/PERCENT>"}

	body := `{"value":21,"unit":"<http://qudt.org/vocab/unit/DEG_C>","version":"SignalA_v1.0"}`
	req := httptest.NewRequest(http.MethodPut, "/leveler/Leveler_1/setpoint", strings.NewReader(body))
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

// TestASetpointWithNoConfiguredUnitFallsBack is follow-up finding N8.
//
// Configure builds a unit asset entirely from systemconfig.json and never merges
// the template into it, so a services array written without a "details" block
// leaves the setpoint with no unit — and the startup check then refused to run,
// saying the setpoint was configured in "". The ethermostat README documented
// exactly such an array, so an operator copying it got a system that would not
// start and no hint that the fix was to add a details block.
func TestASetpointWithNoConfiguredUnitFallsBack(t *testing.T) {
	tr := &Traits{}
	tr.adoptUnits(components.Services{
		"setpoint":  {Definition: "setpoint", SubPath: "setpoint"},
		"deviation": {Definition: "deviation", SubPath: "deviation"},
		"jitter":    {Definition: "jitter", SubPath: "jitter"},
	})

	if tr.setpointUnit == "" {
		t.Fatal("no unit was resolved, so the startup check would refuse to run")
	}
	if _, ok := usecases.LookupUnit(tr.setpointUnit); !ok {
		t.Errorf("fell back to %q, which the framework cannot resolve", tr.setpointUnit)
	}
	if tr.setpointUnit != templateSetpointUnit() {
		t.Errorf("fell back to %q, want the template's %q", tr.setpointUnit, templateSetpointUnit())
	}
	// The deviation follows the setpoint, so it must have been given one too.
	if tr.errorUnit != tr.setpointUnit {
		t.Errorf("the deviation reports %q while the setpoint is %q", tr.errorUnit, tr.setpointUnit)
	}
}

// A unit that is present but unresolvable is a statement the operator made and
// got wrong, and stays fatal — which the startup check does. This records that
// the fallback did not swallow it.
func TestAConfiguredUnitIsNotOverriddenByTheTemplate(t *testing.T) {
	tr := &Traits{}
	tr.adoptUnits(components.Services{
		"setpoint": {Definition: "setpoint", SubPath: "setpoint",
			Details: map[string][]string{"Unit": {"furlongs per fortnight"}}},
	})
	if tr.setpointUnit != "furlongs per fortnight" {
		t.Errorf("the configured unit became %q; the template must not override what an operator wrote", tr.setpointUnit)
	}
	if _, ok := usecases.LookupUnit(tr.setpointUnit); ok {
		t.Error("that unit resolved, so this test proves nothing")
	}
}
