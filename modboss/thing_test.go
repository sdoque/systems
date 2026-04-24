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
