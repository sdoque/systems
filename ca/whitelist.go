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
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Whitelist is the wire format served by the CA at /ca/certification/whitelist.
//
// Version is the Unix-second mtime of whitelist.json; bumping the file's mtime
// (any edit, or `touch`) advances the version automatically — operators do not
// hand-maintain a counter. UpdatedAt is the same timestamp in RFC3339 for
// human readers.
type Whitelist struct {
	Version   int64    `json:"version"`
	UpdatedAt string   `json:"updatedAt"`
	Hashes    []string `json:"hashes"`
}

// loadWhitelist reads the operator-edited whitelist file, which is a flat JSON
// array of SHA-256 hex strings. The wrapper struct adds version metadata
// derived from the file's modification time.
//
// A missing file is not an error: it represents the deliberate state "operator
// has not approved any binaries yet". The CA serves an empty whitelist and the
// maitreD enforces fail-closed (no hash matches an empty list), so accidentally
// deleting the file does not silently approve every binary — it denies them.
func loadWhitelist(path string) (Whitelist, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Whitelist{Hashes: []string{}}, nil
	}
	if err != nil {
		return Whitelist{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Whitelist{}, err
	}
	hashes := []string{}
	if err := json.Unmarshal(data, &hashes); err != nil {
		return Whitelist{}, fmt.Errorf("parse %s: %w", path, err)
	}
	mt := info.ModTime()
	return Whitelist{
		Version:   mt.Unix(),
		UpdatedAt: mt.UTC().Format(time.RFC3339),
		Hashes:    hashes,
	}, nil
}

// whitelisting handles GET /ca/certification/whitelist.
//
// Source IP must be in MaitreDHosts (same gating as maitreD enrollment) so
// that only authenticated host sentinels can pull the list. ?since=N
// short-circuits with 304 Not Modified when the on-disk version has not
// advanced past N, so an unchanged whitelist costs the maitreD a single
// HEAD-equivalent round trip.
func (t *Traits) whitelisting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
		return
	}
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "Failed to determine client IP", http.StatusInternalServerError)
		return
	}
	if !t.isMaitreDAuthorized(clientIP) {
		log.Printf("whitelist: denied source IP %q (maitreDHosts=%v)", clientIP, t.MaitreDHosts)
		http.Error(w, "Unauthorized maitreD host", http.StatusForbidden)
		return
	}

	wl, err := loadWhitelist(t.WhitelistPath)
	if err != nil {
		http.Error(w, "Cannot load whitelist", http.StatusInternalServerError)
		return
	}

	if since := r.URL.Query().Get("since"); since != "" {
		if n, err := strconv.ParseInt(since, 10, 64); err == nil && wl.Version <= n {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wl)
}
