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

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

func TestSegment(t *testing.T) {
	cases := map[string]string{
		"https://host:30187/painter/canvas/view":  "view",
		"https://host:30187/painter/canvas/model": "model",
		"http://host:20105/kgrapher/a/cloudgraph": "cloudgraph",
		"https://host:30187/view/":                "view",
		"":                                        "",
	}
	for in, want := range cases {
		if got := segment(in); got != want {
			t.Errorf("segment(%q) = %q, want %q", in, got, want)
		}
	}
}

// The Host check is what stops a page in the operator's own browser from being
// pointed at this port by a name that resolves to 127.0.0.1. Binding loopback
// does not close that; only refusing an unexpected name does.
func TestLoopback(t *testing.T) {
	allowed := []string{"127.0.0.1:8190", "localhost:8190", "[::1]:8190", "127.0.0.1", "localhost"}
	for _, h := range allowed {
		if !loopback(h) {
			t.Errorf("loopback(%q) = false, want true", h)
		}
	}
	refused := []string{"envoy.example.com:8190", "10.0.0.33:8190", "attacker.test", "0.0.0.0:8190"}
	for _, h := range refused {
		if loopback(h) {
			t.Errorf("loopback(%q) = true, want false", h)
		}
	}
}

// oneRouteProxy publishes a single route backed by a stub provider.
func oneRouteProxy(t *testing.T, body string) (*proxy, *httptest.Server) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(usecases.TokenHeader) == "" {
			t.Errorf("provider was called without a token header")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(body))
	}))
	t.Cleanup(provider.Close)

	cer := &components.Cervice{
		Definition: "view",
		Nodes: map[string][]components.NodeInfo{
			"painter": {{
				URL:    provider.URL + "/painter/canvas/view",
				Tokens: map[string]string{"read": "a-read-token"},
			}},
		},
	}
	return &proxy{
		routes: map[string]*route{"view": {definition: "view", cer: cer}},
		first:  "view",
	}, provider
}

func TestServeHTTPForwardsTheRead(t *testing.T) {
	p, _ := oneRouteProxy(t, "<svg>the cloud</svg>")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8190/view", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<svg>the cloud</svg>" {
		t.Errorf("body = %q, want the provider's bytes verbatim", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html" {
		t.Errorf("Content-Type = %q, want the provider's", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A viewer that forwarded a PUT would be an unattested way to actuate: this
// binary holds a certificate the cloud issued, and a browser does not.
func TestServeHTTPRefusesAnythingButGET(t *testing.T) {
	p, _ := oneRouteProxy(t, "x")
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "http://127.0.0.1:8190/view", nil)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("%s: Allow = %q, want GET", method, got)
		}
	}
}

func TestServeHTTPRefusesAForeignHost(t *testing.T) {
	p, _ := oneRouteProxy(t, "x")
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8190/view", nil)
	req.Host = "attacker.test"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a rebound host", rec.Code)
	}
}

func TestServeHTTPRoutes(t *testing.T) {
	p, _ := oneRouteProxy(t, "x")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8190/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/view" {
		t.Errorf("root: status %d location %q, want 302 to /view", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8190/nothing", nil)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: status = %d, want 404", rec.Code)
	}
}

// A provider that refuses for a reason other than a stale token is reported as
// it answered, not retried: rediscovery cannot fix a 500.
func TestServeHTTPReportsAProviderRefusal(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the painter is confused", http.StatusInternalServerError)
	}))
	defer provider.Close()

	cer := &components.Cervice{
		Definition: "view",
		Nodes: map[string][]components.NodeInfo{
			"painter": {{URL: provider.URL + "/painter/canvas/view", Tokens: map[string]string{"read": "t"}}},
		},
	}
	p := &proxy{routes: map[string]*route{"view": {definition: "view", cer: cer}}, first: "view"}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8190/view", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}
