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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the modeler assembler.
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
		log.Println("Shutting down modeler")
	}
}

//-------------------------------------Service handlers

// aggregate handles GET requests to generate the merged cloud SysML v2 model.
func (t *Traits) aggregate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		t.assembleModel(w)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
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

	// Collect per-system fragments plus structured IBD data.
	seenPortDefs := make(map[string]bool)
	seenAbstractActions := make(map[string]bool)
	var portDefs, blockDefs, abstractActions, behaviorDefs []string
	var fragments []systemFragment

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

		pd, bd, ibd, aa, beh := parsePackage(string(bodyBytes))

		for _, line := range pd {
			name := extractPortDefName(line)
			if name != "" && !seenPortDefs[name] {
				seenPortDefs[name] = true
				portDefs = append(portDefs, line)
			}
		}
		for _, line := range aa {
			name := extractAbstractActionName(line)
			if name != "" && !seenAbstractActions[name] {
				seenAbstractActions[name] = true
				abstractActions = append(abstractActions, line)
			}
		}
		blockDefs = append(blockDefs, bd...)
		behaviorDefs = append(behaviorDefs, beh...)
		for _, part := range ibd {
			fragments = append(fragments, parseSystemFragment(part))
		}
	}

	model := t.emitCloudPackage(portDefs, abstractActions, blockDefs, behaviorDefs, fragments)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(model))
}

// parsePackage parses a SysML v2 package (as produced by SModeling) and returns
// the port definitions, block definitions (part def), IBD parts (part instances),
// abstract action defs, and behavior action defs.
func parsePackage(text string) (portDefs []string, blockDefs []string, ibdParts []string, abstractActions []string, behaviorDefs []string) {
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

		// Single-line abstract action definition.
		if strings.HasPrefix(trimmed, "abstract action def ") {
			abstractActions = append(abstractActions, lines[i])
			i++
			continue
		}

		// Multi-line behavior action definition.
		if strings.HasPrefix(trimmed, "action def ") {
			block, consumed := extractBlock(lines, i)
			if block != "" {
				behaviorDefs = append(behaviorDefs, block)
			}
			i += consumed
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

// extractAbstractActionName extracts the name from an "    abstract action def <Name>;" line.
func extractAbstractActionName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "abstract action def ") {
		return ""
	}
	name := strings.TrimPrefix(trimmed, "abstract action def ")
	name = strings.Trim(name, "; \t")
	return name
}

//-------------------------------------LocalCloud assembly

// systemFragment holds the structural data extracted from one system's IBD part.
// It captures enough to place the system inside a LocalCloud instance and to
// resolve its @connect entries against providers elsewhere in the cloud.
type systemFragment struct {
	partName    string         // e.g. "thermostat"
	typeName    string         // e.g. "thermostatSystem"
	host        string         // e.g. "canbus"
	ipAddresses []string       // IP addresses associated with the host
	ports       map[string]int // protocol → port number
	provides    []providesEntry
	connects    []connectEntry
}

// providesEntry records one service exposed by this system.
type providesEntry struct {
	asset      string // e.g. "controller_1"
	definition string // e.g. "setpoint"
	url        string
}

// connectEntry records one cervice this system consumes, with the URL of the
// resolved provider (or an empty URL when no provider was registered).
type connectEntry struct {
	asset   string // consuming asset name
	cervice string // consumed service definition
	url     string // provider URL, or "" when unresolved
}

var (
	ibdHeaderRe = regexp.MustCompile(`^\s*part\s+'([^']+)'\s*:\s*'([^']+)'`)
	hostAttrRe  = regexp.MustCompile(`attribute\s+host\s*:\s*String\s*=\s*"([^"]+)"`)
	ipAttrRe    = regexp.MustCompile(`attribute\s+ipAddress\s*:\s*String\s*=\s*"([^"]+)"`)
	portAttrRe  = regexp.MustCompile(`attribute\s+(\w+)Port\s*:\s*Integer\s*=\s*(\d+)`)
	providesRe  = regexp.MustCompile(`//\s*provides\s+([^.\s]+)\.([^\s]+)\s+at\s+(\S+)`)
	connectRe   = regexp.MustCompile(`//\s*@connect\s+([^.\s]+)\.([^\s]+)\s+→\s+(.+?)\s*$`)
)

// parseSystemFragment walks the lines of one IBD "part '<name>' : '<type>' { ... }"
// block and pulls out the structural data the modeler needs to re-emit the
// system inside a LocalCloud.
func parseSystemFragment(ibdPart string) systemFragment {
	frag := systemFragment{ports: make(map[string]int)}
	for _, line := range strings.Split(ibdPart, "\n") {
		if m := ibdHeaderRe.FindStringSubmatch(line); m != nil && frag.partName == "" {
			frag.partName = m[1]
			frag.typeName = m[2]
			continue
		}
		if m := hostAttrRe.FindStringSubmatch(line); m != nil {
			frag.host = m[1]
			continue
		}
		if m := ipAttrRe.FindStringSubmatch(line); m != nil {
			frag.ipAddresses = append(frag.ipAddresses, m[1])
			continue
		}
		if m := portAttrRe.FindStringSubmatch(line); m != nil {
			if p, err := strconv.Atoi(m[2]); err == nil {
				frag.ports[m[1]] = p
			}
			continue
		}
		if m := providesRe.FindStringSubmatch(line); m != nil {
			frag.provides = append(frag.provides, providesEntry{
				asset: m[1], definition: m[2], url: m[3],
			})
			continue
		}
		if m := connectRe.FindStringSubmatch(line); m != nil {
			url := strings.TrimSpace(m[3])
			// An unresolved consumer ("(no registered provider)") has no URL to
			// match; normalise that to an empty string so the emitter can treat
			// it uniformly.
			if strings.HasPrefix(url, "(") {
				url = ""
			}
			frag.connects = append(frag.connects, connectEntry{
				asset: m[1], cervice: m[2], url: url,
			})
			continue
		}
	}
	return frag
}

// emitCloudPackage produces the complete SysML v2 package representing the
// local cloud: port defs, abstract action defs, the Host and LocalCloud type
// definitions, per-system block defs, behaviour defs, and a LocalCloud IBD
// instance containing host parts, system parts, and formal connect statements.
func (t *Traits) emitCloudPackage(portDefs, abstractActions, blockDefs, behaviorDefs []string, fragments []systemFragment) string {
	// URL → (systemPart, assetName, definition) lookup for connect resolution.
	providerIndex := make(map[string][3]string)
	for _, f := range fragments {
		for _, p := range f.provides {
			providerIndex[p.url] = [3]string{f.partName, p.asset, p.definition}
		}
	}

	// Unique host info aggregated across systems.
	type hostInfo struct {
		name string
		ips  []string
	}
	hosts := make(map[string]*hostInfo)
	for _, f := range fragments {
		if f.host == "" {
			continue
		}
		h, ok := hosts[f.host]
		if !ok {
			h = &hostInfo{name: f.host}
			hosts[f.host] = h
		}
		for _, ip := range f.ipAddresses {
			if !containsString(h.ips, ip) {
				h.ips = append(h.ips, ip)
			}
		}
	}
	hostNames := make([]string, 0, len(hosts))
	for k := range hosts {
		hostNames = append(hostNames, k)
	}
	sort.Strings(hostNames)

	// Stable order for system emission.
	fragSorted := make([]systemFragment, len(fragments))
	copy(fragSorted, fragments)
	sort.Slice(fragSorted, func(i, j int) bool { return fragSorted[i].partName < fragSorted[j].partName })

	var sb strings.Builder
	fmt.Fprintf(&sb, "package '%s' {\n\n", t.CloudName)

	if len(portDefs) > 0 {
		sb.WriteString("    // ── Port Definitions ─────────────────────────────────────────────────────\n")
		for _, line := range portDefs {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	if len(abstractActions) > 0 {
		sb.WriteString("    // ── Abstract Action Definitions ──────────────────────────────────────────\n")
		for _, line := range abstractActions {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("    // ── Host Definition ──────────────────────────────────────────────────────\n")
	sb.WriteString("    part def 'Host' {\n")
	sb.WriteString("        attribute name : String;\n")
	sb.WriteString("        attribute ipAddress : String[*];\n")
	sb.WriteString("    }\n\n")

	if len(blockDefs) > 0 {
		sb.WriteString("    // ── Block Definitions (BDD) ──────────────────────────────────────────────\n")
		for _, block := range blockDefs {
			sb.WriteString(block + "\n\n")
		}
	}

	// LocalCloud type: lists every host and every system as a part usage.
	sb.WriteString("    part def 'LocalCloud' {\n")
	sb.WriteString("        attribute name : String;\n")
	for _, h := range hostNames {
		fmt.Fprintf(&sb, "        part %s : 'Host';\n", quotedIfNeeded(h))
	}
	for _, f := range fragSorted {
		fmt.Fprintf(&sb, "        part %s : '%s';\n", quotedIfNeeded(f.partName), f.typeName)
	}
	sb.WriteString("    }\n\n")

	if len(behaviorDefs) > 0 {
		sb.WriteString("    // ── Behaviour Definitions ────────────────────────────────────────────────\n")
		for _, block := range behaviorDefs {
			sb.WriteString(block + "\n\n")
		}
	}

	// LocalCloud instance (IBD): hosts, systems with attribute values, and connects.
	sb.WriteString("    // ── Internal Block Diagram (IBD) ─────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "    part '%s' : 'LocalCloud' {\n", t.CloudName)
	fmt.Fprintf(&sb, "        attribute name : String = \"%s\";\n\n", t.CloudName)

	for _, h := range hostNames {
		info := hosts[h]
		fmt.Fprintf(&sb, "        part %s : 'Host' {\n", quotedIfNeeded(h))
		fmt.Fprintf(&sb, "            attribute name : String = \"%s\";\n", h)
		for _, ip := range info.ips {
			fmt.Fprintf(&sb, "            attribute ipAddress : String = \"%s\";\n", ip)
		}
		sb.WriteString("        }\n\n")
	}

	for _, f := range fragSorted {
		fmt.Fprintf(&sb, "        part %s : '%s' {\n", quotedIfNeeded(f.partName), f.typeName)
		if f.host != "" {
			fmt.Fprintf(&sb, "            attribute host : String = \"%s\";\n", f.host)
		}
		protos := make([]string, 0, len(f.ports))
		for p := range f.ports {
			protos = append(protos, p)
		}
		sort.Strings(protos)
		for _, p := range protos {
			fmt.Fprintf(&sb, "            attribute %sPort : Integer = %d;\n", p, f.ports[p])
		}
		for _, pr := range f.provides {
			fmt.Fprintf(&sb, "            // provides: %s\n", pr.url)
		}
		sb.WriteString("        }\n\n")
	}

	// Connects: resolve each consumer's @connect URL against the provider index.
	var resolved, unresolved []string
	for _, f := range fragSorted {
		for _, c := range f.connects {
			consumerPath := buildPath(f.partName, c.asset, c.cervice)
			if c.url == "" {
				unresolved = append(unresolved,
					fmt.Sprintf("        // %s has no registered provider", consumerPath))
				continue
			}
			provider, ok := providerIndex[c.url]
			if !ok {
				unresolved = append(unresolved,
					fmt.Sprintf("        // %s → %s (provider not in cloud)", consumerPath, c.url))
				continue
			}
			providerPath := buildPath(provider[0], provider[1], provider[2])
			resolved = append(resolved,
				fmt.Sprintf("        connect %s to %s;", consumerPath, providerPath))
		}
	}
	if len(resolved) > 0 || len(unresolved) > 0 {
		sb.WriteString("        // ── Connections ──────────────────────────────────────────────────────\n")
		for _, l := range resolved {
			sb.WriteString(l + "\n")
		}
		for _, l := range unresolved {
			sb.WriteString(l + "\n")
		}
	}

	sb.WriteString("    }\n")
	sb.WriteString("}\n")
	return sb.String()
}

// quotedIfNeeded wraps a SysML name in single quotes when it contains
// characters that would be invalid in an unquoted identifier.
func quotedIfNeeded(name string) string {
	if needsQuoting(name) {
		return "'" + name + "'"
	}
	return name
}

func needsQuoting(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 && unicode.IsDigit(r) {
			return true
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return true
		}
	}
	return false
}

// buildPath joins path segments with dots, quoting each segment individually.
func buildPath(segments ...string) string {
	out := make([]string, len(segments))
	for i, s := range segments {
		out[i] = quotedIfNeeded(s)
	}
	return strings.Join(out, ".")
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
