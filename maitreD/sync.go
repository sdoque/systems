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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// whitelistResponse mirrors the wire format produced by the CA's
// /ca/certification/whitelist endpoint. Both endpoints define their own copy
// because the CA and maitreD are separate `package main` binaries with no
// shared package; the JSON contract on the wire is the source of truth.
type whitelistResponse struct {
	Version   int64    `json:"version"`
	UpdatedAt string   `json:"updatedAt"`
	Hashes    []string `json:"hashes"`
}

// defaultSyncInterval is how often the maitreD re-checks the CA for whitelist
// changes after the initial load. Five minutes is the design's deliberate
// trade-off: deployments don't change minute-by-minute, but operators don't
// want to wait an hour after editing the whitelist.
const defaultSyncInterval = 5 * time.Minute

// loadCache reads the previously-fetched whitelist from disk into Traits.
// A missing file is not an error — it represents "first ever run".
func (t *Traits) loadCache(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var wl whitelistResponse
	if err := json.Unmarshal(data, &wl); err != nil {
		return fmt.Errorf("parse cache %s: %w", path, err)
	}
	t.mu.Lock()
	t.Whitelist = wl.Hashes
	t.version = wl.Version
	t.loaded = true
	t.mu.Unlock()
	return nil
}

// saveCache atomically writes the in-memory whitelist to disk so the next
// startup can fall back to it if the CA is unreachable. The atomic dance
// (write to a sibling file, then rename) keeps the cache file consistent
// even if the maitreD is killed mid-write.
func (t *Traits) saveCache(path string) error {
	t.mu.RLock()
	wl := whitelistResponse{
		Version:   t.version,
		UpdatedAt: time.Unix(t.version, 0).UTC().Format(time.RFC3339),
		Hashes:    append([]string{}, t.Whitelist...),
	}
	t.mu.RUnlock()

	data, err := json.MarshalIndent(wl, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fetchFromCA performs a single GET against the CA's whitelist endpoint. It
// returns true if the CA's version is newer than ours (the body has been
// applied); false if the CA returned 304 Not Modified or the body is
// otherwise unchanged. caURL is the base URL of the CA's certification asset
// (e.g. "http://localhost:20100/ca/certification"), to which "/whitelist" is
// appended.
func (t *Traits) fetchFromCA(ctx context.Context, client *http.Client, caURL string) (bool, error) {
	t.mu.RLock()
	since := t.version
	t.mu.RUnlock()

	url := strings.TrimRight(caURL, "/") + "/whitelist"
	if since > 0 {
		url = fmt.Sprintf("%s?since=%d", url, since)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("CA returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var wl whitelistResponse
	if err := json.NewDecoder(resp.Body).Decode(&wl); err != nil {
		return false, fmt.Errorf("decode whitelist response: %w", err)
	}

	t.mu.Lock()
	t.Whitelist = wl.Hashes
	t.version = wl.Version
	t.loaded = true
	t.mu.Unlock()
	return true, nil
}

// syncOnce performs one fetch+cache cycle. On success it returns nil; on
// failure it returns the error so the caller can decide whether to fail-fast
// (first-ever run, no cache) or fail-soft (continue on stale cache).
func (t *Traits) syncOnce(ctx context.Context, client *http.Client, caURL, cachePath string) error {
	changed, err := t.fetchFromCA(ctx, client, caURL)
	if err != nil {
		return err
	}
	if changed {
		if err := t.saveCache(cachePath); err != nil {
			// Cache write failure is logged but does not invalidate the in-memory
			// fetch — attestations will still work; only the next-startup fallback
			// is degraded.
			log.Printf("warning: could not persist whitelist cache to %s: %v", cachePath, err)
		}
	}
	return nil
}

// runSyncLoop bootstraps the whitelist (cache → first fetch) and then keeps
// it fresh on a ticker until ctx is cancelled. Failure semantics, by design:
//
//   - cache present + fetch fails → log warning, continue with cache.
//   - cache absent + fetch fails  → return error (caller exits the process).
//   - successful fetch            → updates in-memory + cache.
//   - subsequent fetch failures   → log warning, keep current in-memory state.
func (t *Traits) runSyncLoop(ctx context.Context, client *http.Client, caURL, cachePath string, interval time.Duration) error {
	if err := t.loadCache(cachePath); err != nil {
		log.Printf("warning: could not read whitelist cache %s: %v", cachePath, err)
	}
	hadCache := t.IsLoaded()

	if err := t.syncOnce(ctx, client, caURL, cachePath); err != nil {
		if !hadCache {
			return fmt.Errorf("first whitelist fetch failed and no cache exists: %w", err)
		}
		log.Printf("warning: initial whitelist fetch failed, continuing with cache: %v", err)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := t.syncOnce(ctx, client, caURL, cachePath); err != nil {
					log.Printf("warning: whitelist sync failed, keeping current state: %v", err)
				}
			}
		}
	}()
	return nil
}

// IsLoaded reports whether the in-memory whitelist has been populated at least
// once (from cache or from a fetch). The attest handler will use this in
// step 3 to fail-closed before the first successful load.
func (t *Traits) IsLoaded() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded
}

// bootstrapWhitelist resolves the CA URL from the system's core-system list,
// then drives runSyncLoop on every maitreD UnitAsset in the system. Returns
// an error if the maitreD asset is missing, the CA URL cannot be resolved,
// or the first sync fails fatally (no cache + CA unreachable).
func bootstrapWhitelist(sys *components.System) error {
	caURL, err := components.GetRunningCoreSystemURL(sys, "ca")
	if err != nil {
		return fmt.Errorf("resolve CA URL: %w", err)
	}
	for name, ua := range sys.UAssets {
		t, ok := ua.GetTraits().(*Traits)
		if !ok {
			continue // not a maitreD asset
		}
		if err := t.runSyncLoop(sys.Ctx, http.DefaultClient, caURL, whitelistCachePath, defaultSyncInterval); err != nil {
			return fmt.Errorf("asset %s: %w", name, err)
		}
	}
	return nil
}
