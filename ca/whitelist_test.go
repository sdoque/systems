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
