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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the cloudmodel assembler.
type Traits struct {
	CloudName string             `json:"cloudName"` // name used for the merged SysML v2 package
	owner     *components.System `json:"-"`
	name      string             `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	cloudmodel := components.Service{
		Definition:  "cloudmodel",
		SubPath:     "cloudmodel",
		Details:     map[string][]string{"Format": {"SysML v2"}},
		RegPeriod:   61,
		Description: "provides the SysML v2 BDD/IBD model of the local cloud (GET)",
	}

	return &components.UnitAsset{
		Name:        "assembler",
		Mission:     "handle_sysml",
		Details:     map[string][]string{"Type": {"Interactive"}},
		ServicesMap: map[string]*components.Service{cloudmodel.SubPath: &cloudmodel},
		Traits:      &Traits{CloudName: "localCloud"},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		CloudName: "localCloud",
		owner:     sys,
		name:      configuredAsset.Name,
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
		log.Println("Shutting down cloudmodel")
	}
}

//-------------------------------------Unit asset's function methods

// assembleModel fetches /smodel from each registered system, merges the per-system
// SysML v2 packages into a single package, and writes the result.
func (t *Traits) assembleModel(w http.ResponseWriter) {
	leadingRegistrarURL, err := components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
	if err != nil {
		log.Printf("Error getting the leading service registrar URL: %s\n", err)
		http.Error(w, "Internal Server Error: unable to get leading service registrar URL", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequest(http.MethodGet, leadingRegistrarURL+"/syslist", nil)
	if err != nil {
		log.Printf("Error creating system list request: %s\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	req = req.WithContext(ctx)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error fetching system list: %s\n", err)
		http.Error(w, "Service Unavailable: unable to reach service registrar", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading system list response: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		log.Printf("Error parsing system list content type: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	sL, err := usecases.Unpack(bodyBytes, mediaType)
	if err != nil {
		log.Printf("Error unpacking system list: %v\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	systemsList, ok := sL.(*forms.SystemRecordList_v1)
	if !ok {
		log.Println("Problem asserting system list type")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Collect and merge all per-system packages.
	seenPortDefs := make(map[string]bool)
	var portDefs, blockDefs, ibdParts []string

	for _, sysURL := range systemsList.List {
		smodelURL := sysURL + "/smodel"
		log.Println("Fetching SysML model from", smodelURL)

		resp, err := http.Get(smodelURL)
		if err != nil {
			log.Printf("Unable to get SysML model from %s: %s\n", sysURL, err)
			continue
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading model response from %s: %s\n", sysURL, err)
			continue
		}

		pd, bd, ibd := parsePackage(string(bodyBytes))

		for _, line := range pd {
			name := extractPortDefName(line)
			if name != "" && !seenPortDefs[name] {
				seenPortDefs[name] = true
				portDefs = append(portDefs, line)
			}
		}
		blockDefs = append(blockDefs, bd...)
		ibdParts = append(ibdParts, ibd...)
	}

	// Build the merged SysML v2 package.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("package '%s' {\n\n", t.CloudName))

	if len(portDefs) > 0 {
		sb.WriteString("    // ── Port Definitions ─────────────────────────────────────────────────────\n")
		for _, line := range portDefs {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if len(blockDefs) > 0 {
		sb.WriteString("    // ── Block Definitions (BDD) ──────────────────────────────────────────────\n")
		for _, block := range blockDefs {
			sb.WriteString(block + "\n\n")
		}
	}

	if len(ibdParts) > 0 {
		sb.WriteString("    // ── Internal Block Diagram (IBD) ─────────────────────────────────────────\n")
		for _, part := range ibdParts {
			sb.WriteString(part + "\n\n")
		}
	}

	sb.WriteString("}\n")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(sb.String()))
}

// parsePackage parses a SysML v2 package (as produced by SModeling) and returns
// the port definitions, block definitions (part def), and IBD parts (part instances).
func parsePackage(text string) (portDefs []string, blockDefs []string, ibdParts []string) {
	inner := extractPackageContent(text)
	if inner == "" {
		return
	}

	lines := strings.Split(inner, "\n")
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		// Skip blank lines and comment-only lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			i++
			continue
		}

		// Single-line port definition.
		if strings.HasPrefix(trimmed, "port def ") {
			portDefs = append(portDefs, lines[i])
			i++
			continue
		}

		// Multi-line block definition (BDD).
		if strings.HasPrefix(trimmed, "part def ") {
			block, consumed := extractBlock(lines, i)
			if block != "" {
				blockDefs = append(blockDefs, block)
			}
			i += consumed
			continue
		}

		// Multi-line part instance (IBD).
		if strings.HasPrefix(trimmed, "part ") {
			block, consumed := extractBlock(lines, i)
			if block != "" {
				ibdParts = append(ibdParts, block)
			}
			i += consumed
			continue
		}

		i++
	}
	return
}

// extractPackageContent strips the "package '...' {" header and closing "}" to
// return only the inner content of a SysML v2 package block.
func extractPackageContent(text string) string {
	text = strings.TrimSpace(text)
	idx := strings.Index(text, "{")
	if idx < 0 {
		return ""
	}
	last := strings.LastIndex(text, "}")
	if last <= idx {
		return ""
	}
	return text[idx+1 : last]
}

// extractBlock reads a brace-balanced block starting at lines[start] and returns
// the block text and the number of lines consumed.
func extractBlock(lines []string, start int) (string, int) {
	depth := 0
	var collected []string
	started := false

	for i := start; i < len(lines); i++ {
		line := lines[i]
		collected = append(collected, line)
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if strings.Contains(line, "{") {
			started = true
		}
		if started && depth <= 0 {
			return strings.Join(collected, "\n"), i - start + 1
		}
	}
	return strings.Join(collected, "\n"), len(lines) - start
}

// extractPortDefName extracts the definition name from a "    port def '<name>';" line.
func extractPortDefName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "port def ") {
		return ""
	}
	name := strings.TrimPrefix(trimmed, "port def ")
	name = strings.Trim(name, "'; \t")
	return name
}
