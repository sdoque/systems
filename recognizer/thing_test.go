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
 ***************************************************************************SDG*/

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// minimalJPEG returns a small byte slice that starts with a JPEG magic header.
// It is not a valid image but is sufficient for round-trip tests.
func minimalJPEG() []byte {
	return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
}

// TestInitTemplate verifies name, mission, service registration, and default trait values.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "YOLOv8" {
		t.Errorf("Name: got %q, want %q", ua.Name, "YOLOv8")
	}
	if ua.Mission != "object_detection" {
		t.Errorf("Mission: got %q, want %q", ua.Mission, "object_detection")
	}
	if _, ok := ua.ServicesMap["recognize"]; !ok {
		t.Error("ServicesMap should contain a 'recognize' service")
	}

	tr, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.YOLOServiceURL == "" {
		t.Error("YOLOServiceURL default should not be empty")
	}
	if tr.YOLOModel == "" {
		t.Error("YOLOModel default should not be empty")
	}
}

// TestServing_Recognize_MethodNotAllowed verifies that non-GET requests to recognize return 405.
func TestServing_Recognize_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/recognizer/YOLOv8/recognize", nil)
	serving(tr, w, r, "recognize")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestServing_InvalidService verifies that unknown service paths return 400.
func TestServing_InvalidService(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/recognizer/YOLOv8/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestDownloadImage_Success verifies that downloadImage returns the server's response body.
func TestDownloadImage_Success(t *testing.T) {
	content := minimalJPEG()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(content)
	}))
	defer srv.Close()

	got, err := downloadImage(srv.URL)
	if err != nil {
		t.Fatalf("downloadImage returned error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %v, want %v", got, content)
	}
}

// TestDownloadImage_Unreachable verifies that an unreachable URL returns an error.
func TestDownloadImage_Unreachable(t *testing.T) {
	_, err := downloadImage("http://127.0.0.1:1/no-such-server")
	if err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
}

// TestCallYOLOService_Success verifies the full happy path: multipart POST accepted,
// JSON decoded, base64 annotated image decoded, labels returned.
func TestCallYOLOService_Success(t *testing.T) {
	annotatedBytes := minimalJPEG()
	annotatedB64 := base64.StdEncoding.EncodeToString(annotatedBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/detect" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(yoloResponse{
			Labels:    []string{"person", "chair"},
			Annotated: annotatedB64,
		})
	}))
	defer srv.Close()

	tr := &Traits{YOLOServiceURL: srv.URL, YOLOModel: "yolov8n.pt"}
	labels, annotated, err := tr.callYOLOService(minimalJPEG())
	if err != nil {
		t.Fatalf("callYOLOService returned error: %v", err)
	}
	if len(labels) != 2 || labels[0] != "person" || labels[1] != "chair" {
		t.Errorf("labels: got %v, want [person chair]", labels)
	}
	if string(annotated) != string(annotatedBytes) {
		t.Errorf("annotated bytes mismatch")
	}
}

// TestCallYOLOService_NoDetections verifies that an empty label list is returned cleanly.
func TestCallYOLOService_NoDetections(t *testing.T) {
	annotatedB64 := base64.StdEncoding.EncodeToString(minimalJPEG())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(yoloResponse{Labels: []string{}, Annotated: annotatedB64})
	}))
	defer srv.Close()

	tr := &Traits{YOLOServiceURL: srv.URL}
	labels, _, err := tr.callYOLOService(minimalJPEG())
	if err != nil {
		t.Fatalf("callYOLOService returned error: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("expected empty labels, got %v", labels)
	}
}

// TestCallYOLOService_ServiceDown verifies that an unreachable service URL returns an error.
func TestCallYOLOService_ServiceDown(t *testing.T) {
	tr := &Traits{YOLOServiceURL: "http://127.0.0.1:1"}
	_, _, err := tr.callYOLOService(minimalJPEG())
	if err == nil {
		t.Error("expected error when YOLO service is unreachable, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error should mention unreachable, got: %v", err)
	}
}

// TestCallYOLOService_NonOKResponse verifies that a non-200 upstream response returns an error.
func TestCallYOLOService_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := &Traits{YOLOServiceURL: srv.URL}
	_, _, err := tr.callYOLOService(minimalJPEG())
	if err == nil {
		t.Error("expected error for non-200 response, got nil")
	}
}

// TestCallYOLOService_ServiceError verifies that a non-empty error field in the JSON response
// is propagated as a Go error.
func TestCallYOLOService_ServiceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(yoloResponse{Error: "could not decode image"})
	}))
	defer srv.Close()

	tr := &Traits{YOLOServiceURL: srv.URL}
	_, _, err := tr.callYOLOService(minimalJPEG())
	if err == nil {
		t.Error("expected error from service error field, got nil")
	}
	if !strings.Contains(err.Error(), "could not decode image") {
		t.Errorf("error should contain service message, got: %v", err)
	}
}

// TestCallYOLOService_InvalidBase64 verifies that a malformed base64 annotated field returns an error.
func TestCallYOLOService_InvalidBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(yoloResponse{Labels: []string{"cat"}, Annotated: "!!!not-valid-base64!!!"})
	}))
	defer srv.Close()

	tr := &Traits{YOLOServiceURL: srv.URL}
	_, _, err := tr.callYOLOService(minimalJPEG())
	if err == nil {
		t.Error("expected error for invalid base64 annotated field, got nil")
	}
}

// TestTraitsJSON verifies that Traits fields round-trip correctly through JSON unmarshalling.
func TestTraitsJSON(t *testing.T) {
	raw := []byte(`{
		"functionalLocation": "Entrance",
		"yoloServiceURL":     "http://localhost:5001",
		"yoloModel":          "yolov8s.pt"
	}`)

	var tr Traits
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if tr.FunctionalLocation != "Entrance" {
		t.Errorf("FunctionalLocation: got %q", tr.FunctionalLocation)
	}
	if tr.YOLOServiceURL != "http://localhost:5001" {
		t.Errorf("YOLOServiceURL: got %q", tr.YOLOServiceURL)
	}
	if tr.YOLOModel != "yolov8s.pt" {
		t.Errorf("YOLOModel: got %q", tr.YOLOModel)
	}
}
