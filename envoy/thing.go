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
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// initTemplate returns the asset that seeds systemconfig.json on a first run.
//
// One asset, no services. The mission is logging because that is what this does
// — it keeps a record — and because a mission is what the authorizer writes
// policy against, so leaving it blank would make the subject unclassifiable.
func initTemplate() *components.UnitAsset {
	return &components.UnitAsset{
		Name:        "operator",
		Mission:     components.MissionLogging,
		Details:     map[string][]string{"Type": {"Interactive"}},
		ServicesMap: components.Services{},
	}
}

// capture reads every provider of one service definition and writes each body
// to its own file. It returns how many were written and how many could not be.
func capture(sys *components.System, definition, dir string, stamp bool) (written, failed int) {
	cer, err := discover(sys, definition)
	if err != nil {
		log.Printf("envoy: %s: %v", definition, err)
		return 0, 0
	}

	for _, nodes := range cer.Nodes {
		for _, ni := range nodes {
			body, ctype, err := fetch(ni)
			if err != nil {
				log.Printf("envoy: %s: %v", definition, err)
				failed++
				continue
			}
			path := filepath.Join(dir, filename(definition, ni, ctype, stamp))
			if err := os.WriteFile(path, body, 0o644); err != nil {
				log.Printf("envoy: cannot write %s: %v", path, err)
				failed++
				continue
			}
			log.Printf("envoy: %s → %s (%d bytes)", definition, path, len(body))
			written++
		}
	}
	return written, failed
}

// fetch performs the read, presenting the token discovery obtained.
//
// The body is returned verbatim and never unpacked. This system archives what a
// provider said, and a Turtle graph or a SysML document is not one of the forms
// the framework knows how to parse — a capture tool that could only save what it
// could also interpret would be useless for exactly the documents worth saving.
func fetch(ni components.NodeInfo) (body []byte, contentType string, err error) {
	body, contentType, _, err = fetchStatus(ni)
	return body, contentType, err
}

// fetchStatus is fetch, and also reports the status the provider answered with.
//
// The proxy needs the number rather than the sentence: a 401 or 403 means the
// token this node was discovered with has gone stale and the read should be
// retried after re-discovery, while any other refusal is the provider's answer
// and belongs in front of the operator unchanged. Parsing that distinction back
// out of an error string would be guessing.
func fetchStatus(ni components.NodeInfo) (body []byte, contentType string, status int, err error) {
	req, err := http.NewRequest(http.MethodGet, ni.URL, nil)
	if err != nil {
		return nil, "", 0, err
	}
	if token := ni.Tokens["read"]; token != "" {
		req.Header.Set(usecases.TokenHeader, token)
	}

	// The framework's transport, so the CA this system now trusts is used to
	// verify the provider; a fresh http.Client would trust nothing and fail
	// every https URL the registry hands out.
	client := &http.Client{Timeout: 60 * time.Second, Transport: http.DefaultClient.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("reading %s: %w", ni.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body carries the reason — "the token expired at …", "the caller
		// presented no verified certificate" — and a bare status code sends an
		// operator to the wrong file.
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		detail := strings.TrimSpace(usecases.ForLog(string(reason)))
		if detail != "" {
			return nil, "", resp.StatusCode, fmt.Errorf("reading %s: %s: %s", ni.URL, resp.Status, detail)
		}
		return nil, "", resp.StatusCode, fmt.Errorf("reading %s: %s", ni.URL, resp.Status)
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", resp.StatusCode, fmt.Errorf("reading %s: %w", ni.URL, err)
	}
	return body, resp.Header.Get("Content-Type"), resp.StatusCode, nil
}

// filename names the capture after where it came from, so a directory of them
// can be read without opening any.
func filename(definition string, ni components.NodeInfo, contentType string, stamp bool) string {
	name := strings.Join(pathParts(ni.URL), "-")
	if name == "" {
		name = definition
	}
	if stamp {
		name += "-" + time.Now().Format("20060102-150405")
	}
	return name + extensionFor(ni, contentType)
}

// pathParts turns "https://host:30105/kgrapher/assembler/cloudgraph" into
// [kgrapher assembler cloudgraph], which is system, asset and service — the
// three things that identify a capture.
func pathParts(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var parts []string
	for _, p := range strings.Split(u.Path, "/") {
		if p = sanitize(p); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// sanitize keeps a path segment to characters that are safe in a filename on
// every platform. A provider names its own services, and a service definition
// containing a slash or a NUL would otherwise decide where this system writes.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// extensionFor picks a file extension, preferring what the provider registered
// over what it sent. The registry says "Turtle" or "SysML v2" because a person
// wrote it there; a Content-Type is often just text/plain.
func extensionFor(ni components.NodeInfo, contentType string) string {
	for _, format := range ni.Details["Format"] {
		switch strings.ToLower(strings.TrimSpace(format)) {
		case "turtle", "text/turtle":
			return ".ttl"
		case "sysml v2", "sysml":
			return ".sysml"
		case "application/json":
			return ".json"
		case "text/html":
			return ".html"
		}
	}
	switch {
	case strings.Contains(contentType, "turtle"):
		return ".ttl"
	case strings.Contains(contentType, "json"):
		return ".json"
	case strings.Contains(contentType, "html"):
		return ".html"
	case strings.Contains(contentType, "xml"):
		return ".xml"
	}
	return ".txt"
}
