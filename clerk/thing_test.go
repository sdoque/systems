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
 *   Franziska Sievert - initial implementation
 *   Jan A. van Deventer, Luleå - modernized for current mbaigo
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestTraits returns a Traits with nil owner (no Orchestrator needed).
func newTestTraits() *Traits {
	return &Traits{}
}

// TestOrdersHandler_GET_ServesPage verifies that GET without ?id returns the HTML page.
func TestOrdersHandler_GET_ServesPage(t *testing.T) {
	tr := newTestTraits()
	req := httptest.NewRequest(http.MethodGet, "/clerk/product/orders", nil)
	w := httptest.NewRecorder()
	tr.ordersHandler(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
	ct := w.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "Pen Holder Orders") {
		t.Error("page body does not contain expected title")
	}
}

// TestOrdersHandler_InvalidMethod verifies 405 for DELETE.
func TestOrdersHandler_InvalidMethod(t *testing.T) {
	tr := newTestTraits()
	req := httptest.NewRequest(http.MethodDelete, "/clerk/product/orders", nil)
	w := httptest.NewRecorder()
	tr.ordersHandler(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestServing_InvalidPath verifies 400 for an unknown service path.
func TestServing_InvalidPath(t *testing.T) {
	tr := newTestTraits()
	req := httptest.NewRequest(http.MethodGet, "/clerk/product/unknown", nil)
	w := httptest.NewRecorder()
	serving(tr, w, req, "unknown")
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

// TestSubmitOrder_ValidationHeight verifies that height > 21 is rejected server-side.
func TestSubmitOrder_ValidationHeight(t *testing.T) {
	tr := newTestTraits()
	order := &PenHolderOrder_v1{
		OrderNumber: 0, Name: "Test", Email: "t@t.com",
		Height: 25, Depth: 3, Roughness: 63, PeppolID: "",
		OrderedTimestamp: time.Now(), Version: "PenHolderOrder_v1",
	}
	order.NewForm()
	body, _ := json.Marshal(order)
	req := httptest.NewRequest(http.MethodPost, "/clerk/product/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	tr.submitOrder(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for height > 21, got %d", w.Result().StatusCode)
	}
}

// TestSubmitOrder_ValidationDepth verifies that depth > height is rejected server-side.
func TestSubmitOrder_ValidationDepth(t *testing.T) {
	tr := newTestTraits()
	order := &PenHolderOrder_v1{
		OrderNumber: 0, Name: "Test", Email: "t@t.com",
		Height: 10, Depth: 15, Roughness: 63, PeppolID: "",
		OrderedTimestamp: time.Now(), Version: "PenHolderOrder_v1",
	}
	order.NewForm()
	body, _ := json.Marshal(order)
	req := httptest.NewRequest(http.MethodPost, "/clerk/product/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	tr.submitOrder(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for depth > height, got %d", w.Result().StatusCode)
	}
}

// TestSubmitOrder_BadBody verifies that a malformed body returns 400.
func TestSubmitOrder_BadBody(t *testing.T) {
	tr := newTestTraits()
	req := httptest.NewRequest(http.MethodPost, "/clerk/product/orders",
		bytes.NewReader([]byte("not json at all")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	tr.submitOrder(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

// TestPenHolderOrder_v1_FormVersion verifies NewForm sets the version field.
func TestPenHolderOrder_v1_FormVersion(t *testing.T) {
	var f PenHolderOrder_v1
	f.NewForm()
	if f.FormVersion() != "PenHolderOrder_v1" {
		t.Errorf("expected PenHolderOrder_v1, got %q", f.FormVersion())
	}
}

// TestOrderPage_ContainsFormElements verifies the embedded HTML has the key form fields.
func TestOrderPage_ContainsFormElements(t *testing.T) {
	for _, want := range []string{
		`id="name"`, `id="email"`, `id="height"`, `id="depth"`,
		`id="roughness"`, `id="line"`, `id="peppol"`,
		`id="lookupId"`, `id="lookupEmail"`,
		`Place Order`, `Look Up Order`,
	} {
		if !strings.Contains(orderPage, want) {
			t.Errorf("orderPage missing expected element: %q", want)
		}
	}
}

// TestOrdersHandler_GET_LookupRequiresBoth verifies 400 when only one lookup param is provided.
func TestOrdersHandler_GET_LookupRequiresBoth(t *testing.T) {
	tr := newTestTraits()

	// id only
	req := httptest.NewRequest(http.MethodGet, "/clerk/product/orders?id=1", nil)
	w := httptest.NewRecorder()
	tr.ordersHandler(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for id-only lookup, got %d", w.Result().StatusCode)
	}

	// email only
	req = httptest.NewRequest(http.MethodGet, "/clerk/product/orders?email=x@x.com", nil)
	w = httptest.NewRecorder()
	tr.ordersHandler(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for email-only lookup, got %d", w.Result().StatusCode)
	}
}
