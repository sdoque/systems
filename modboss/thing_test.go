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
 ***************************************************************************SDG*/

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ------------------------------------- typeOfIO

func TestTypeOfIO(t *testing.T) {
	tests := []struct {
		input string
		want  ioType
	}{
		{"coil", Coil},
		{"discreteInput", DiscreteInput},
		{"holdingRegister", HoldingRegister},
		{"inputRegister", InputRegister},
	}
	for _, tc := range tests {
		got := typeOfIO(tc.input)
		if got != tc.want {
			t.Errorf("typeOfIO(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ------------------------------------- ioType.String

func TestIOTypeString(t *testing.T) {
	tests := []struct {
		io   ioType
		want string
	}{
		{Coil, "Coil"},
		{DiscreteInput, "DiscreteInput"},
		{HoldingRegister, "HoldingRegister"},
		{InputRegister, "InputRegister"},
		{ioType(99), "Unknown"},
	}
	for _, tc := range tests {
		got := tc.io.String()
		if got != tc.want {
			t.Errorf("ioType(%d).String() = %q, want %q", tc.io, got, tc.want)
		}
	}
}

// ------------------------------------- initTemplate

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() == "" {
		t.Error("template name should not be empty")
	}
	if _, ok := ua.GetServices()["access"]; !ok {
		t.Error("ServicesMap should contain an 'access' service")
	}

	tr, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.ServerAddress == "" {
		t.Error("ServerAddress default should not be empty")
	}
	if len(tr.RegisterMap) == 0 {
		t.Error("RegisterMap should have at least one entry")
	}
	if _, ok := tr.RegisterMap["coil"]; !ok {
		t.Error("RegisterMap should contain a 'coil' section")
	}
}

// ------------------------------------- serving dispatcher

func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/modboss/PLC/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A register's mission follows its access mode, because the PLC is an interface
// and each register is the asset behind it. Getting this backwards would hand a
// read-only sensor the mission of an actuator, and any policy permitting writes
// to actuation would then reach it.
func TestMissionForRights(t *testing.T) {
	tests := []struct {
		rights  string
		want    string
		wantErr bool
	}{
		{"ro", "measurement", false},
		{"rw", "actuation", false},
		{"wo", "actuation", false},
		{"RO", "measurement", false}, // register maps are hand-written
		{" rw ", "actuation", false},
		{"", "", true},
		{"read", "", true},
		{"r/w", "", true},
	}

	for _, tc := range tests {
		got, err := missionForRights(tc.rights)
		if tc.wantErr != (err != nil) {
			t.Errorf("missionForRights(%q) error = %v; wantErr %v", tc.rights, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("missionForRights(%q) = %q; want %q", tc.rights, got, tc.want)
		}
	}
}

// Every entry in the shipped template must resolve, so a fresh deployment cannot
// generate a configuration that refuses to start.
func TestTemplateRegisterMapRightsAllResolve(t *testing.T) {
	ua := initTemplate()
	traits, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatalf("template traits are %T; want *Traits", ua.Traits)
	}

	seen := 0
	for kind, registers := range traits.RegisterMap {
		for _, entry := range registers {
			parts := strings.Split(entry, ",")
			if len(parts) < 4 {
				t.Errorf("%s entry %q has %d fields; want at least 4", kind, entry, len(parts))
				continue
			}
			if _, err := missionForRights(parts[2]); err != nil {
				t.Errorf("%s register %q: %v", kind, parts[1], err)
			}
			seen++
		}
	}
	if seen == 0 {
		t.Error("template register map is empty; the system would provide no services")
	}
}

// A register may name its own functional location. The PLC is a cabinet while
// the devices wired to it are spread across the plant, so a failed photo sensor
// has to be locatable from its registration rather than from the cabinet's
// address. Omitting it is valid and means the device is at the PLC.
func TestRegisterLocation(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{"four fields inherit the PLC's location", "00001,MotorRunning,ro,Boolean", ""},
		{"fifth field is the location", "00001,MotorRunning,ro,Boolean,FeedStation", "FeedStation"},
		{"surrounding space is ignored", "00001,MotorRunning,ro,Boolean, DryingOven ", "DryingOven"},
		{"an empty fifth field inherits", "00001,MotorRunning,ro,Boolean,", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := registerLocation(strings.Split(tc.entry, ",")); got != tc.want {
				t.Errorf("registerLocation(%q) = %q; want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// Every entry in the shipped template must still parse, whether or not it
// carries a location, so a fresh deployment generates a usable configuration.
func TestTemplateRegisterMapEntriesParse(t *testing.T) {
	ua := initTemplate()
	traits, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatalf("template traits are %T; want *Traits", ua.Traits)
	}

	located, inherited := 0, 0
	for kind, registers := range traits.RegisterMap {
		for _, entry := range registers {
			parts := strings.Split(entry, ",")
			if len(parts) < 4 {
				t.Errorf("%s entry %q has %d fields; want at least 4", kind, entry, len(parts))
				continue
			}
			if registerLocation(parts) != "" {
				located++
			} else {
				inherited++
			}
		}
	}

	// The template is also documentation: it has to show both forms.
	if located == 0 {
		t.Error("no template entry declares a location; the fifth field is undocumented by example")
	}
	if inherited == 0 {
		t.Error("no template entry omits the location; the four-field form is undocumented by example")
	}
}
