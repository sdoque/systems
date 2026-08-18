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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// TestGetSetPoint verifies that getSetPoint returns a form with the correct
// Value and Unit fields. The unit is whatever the configuration declared, not a
// constant: that is what lets one controller be commissioned in °C and another
// in °F without touching the code.
func TestGetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 22.5, setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}
	f := tr.getSetPoint()

	if f.Value != 22.5 {
		t.Errorf("expected Value 22.5, got %f", f.Value)
	}
	if f.Unit != "<http://qudt.org/vocab/unit/DEG_C>" {
		t.Errorf("expected the configured unit, got %q", f.Unit)
	}
}

// TestSetSetPoint verifies that setSetPoint updates the SetPt field.
func TestSetSetPoint(t *testing.T) {
	tr := &Traits{SetPt: 20.0, setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}

	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 24.0
	f.Unit = "<http://qudt.org/vocab/unit/DEG_C>"
	f.Timestamp = time.Now()

	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}

	if tr.SetPt != 24.0 {
		t.Errorf("expected SetPt 24.0, got %f", tr.SetPt)
	}
}

// TestGetError verifies that getError returns a form with the correct Value
// and Unit fields.
func TestGetError(t *testing.T) {
	tr := &Traits{deviation: -1.5, errorUnit: "<http://qudt.org/vocab/unit/DEG_C>"}
	f := tr.getError()

	if f.Value != -1.5 {
		t.Errorf("expected Value -1.5, got %f", f.Value)
	}
	if f.Unit != "<http://qudt.org/vocab/unit/DEG_C>" {
		t.Errorf("expected the setpoint's unit, got %q", f.Unit)
	}
}

// TestGetJitter verifies that getJitter returns a form carrying the unit its
// service was configured with.
func TestGetJitter(t *testing.T) {
	tr := &Traits{jitter: 42 * time.Millisecond, jitterUnit: "<http://qudt.org/vocab/unit/MilliSEC>"}
	f := tr.getJitter()

	if f.Unit != "<http://qudt.org/vocab/unit/MilliSEC>" {
		t.Errorf("expected the configured unit, got %q", f.Unit)
	}
	if f.Value != 42.0 {
		t.Errorf("expected Value 42, got %f", f.Value)
	}
}

// TestCalculateOutput is a table-driven test for the P-controller output
// clamped to [0, 100].
func TestCalculateOutput(t *testing.T) {
	cases := []struct {
		name     string
		kp       float64
		diff     float64
		expected float64
	}{
		{"clamp high: Kp=5 diff=10 -> 100", 5, 10, 100},
		{"neutral: Kp=5 diff=0 -> 50", 5, 0, 50},
		{"clamp low: Kp=5 diff=-20 -> 0", 5, -20, 0},
		{"proportional: Kp=1 diff=5 -> 55", 1, 5, 55},
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

// TestSetpt verifies that the setpt handler returns 200 for GET and 404 for
// an unsupported method (DELETE).
func TestSetpt(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{SetPt: 21.0}
		req := httptest.NewRequest(http.MethodGet, "/setpoint", nil)
		w := httptest.NewRecorder()
		tr.setpt(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{SetPt: 21.0}
		req := httptest.NewRequest(http.MethodDelete, "/setpoint", nil)
		w := httptest.NewRecorder()
		tr.setpt(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
}

// TestDiff verifies that the diff handler returns 200 for GET and 404 for an
// unsupported method (DELETE).
func TestDiff(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{deviation: 0.5}
		req := httptest.NewRequest(http.MethodGet, "/deviation", nil)
		w := httptest.NewRecorder()
		tr.diff(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{}
		req := httptest.NewRequest(http.MethodDelete, "/deviation", nil)
		w := httptest.NewRecorder()
		tr.diff(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
}

// TestVariations verifies that the variations handler returns 200 for GET and
// 404 for an unsupported method (DELETE).
func TestVariations(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tr := &Traits{jitter: 5 * time.Millisecond}
		req := httptest.NewRequest(http.MethodGet, "/jitter", nil)
		w := httptest.NewRecorder()
		tr.variations(w, req)

		if w.Result().StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Result().StatusCode)
		}
	})

	t.Run("DELETE returns 404", func(t *testing.T) {
		tr := &Traits{}
		req := httptest.NewRequest(http.MethodDelete, "/jitter", nil)
		w := httptest.NewRecorder()
		tr.variations(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Result().StatusCode)
		}
	})
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

// The thermal error is the difference between the setpoint and the measurement,
// so it must be reported in the setpoint's unit. Configuring the two separately
// would let them disagree, and a °C error against a °F setpoint would look
// plausible for a long time before anyone noticed the valve behaving oddly.
func TestThermalErrorFollowsTheSetpointUnit(t *testing.T) {
	for _, unit := range []string{
		"<http://qudt.org/vocab/unit/DEG_C>",
		"<http://qudt.org/vocab/unit/DEG_F>",
		"Celsius", // a deployment that has not migrated
	} {
		services := components.Services{
			"setpoint":  {Definition: "setpoint", Details: map[string][]string{"Unit": {unit}}},
			"deviation": {Definition: "deviation", Details: map[string][]string{"Measure": {"interval"}}},
			"jitter":    {Definition: "jitter", Details: map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/MilliSEC>"}}},
		}

		tr := &Traits{}
		tr.adoptUnits(services)

		if tr.errorUnit != unit {
			t.Errorf("setpoint in %q gave an error unit of %q", unit, tr.errorUnit)
		}
		// And it must be advertised, or a consumer has nothing to convert from.
		if got := services["deviation"].Details["Unit"]; len(got) != 1 || got[0] != unit {
			t.Errorf("the registered error unit is %v; want %q", got, unit)
		}
		if tr.getError().Unit != unit {
			t.Errorf("the reported error unit is %q; want %q", tr.getError().Unit, unit)
		}
		if tr.getSetPoint().Unit != unit {
			t.Errorf("the reported setpoint unit is %q; want %q", tr.getSetPoint().Unit, unit)
		}
	}
}

// Everything this controller produces has to say what kind of quantity it is, or
// no consumer asking by quantity kind will ever be paired with it — the same
// requirement it places on the sensor it consumes.
func TestProvidedServicesDeclareTheirQuantityKind(t *testing.T) {
	ua := initTemplate()

	for _, definition := range []string{"setpoint", "deviation", "jitter"} {
		serv := findService(ua.GetServices(), definition)
		if serv == nil {
			t.Errorf("%s is missing from the template", definition)
			continue
		}
		if kind := serv.Details["QuantityKind"]; len(kind) != 1 {
			t.Errorf("%s declares QuantityKind %v; a provider without one is unfindable", definition, kind)
		}
	}

	// The error is a difference, and saying so is what stops a consumer applying
	// an offset to it.
	thermalError := findService(ua.GetServices(), "deviation")
	if measure := thermalError.Details["Measure"]; len(measure) != 1 || measure[0] != "interval" {
		t.Errorf("deviation declares Measure %v; want [interval]", measure)
	}
	// And it declares no unit of its own: that is the setpoint's to give.
	if unit := thermalError.Details["Unit"]; len(unit) != 0 {
		t.Errorf("deviation declares its own unit %v; it must follow the setpoint", unit)
	}
}

// The control loop subtracts the measurement from the setpoint, so the two must
// be in one unit. Commissioning the setpoint in °F against a hardcoded °C
// measurement gives 68 − 20 = 48, saturates the valve, and reports figures that
// all look plausible.
func TestMeasurementIsConvertedIntoTheSetpointUnit(t *testing.T) {
	for _, unit := range []string{
		"<http://qudt.org/vocab/unit/DEG_C>",
		"<http://qudt.org/vocab/unit/DEG_F>",
	} {
		tr := &Traits{setpointUnit: unit}
		cer := &components.Cervice{
			Definition: "temperature",
			Details:    map[string][]string{"Unit": {tr.setpointUnit}},
		}

		// A Fahrenheit sensor reporting a room at 20 °C.
		var reading forms.SignalA_v1a
		reading.NewForm()
		reading.Value = 68
		reading.Unit = "<http://qudt.org/vocab/unit/DEG_F>"

		got, err := usecases.NormalizeUnits(cer, &reading)
		if err != nil {
			t.Fatalf("NormalizeUnits: %v", err)
		}
		sig := got.(*forms.SignalA_v1a)
		if sig.Unit != unit {
			t.Errorf("reading arrived as %q; want the setpoint's %q", sig.Unit, unit)
		}
		// 68 °F is 20 °C: the deviation from a 20-in-its-own-unit setpoint must
		// be zero either way.
		want := 20.0
		if unit == "<http://qudt.org/vocab/unit/DEG_F>" {
			want = 68.0
		}
		if diff := sig.Value - want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("reading = %v in %s; want %v", sig.Value, unit, want)
		}
	}
}

// A setpoint arriving in someone else's unit is converted, and one that cannot
// be reconciled is refused rather than written into the loop.
func TestSetpointAdoptsTheConfiguredUnit(t *testing.T) {
	tr := &Traits{setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>"}

	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = 68
	f.Unit = "<http://qudt.org/vocab/unit/DEG_F>"
	if err := tr.setSetPoint(f); err != nil {
		t.Fatalf("setSetPoint: %v", err)
	}
	if diff := tr.SetPt - 20.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("SetPt = %v; want 68 °F expressed as 20 °C", tr.SetPt)
	}

	var wrong forms.SignalA_v1a
	wrong.NewForm()
	wrong.Value = 50
	wrong.Unit = "<http://qudt.org/vocab/unit/PERCENT>"
	if err := tr.setSetPoint(wrong); err == nil {
		t.Error("a percentage was accepted as a temperature setpoint")
	}

	var unnamed forms.SignalA_v1a
	unnamed.NewForm()
	unnamed.Value = 22
	if err := tr.setSetPoint(unnamed); err == nil {
		t.Error("a setpoint with no unit was accepted by a controller that names one")
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
