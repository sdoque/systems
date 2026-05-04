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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// writeTempFile creates a file with the given content in a temp dir and returns its path.
func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "testexe")
	if err := os.WriteFile(path, content, 0755); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// sha256Hex returns the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// withResolveExecutable temporarily replaces resolveExecutable for the duration of the test.
func withResolveExecutable(t *testing.T, fn func(int) (string, error)) {
	t.Helper()
	orig := resolveExecutable
	resolveExecutable = fn
	t.Cleanup(func() { resolveExecutable = orig })
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "maitreD" {
		t.Errorf("name = %q, want %q", ua.GetName(), "maitreD")
	}
	svc, ok := ua.GetServices()["attest"]
	if !ok {
		t.Fatal("expected 'attest' entry in ServicesMap")
	}
	if svc.Definition != "attest" {
		t.Errorf("service definition = %q, want %q", svc.Definition, "attest")
	}
	if ua.GetTraits() == nil {
		t.Error("Traits should be non-nil")
	}
}

// ── Traits serialisation ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	// All Traits fields are runtime state, not config: marshalling must
	// produce no operator-visible fields. A future schema addition that
	// accidentally exposes one of these will fail this test.
	original := &Traits{Whitelist: []string{"abc123"}, version: 42, loaded: true}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	for _, field := range []string{"whitelist", "version", "loaded", "owner", "name"} {
		if _, ok := raw[field]; ok {
			t.Errorf("field %q must not appear in JSON", field)
		}
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

func TestNewResource(t *testing.T) {
	t.Run("creates unit asset with correct fields", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sys := components.NewSystem("maitreD", ctx)
		sys.Husk = &components.Husk{
			Host:      components.NewDevice(),
			ProtoPort: map[string]int{"http": 20101},
		}

		attestSvc := components.Service{
			Definition: "attest",
			SubPath:    "attest",
		}
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "maitreD",
			Mission:  "attest systems on this host",
			Services: []components.Service{attestSvc},
		}

		ua, cleanup := newResource(cfgAsset, &sys)
		defer cleanup()

		if ua.GetName() != "maitreD" {
			t.Errorf("name = %q, want %q", ua.GetName(), "maitreD")
		}
		if ua.Mission != "attest systems on this host" {
			t.Errorf("mission = %q, want %q", ua.Mission, "attest systems on this host")
		}
		if ua.ServingFunc == nil {
			t.Error("ServingFunc must be set")
		}
		if _, ok := ua.GetServices()["attest"]; !ok {
			t.Error("expected 'attest' service in map")
		}
	})

	t.Run("ignores any 'whitelist' field carried by an older systemconfig", func(t *testing.T) {
		// The whitelist is now CA-mastered. Operator-supplied whitelist entries
		// in systemconfig.json must be silently ignored, not loaded as truth.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sys := components.NewSystem("maitreD", ctx)
		sys.Husk = &components.Husk{
			Host:      components.NewDevice(),
			ProtoPort: map[string]int{"http": 20101},
		}

		// Hand-craft a traits payload using the legacy "whitelist" key.
		traitJSON := json.RawMessage(`{"whitelist":["aabbcc"]}`)
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "maitreD",
			Traits:   []json.RawMessage{traitJSON},
			Services: []components.Service{{Definition: "attest", SubPath: "attest"}},
		}

		ua, cleanup := newResource(cfgAsset, &sys)
		defer cleanup()

		tr, ok := ua.GetTraits().(*Traits)
		if !ok {
			t.Fatal("traits are not of type *Traits")
		}
		if len(tr.Whitelist) != 0 {
			t.Errorf("Whitelist must remain empty (CA is the source of truth); got %v", tr.Whitelist)
		}
	})
}

// ── serving ───────────────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	exeData := []byte("fake-executable")
	exePath := writeTempFile(t, exeData)
	hash := sha256Hex(exeData)
	tr := &Traits{Whitelist: []string{hash}, loaded: true}

	withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })

	t.Run("attest path dispatches correctly", func(t *testing.T) {
		body, _ := json.Marshal(map[string]int{"pid": 42})
		req := httptest.NewRequest(http.MethodPost, "/maitreD/maitreD/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		serving(tr, w, req, "attest")
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown path returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// ── attest ────────────────────────────────────────────────────────────────────

func TestAttest(t *testing.T) {
	exeData := []byte("approved-binary-content")
	exePath := writeTempFile(t, exeData)
	approvedHash := sha256Hex(exeData)

	tr := &Traits{Whitelist: []string{approvedHash}, loaded: true}

	t.Run("approved executable returns 200", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })

		body, _ := json.Marshal(map[string]int{"pid": 99})
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown executable hash returns 403", func(t *testing.T) {
		otherData := []byte("untrusted-binary")
		otherPath := writeTempFile(t, otherData)
		withResolveExecutable(t, func(pid int) (string, error) { return otherPath, nil })

		body, _ := json.Marshal(map[string]int{"pid": 99})
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("unresolvable PID returns 500", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) {
			return "", fmt.Errorf("no such process")
		})

		body, _ := json.Marshal(map[string]int{"pid": 99})
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", w.Code)
		}
	})

	t.Run("non-POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/attest", nil)
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader([]byte("not json")))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("zero PID returns 400", func(t *testing.T) {
		body, _ := json.Marshal(map[string]int{"pid": 0})
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("returns 503 when whitelist not yet loaded", func(t *testing.T) {
		// loaded=false ⇒ no successful sync yet ⇒ refuse to make a decision.
		notReady := &Traits{Whitelist: []string{approvedHash}}
		body, _ := json.Marshal(map[string]int{"pid": 99})
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		notReady.attest(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
	})
}

// ── hashFile ──────────────────────────────────────────────────────────────────

func TestHashFile(t *testing.T) {
	content := []byte("hello maitreD")
	path := writeTempFile(t, content)

	t.Run("produces correct SHA-256", func(t *testing.T) {
		got, err := hashFile(path)
		if err != nil {
			t.Fatalf("hashFile: %v", err)
		}
		if want := sha256Hex(content); got != want {
			t.Errorf("hash = %s, want %s", got, want)
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := hashFile(filepath.Join(t.TempDir(), "no-such-file"))
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

// ── isApproved ────────────────────────────────────────────────────────────────

func TestIsApproved(t *testing.T) {
	tr := &Traits{Whitelist: []string{"aaa", "bbb"}}

	if !tr.isApproved("aaa") {
		t.Error("expected aaa to be approved")
	}
	if tr.isApproved("ccc") {
		t.Error("expected ccc to be rejected")
	}
	if (&Traits{}).isApproved("aaa") {
		t.Error("empty whitelist should reject everything")
	}
}
