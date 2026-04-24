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
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "PiCam" {
		t.Errorf("expected Name %q, got %q", "PiCam", ua.Name)
	}

	if ua.Mission != "capture_photographe" {
		t.Errorf("expected Mission %q, got %q", "capture_photographe", ua.Mission)
	}

	if _, ok := ua.ServicesMap["photograph"]; !ok {
		t.Error("expected ServicesMap to have an entry for \"photograph\"")
	}
}

// TestServing_Files verifies that the "files" path returns 200.
func TestServing_Files(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/photographer/PiCam/files/image.jpg", nil)
	serving(tr, w, r, "files")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestServing_InvalidPath verifies that an unknown service path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/photographer/PiCam/unknown", nil)
	serving(tr, w, r, "unknown")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestServing_Photograph_NonGET verifies that non-GET requests to "photograph" return 404.
func TestServing_Photograph_NonGET(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/photographer/PiCam/photograph", nil)
	serving(tr, w, r, "photograph")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
