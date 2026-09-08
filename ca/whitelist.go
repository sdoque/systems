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
	"os"
	"time"
)

// Whitelist is the operator's policy: the SHA-256 hashes of the binaries this
// cloud will issue certificates to.
//
// It is read on every certificate request rather than held, so an edit takes
// effect immediately. Version is the Unix-second mtime of whitelist.json and
// UpdatedAt the same in RFC3339; both are kept because they cost nothing and
// they are what a log line needs to say which policy was applied.
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

// The CA no longer serves this list to anyone. It used to: every maitreD
// fetched a copy, cached it on disk and applied it on the CA's behalf, which
// put the policy on every host, gave each copy a five-minute staleness, and
// left a file that granted a certificate to whatever hash was written into it.
// The maitreD now reports a measurement and the decision is made here, against
// this file, at the moment a certificate is asked for.
