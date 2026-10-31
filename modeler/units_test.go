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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	degC     = "<http://qudt.org/vocab/unit/DEG_C>"
	degF     = "<http://qudt.org/vocab/unit/DEG_F>"
	tempKind = "<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"
)

// serveModel stands a system up as its /smodel endpoint.
func serveModel(t *testing.T, sys *components.System) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usecases.SModeling(w, r, sys)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func testSystem(name, host, ip string, port int, assets ...*components.UnitAsset) *components.System {
	sys := &components.System{
		Name: name,
		Husk: &components.Husk{
			Host:      &components.HostingDevice{Name: host, IPAddresses: []string{ip}},
			ProtoPort: map[string]int{"http": port},
			Details:   map[string][]string{},
		},
		UAssets: make(map[string]*components.UnitAsset),
		Mutex:   &sync.Mutex{},
	}
	for _, ua := range assets {
		sys.UAssets[ua.Name] = ua
	}
	return sys
}

// A kitchen thermometer in °C, and a thermostat that reads it in °F, reports
// its deviation as a difference, and drives a valve in percent: the cases the
// unit mapping has to keep apart, assembled into one cloud model.
func TestTheCloudModelCarriesUnits(t *testing.T) {
	thermometer := testSystem("ds18b20", "kitchenpi", "192.0.2.10", 20150, &components.UnitAsset{
		Name:    "Kitchen",
		Mission: components.MissionMeasurement,
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{"temperature": {
			Definition: "temperature", SubPath: "temperature",
			Details: map[string][]string{"Unit": {degC}, "QuantityKind": {tempKind}},
		}},
	})
	thermostat := testSystem("thermostat", "kitchenpi", "192.0.2.10", 20151, &components.UnitAsset{
		Name:    "controller",
		Mission: components.MissionControl,
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{"diff": {
			Definition: "diff", SubPath: "diff",
			Details: map[string][]string{"Unit": {degF}, "QuantityKind": {tempKind}, "Measure": {"interval"}},
		}},
		CervicesMap: components.Cervices{
			"temperature": {
				Definition: "temperature", Mode: "get",
				Details: map[string][]string{"Unit": {degF}, "QuantityKind": {tempKind}},
				Nodes: map[string][]components.NodeInfo{"ds18b20": {{
					URL: "http://192.0.2.10:20150/ds18b20/Kitchen/temperature",
				}}},
			},
			"valve": {
				Definition: "position", Mode: "set",
				Details: map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/PERCENT>"}},
				Nodes:   map[string][]components.NodeInfo{},
			},
		},
	})

	registrar := newMockRegistrar(systemRecordListJSON([]string{serveModel(t, thermometer), serveModel(t, thermostat)}))
	defer registrar.Close()
	w := httptest.NewRecorder()
	newTestTraits(registrar.URL, "kitchenCloud").assembleModel(w)
	model := w.Body.String()

	// SysML v2 users can load this to check it: MAF_SAMPLE=path go test -run CarriesUnits
	if path := os.Getenv("MAF_SAMPLE"); path != "" {
		if err := os.WriteFile(path, []byte(model), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, want := range []string{
		"port def 'Temperature' :> ServicePort;",
		// the thermometer reads on the Celsius scale
		"attribute :>> value : Quantities::ScalarQuantityValue { :>> mRef = SI::'°C_abs'; }",
		// the thermostat asks for Fahrenheit, which is where the framework converts
		"attribute :>> value : Quantities::ScalarQuantityValue { :>> mRef = USCustomaryUnits::'°F_abs'; }",
		// its deviation is a difference, not a reading
		"attribute :>> value : ISQ::TemperatureDifferenceValue { :>> mRef = USCustomaryUnits::'°F'; }",
		"attribute :>> value : MeasurementReferences::DimensionOneValue { :>> mRef = mAF::'%'; }",
		`attribute :>> qudtUnit = "http://qudt.org/vocab/unit/DEG_C";`,
		"attribute redefines mission = Mission::'control';",
		`attribute redefines functionalLocation = "Kitchen";`,
	} {
		if !strings.Contains(model, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(model, "port def 'Temperature'") != 1 {
		t.Errorf("the Temperature port is defined %d times", strings.Count(model, "port def 'Temperature'"))
	}
}
