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
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// describeResolutionFailure turns a failure to read /proc/<pid>/exe into
// something an operator can act on.
//
// The interesting case is permission. Linux lets a process read another's exe
// link only if it could trace it, so a maitreD running as one user cannot see a
// system started with sudo — which is how a system that needs GPIO is usually
// started. Every other system on the host attests and that one never does.
//
// The remedy given here is to drop the privilege rather than to raise maitreD's.
// A maitreD running as root to inspect everything is a larger thing to trust
// than the systems it is attesting, and requiring it would put root in the path
// of every deployment.
// It returns the explanation and whether maitreD is certain enough to refuse
// rather than report a fault of its own.
func describeResolutionFailure(pid int, err error) (reason string, refused bool) {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("cannot read /proc/%d/exe: the process belongs to another user, "+
			"so this maitreD cannot see what it is running. Start that system as the same user as maitreD — "+
			"a system needing GPIO usually wants group membership (gpio, dialout) rather than sudo — "+
			"or run maitreD as the user that owns it", pid), true
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no process %d: it exited before it could be attested", pid), true
	default:
		return fmt.Sprintf("cannot read /proc/%d/exe: %v", pid, err), false
	}
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	attest := components.Service{
		Definition:  "attest",
		SubPath:     "attest",
		Details:     map[string][]string{"Forms": {"application/json"}, "Methods": components.HTTPMethods("POST")},
		RegPeriod:   0,
		Description: "verifies (POST) the executable hash of the requesting system against the whitelist",
	}

	return &components.UnitAsset{
		Name:        "maitreD",
		Mission:     components.MissionCore,
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
		// Say which failure it is. The two look identical from outside and want
		// opposite responses: a process that has exited is nothing to worry
		// about, while one this maitreD cannot see is a system that will never
		// be certified, retrying once a minute for as long as it runs.
		reason, refused := describeResolutionFailure(req.PID, err)
		log.Printf("attestation impossible: pid=%d: %s\n", req.PID, reason)
		// A refusal where maitreD is certain, an error where it is not. It
		// cannot see a process belonging to another user and never will, so
		// retrying is pointless and 403 says so; an unexpected failure might be
		// transient and 500 leaves that open.
		status := http.StatusInternalServerError
		if refused {
			status = http.StatusForbidden
		}
		http.Error(w, reason, status)
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
