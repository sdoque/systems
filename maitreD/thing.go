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
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the runtime state of the maitreD unit asset.
//
// The Whitelist is fetched from the CA and refreshed periodically (see
// sync.go); it is not part of the operator-edited systemconfig.json schema.
// Any "whitelist" entry that an older systemconfig still carries is silently
// ignored by Go's json package because the field is tagged `json:"-"`.
type Traits struct {
	Whitelist []string           `json:"-"` // approved SHA-256 hashes (kept in sync with the CA)
	version   int64              `json:"-"` // current whitelist version (CA-issued)
	loaded    bool               `json:"-"` // true after first successful cache load or fetch
	mu        sync.RWMutex       `json:"-"` // protects Whitelist, version, loaded
	owner     *components.System `json:"-"`
	name      string             `json:"-"`
}

// resolveExecutable returns the filesystem path of the executable running as pid.
// It reads /proc/<pid>/exe, which is Linux-specific. The variable form allows
// tests to substitute a different implementation without build tags.
var resolveExecutable = func(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	attest := components.Service{
		Definition:  "attest",
		SubPath:     "attest",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   0,
		Description: "verifies (POST) the executable hash of the requesting system against the whitelist",
	}

	return &components.UnitAsset{
		Name:        "maitreD",
		Details:     map[string][]string{"Role": {"host-attestation"}},
		ServicesMap: map[string]*components.Service{attest.SubPath: &attest},
		Traits:      &Traits{},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
		name:  configuredAsset.Name,
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
	}
}

//-------------------------------------Unit asset's function methods

// attest handles a POST request from the CA. It resolves the executable of the given PID,
// hashes it, and returns 200 if the hash is on the whitelist or 403 if it is not.
//
// Returns 503 Service Unavailable until the maitreD has loaded a whitelist
// at least once (from cache or fresh fetch). This prevents the brief
// post-startup window in which attestation could otherwise run against an
// empty in-memory list and approve nothing legitimately, or — worse — be
// silently misconfigured into a permissive state.
func (t *Traits) attest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
		return
	}
	if !t.IsLoaded() {
		http.Error(w, "Whitelist not yet loaded", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID <= 0 {
		http.Error(w, "Invalid request body: expected {\"pid\": <n>}", http.StatusBadRequest)
		return
	}

	exePath, err := resolveExecutable(req.PID)
	if err != nil {
		http.Error(w, "Cannot resolve executable for PID", http.StatusInternalServerError)
		return
	}

	hash, err := hashFile(exePath)
	if err != nil {
		http.Error(w, "Cannot hash executable", http.StatusInternalServerError)
		return
	}

	if !t.isApproved(hash) {
		log.Printf("attestation denied: pid=%d exe=%s hash=%s\n", req.PID, exePath, hash)
		http.Error(w, "Executable not in whitelist", http.StatusForbidden)
		return
	}

	log.Printf("attestation approved: pid=%d exe=%s\n", req.PID, exePath)
	w.WriteHeader(http.StatusOK)
}

// isApproved reports whether hash is present in the in-memory whitelist.
// The read lock keeps this safe against the sync loop concurrently swapping
// the slice during a refresh.
func (t *Traits) isApproved(hash string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, h := range t.Whitelist {
		if h == hash {
			return true
		}
	}
	return false
}

// hashFile returns the lowercase hex-encoded SHA-256 digest of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
