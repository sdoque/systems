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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// fakeCA returns an httptest server that mimics the CA's whitelist endpoint.
// Each call lets the test inspect the ?since query and choose 200 vs 304.
func fakeCA(t *testing.T, version int64, hashes []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/whitelist" {
			http.NotFound(w, r)
			return
		}
		if since := r.URL.Query().Get("since"); since != "" {
			sinceN, _ := strconv.ParseInt(since, 10, 64)
			if sinceN >= version {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whitelistResponse{
			Version: version,
			Hashes:  hashes,
		})
	}))
}

// ── cache round-trip ──────────────────────────────────────────────────────────

func TestSaveAndLoadCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitelist.cache.json")

	t1 := &Traits{Whitelist: []string{"abc", "def"}, version: 1700000000, loaded: true}
	if err := t1.saveCache(path); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	t2 := &Traits{}
	if err := t2.loadCache(path); err != nil {
		t.Fatalf("loadCache: %v", err)
	}

	if !reflect.DeepEqual(t2.Whitelist, t1.Whitelist) {
		t.Errorf("hashes = %v, want %v", t2.Whitelist, t1.Whitelist)
	}
	if t2.version != t1.version {
		t.Errorf("version = %d, want %d", t2.version, t1.version)
	}
	if !t2.IsLoaded() {
		t.Error("loaded must be true after loadCache")
	}
}

func TestLoadCacheMissingFile(t *testing.T) {
	dir := t.TempDir()
	tr := &Traits{}
	if err := tr.loadCache(filepath.Join(dir, "nope.json")); err != nil {
		t.Fatalf("missing cache file should not be an error: %v", err)
	}
	if tr.IsLoaded() {
		t.Error("loaded must remain false when no cache file existed")
	}
}

func TestLoadCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitelist.cache.json")
	os.WriteFile(path, []byte("not json"), 0644)

	tr := &Traits{}
	if err := tr.loadCache(path); err == nil {
		t.Error("expected error for corrupt cache file")
	}
}

// ── fetchFromCA ───────────────────────────────────────────────────────────────

func TestFetchFromCA(t *testing.T) {
	t.Run("empty Traits gets full whitelist", func(t *testing.T) {
		srv := fakeCA(t, 100, []string{"a", "b"})
		defer srv.Close()

		tr := &Traits{}
		changed, err := tr.fetchFromCA(context.Background(), http.DefaultClient, srv.URL)
		if err != nil {
			t.Fatalf("fetchFromCA: %v", err)
		}
		if !changed {
			t.Error("expected changed=true on first fetch")
		}
		if !reflect.DeepEqual(tr.Whitelist, []string{"a", "b"}) {
			t.Errorf("hashes = %v, want [a b]", tr.Whitelist)
		}
		if tr.version != 100 {
			t.Errorf("version = %d, want 100", tr.version)
		}
	})

	t.Run("up-to-date Traits gets 304 → changed=false", func(t *testing.T) {
		srv := fakeCA(t, 100, []string{"a"})
		defer srv.Close()

		tr := &Traits{Whitelist: []string{"a"}, version: 100, loaded: true}
		changed, err := tr.fetchFromCA(context.Background(), http.DefaultClient, srv.URL)
		if err != nil {
			t.Fatalf("fetchFromCA: %v", err)
		}
		if changed {
			t.Error("expected changed=false on 304")
		}
	})

	t.Run("CA error returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusInternalServerError)
		}))
		defer srv.Close()

		tr := &Traits{}
		_, err := tr.fetchFromCA(context.Background(), http.DefaultClient, srv.URL)
		if err == nil {
			t.Error("expected error when CA returns 500")
		}
	})
}

// ── runSyncLoop bootstrap behaviour ───────────────────────────────────────────

func TestRunSyncLoopFirstRun(t *testing.T) {
	srv := fakeCA(t, 100, []string{"a", "b"})
	defer srv.Close()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "whitelist.cache.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{}
	// Use a long interval so the ticker doesn't fire during this synchronous test.
	if err := tr.runSyncLoop(ctx, http.DefaultClient, srv.URL, cachePath, time.Hour); err != nil {
		t.Fatalf("runSyncLoop: %v", err)
	}

	if !tr.IsLoaded() {
		t.Fatal("loaded must be true after first successful fetch")
	}
	if !reflect.DeepEqual(tr.Whitelist, []string{"a", "b"}) {
		t.Errorf("hashes = %v, want [a b]", tr.Whitelist)
	}
	// Cache must have been written.
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache file not written: %v", err)
	}
}

func TestRunSyncLoopFirstRunCAUnreachable(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "whitelist.cache.json")

	tr := &Traits{}
	// No cache + CA unreachable ⇒ must fail fatally.
	err := tr.runSyncLoop(context.Background(), http.DefaultClient,
		"http://127.0.0.1:1", // unreachable
		cachePath, time.Hour)
	if err == nil {
		t.Error("expected fatal error when no cache exists and CA is unreachable")
	}
	if tr.IsLoaded() {
		t.Error("loaded must remain false when first fetch failed without cache")
	}
}

func TestRunSyncLoopFallsBackToCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "whitelist.cache.json")

	// Pre-populate the cache as if a previous run had succeeded.
	pre := &Traits{Whitelist: []string{"cached-hash"}, version: 50, loaded: true}
	if err := pre.saveCache(cachePath); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	tr := &Traits{}
	err := tr.runSyncLoop(context.Background(), http.DefaultClient,
		"http://127.0.0.1:1", // unreachable
		cachePath, time.Hour)
	if err != nil {
		t.Fatalf("expected nil error when cache is present, got: %v", err)
	}
	if !tr.IsLoaded() {
		t.Error("loaded must be true after cache fallback")
	}
	if !reflect.DeepEqual(tr.Whitelist, []string{"cached-hash"}) {
		t.Errorf("hashes = %v, want [cached-hash]", tr.Whitelist)
	}
}
