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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ── loadWhitelist ─────────────────────────────────────────────────────────────

func TestLoadWhitelist(t *testing.T) {
	t.Run("missing file returns empty whitelist with version 0", func(t *testing.T) {
		dir := t.TempDir()
		wl, err := loadWhitelist(filepath.Join(dir, "nope.json"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(wl.Hashes) != 0 {
			t.Errorf("expected empty hashes, got %v", wl.Hashes)
		}
		if wl.Version != 0 {
			t.Errorf("expected version 0 for missing file, got %d", wl.Version)
		}
	})

	t.Run("flat array of hashes parses, version is mtime", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "whitelist.json")
		if err := os.WriteFile(path, []byte(`["abc","def"]`), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		wl, err := loadWhitelist(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(wl.Hashes, []string{"abc", "def"}) {
			t.Errorf("hashes = %v, want [abc def]", wl.Hashes)
		}
		if wl.Version <= 0 {
			t.Errorf("version should be positive for an existing file, got %d", wl.Version)
		}
		if wl.UpdatedAt == "" {
			t.Error("UpdatedAt must be set for an existing file")
		}
	})

	t.Run("empty array yields zero hashes (no panic, no error)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "whitelist.json")
		os.WriteFile(path, []byte(`[]`), 0644)
		wl, err := loadWhitelist(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(wl.Hashes) != 0 {
			t.Errorf("expected empty hashes, got %v", wl.Hashes)
		}
	})

	t.Run("malformed JSON returns parse error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "whitelist.json")
		os.WriteFile(path, []byte(`not json`), 0644)
		if _, err := loadWhitelist(path); err == nil {
			t.Error("expected parse error for malformed JSON")
		}
	})
}

// ── whitelisting (HTTP handler) ───────────────────────────────────────────────

// newWhitelistTraits returns a Traits configured for the whitelist HTTP tests.
// It writes the supplied hashes to a temp file and points Traits at it, so
// each subtest is fully isolated.
func newWhitelistTraits(t *testing.T, hashes []string) *Traits {
	t.Helper()
	path := filepath.Join(t.TempDir(), "whitelist.json")
	if hashes != nil {
		data, _ := json.Marshal(hashes)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("write whitelist: %v", err)
		}
	}
	return &Traits{
		MaitreDHosts:  []string{"127.0.0.1"},
		WhitelistPath: path,
	}
}

func TestWhitelisting(t *testing.T) {
	t.Run("authorized GET returns the whitelist", func(t *testing.T) {
		traits := newWhitelistTraits(t, []string{"abc", "def"})
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()

		traits.whitelisting(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		var wl Whitelist
		if err := json.Unmarshal(w.Body.Bytes(), &wl); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !reflect.DeepEqual(wl.Hashes, []string{"abc", "def"}) {
			t.Errorf("hashes = %v, want [abc def]", wl.Hashes)
		}
		if wl.Version <= 0 {
			t.Errorf("version = %d, want > 0", wl.Version)
		}
	})

	t.Run("unauthorized source IP returns 403", func(t *testing.T) {
		traits := newWhitelistTraits(t, []string{"abc"})
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist", nil)
		req.RemoteAddr = "10.0.0.99:54321"
		w := httptest.NewRecorder()

		traits.whitelisting(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("non-GET methods return 405", func(t *testing.T) {
		traits := newWhitelistTraits(t, []string{"abc"})
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req := httptest.NewRequest(method, "/ca/certification/whitelist", nil)
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()

			traits.whitelisting(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: status = %d, want 405", method, w.Code)
			}
		}
	})

	t.Run("?since at current version returns 304", func(t *testing.T) {
		traits := newWhitelistTraits(t, []string{"abc"})

		// First fetch to learn the current version.
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()
		traits.whitelisting(w, req)
		var wl Whitelist
		if err := json.Unmarshal(w.Body.Bytes(), &wl); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		// Re-request with since == current version → must be 304.
		req2 := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/ca/certification/whitelist?since=%d", wl.Version), nil)
		req2.RemoteAddr = "127.0.0.1:54321"
		w2 := httptest.NewRecorder()
		traits.whitelisting(w2, req2)

		if w2.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304", w2.Code)
		}
	})

	t.Run("?since older than version returns 200 with body", func(t *testing.T) {
		traits := newWhitelistTraits(t, []string{"abc"})
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist?since=0", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()

		traits.whitelisting(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("missing whitelist file serves empty list (fail-closed by maitreD)", func(t *testing.T) {
		traits := newWhitelistTraits(t, nil) // hashes==nil ⇒ no file written
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		w := httptest.NewRecorder()

		traits.whitelisting(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var wl Whitelist
		json.Unmarshal(w.Body.Bytes(), &wl)
		if len(wl.Hashes) != 0 {
			t.Errorf("expected empty hashes for missing file, got %v", wl.Hashes)
		}
		if wl.Version != 0 {
			t.Errorf("expected version 0 for missing file, got %d", wl.Version)
		}
	})
}

// ── serving routing ──────────────────────────────────────────────────────────

func TestServingWhitelistDispatch(t *testing.T) {
	traits := newWhitelistTraits(t, []string{"abc"})
	req := httptest.NewRequest(http.MethodGet, "/ca/certification/whitelist", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()

	serving(traits, w, req, "whitelist")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
}
