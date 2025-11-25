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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters
type Traits struct {
	SystemList     forms.SystemRecordList_v1 `json:"-"`
	TripleStoreURL string                    `json:"graphDBurl"`
	LOntologies    map[string]string         `json:"localOntologies"` // map of ontology names to their file paths
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	// Asset-specific parameters
	Traits
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetServices() components.Services {
	return ua.ServicesMap
}

// GetCervices returns the list of consumed services by the Resource.
func (ua *UnitAsset) GetCervices() components.Cervices {
	return ua.CervicesMap
}

// GetDetails returns the details of the Resource.
func (ua *UnitAsset) GetDetails() map[string][]string {
	return ua.Details
}

// GetTraits returns the traits of the Resource.
func (ua *UnitAsset) GetTraits() any {
	return ua.Traits
}

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	cloudgraph := components.Service{
		Definition:  "cloudgraph",
		SubPath:     "cloudgraph",
		Details:     map[string][]string{"Format": {"Turtle"}},
		RegPeriod:   61,
		Description: "provides the knowledge graph of a local cloud (GET)",
	}

	localOntologies := components.Service{
		Definition:  "localOntologies",
		SubPath:     "localontologies",
		Details:     map[string][]string{"Location": {"Files"}},
		RegPeriod:   61,
		Description: "provides the list of local ontologies (GET)",
	}

	// var uat components.UnitAsset // this is an interface, which we then initialize
	uat := &UnitAsset{
		Name:        "assembler",
		Owner:       &components.System{},
		Details:     map[string][]string{"Type": {"Interactive"}},
		ServicesMap: map[string]*components.Service{cloudgraph.SubPath: &cloudgraph, localOntologies.SubPath: &localOntologies},
		Traits: Traits{
			TripleStoreURL: "http://localhost:7200/repositories/Arrowhead/statements",
			LOntologies: map[string]string{
				"alc": "alc-ontology-local.ttl", // Initialize the map for local ontologies
			},
		},
	}
	return uat
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	// var ua components.UnitAsset // this is an interface, which we then initialize
	ua := &UnitAsset{ // this is an interface, which we then initialize
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	// Ensure that you have a valid local ontology directory
	const dir = "./files"
	// 1. Ensure ./files exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("could not create directory %q: %v", dir, err)
	}
	serverAddress := ua.Owner.Host.IPAddresses[0]                                          // Use the first IP address of the system
	ontologyURL := fmt.Sprintf("http://%s:20105/kgrapher/assembler/files/", serverAddress) //only using http for now TODO: use https
	// 2. Resolve local ontologies to their full URLs
	resolveLocalOntologies(ua.LOntologies, dir, ontologyURL)

	return ua, func() {
		log.Println("Disconnecting from GraphDB")
	}
}

// UnmarshalTraits un-marshals a slice of json.RawMessage into a slice of Traits.
func UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("failed to unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

// resolveLocalOntologies checks if the local ontology files exist in the specified directory.
// If they do, it updates the map with the full URL; if not, it removes the entry and logs a warning.
func resolveLocalOntologies(localOntologies map[string]string, dir string, baseURL string) {
	for prefix, filename := range localOntologies {
		fullPath := filepath.Join(dir, filename)

		if _, err := os.Stat(fullPath); err == nil {
			// File exists: update to full URL
			localOntologies[prefix] = baseURL + filename
		} else {
			// File does not exist: remove entry and warn
			fmt.Printf("Warning: ontology file %s not found in %s. Removing prefix '%s'.\n", filename, dir, prefix)
			delete(localOntologies, prefix)
		}
	}
}

// -------------------------------------Unit asset's function methods

// assembles ontologies gets the list of systems from the lead registrar and then the ontology of each system
func (ua *UnitAsset) assembleOntologies(w http.ResponseWriter) {
	// 1) Discover the leading Service Registrar and request the cloud's system list
	leadingRegistrarURL, err := components.GetRunningCoreSystemURL(ua.Owner, "serviceregistrar")
	if err != nil {
		log.Printf("Error getting the leading service registrar URL: %s\n", err)
		http.Error(w, "Internal Server Error: unable to get leading service registrar URL", http.StatusInternalServerError)
		return
	}
	leadUrl := leadingRegistrarURL + "/syslist"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequest(http.MethodGet, leadUrl, nil)
	if err != nil {
		log.Printf("Error getting the systems list from service registrar, %s\n", err)
		return
	}
	req = req.WithContext(ctx)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error receiving the systems list from service registrar, %s\n", err)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("GetRValue-Error reading registration response body: %v", err)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		fmt.Println("Error parsing media type:", err)
		return
	}
	sL, err := usecases.Unpack(bodyBytes, mediaType)
	if err != nil {
		log.Printf("error extracting the systems list reply %v\n", err)
		return
	}

	// 2) Assert we got a SystemRecordList, which contains the list of system base URLs
	systemsList, ok := sL.(*forms.SystemRecordList_v1)
	if !ok {
		fmt.Println("Problem unpacking the service registration reply")
		return
	}

	// 3) Collect prefixes (deduped), and individual TTL blocks (deduped)
	prefixes := make(map[string]bool)        // "@prefix ..." lines we keep once
	processedBlocks := make(map[string]bool) // for deduping RDF blocks across systems
	var uniqueIndividuals []string           // all individuals we will output

	for _, s := range systemsList.List {
		sysUrl := s + "/kgraph"
		fmt.Println(sysUrl)

		resp, err := http.Get(sysUrl)
		if err != nil {
			log.Printf("Unable to get ontology from %s: %s\n", s, err)
			continue
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading ontology response from %s: %s\n", s, err)
			continue
		}

		// Normalize CRLF to LF so splitting works even if a system returns Windows newlines.
		text := strings.ReplaceAll(string(bodyBytes), "\r\n", "\n")

		// Split into individual RDF blocks; your systems emit blocks separated by a blank line.
		blocks := strings.Split(text, "\n\n") // :contentReference[oaicite:4]{index=4}

		for _, block := range blocks {
			normalizedBlock := strings.TrimSpace(block)
			if normalizedBlock == "" {
				continue
			}
			if processedBlocks[normalizedBlock] {
				continue // already seen
			}

			// Collect @prefix lines (keep them out of individuals)
			if strings.HasPrefix(normalizedBlock, "@prefix") {
				for _, line := range strings.Split(normalizedBlock, "\n") {
					if strings.HasPrefix(line, "@prefix") {
						prefixes[line] = true
					}
				}
				continue
			}

			processedBlocks[normalizedBlock] = true
			uniqueIndividuals = append(uniqueIndividuals, normalizedBlock)
		}
	}

	// 4) Detect a single cloud IRI from any afo:System blocks that already declare afo:isContainedIn
	//    (We do this AFTER collection, to avoid running on an empty slice.)
	var cloudIRI string
	{
		seen := map[string]struct{}{}
		for _, blk := range uniqueIndividuals {
			if !isSystemBlock(blk) { // helper in your file
				continue
			}
			vals := extractContainedIns(blk) // helper in your file
			// De-dup per block; error if a single system declares more than one cloud.
			local := map[string]struct{}{}
			for _, v := range vals {
				local[v] = struct{}{}
			}
			if len(local) > 1 {
				// report early with the concrete subject for clarity
				http.Error(w, fmt.Sprintf("Bad Request: system %s has conflicting afo:isContainedIn values", extractSubject(blk)), http.StatusBadRequest)
				return
			}
			for k := range local {
				seen[k] = struct{}{}
			}
		}
		if len(seen) == 0 {
			http.Error(w, "Bad Request: no afo:isContainedIn found; please declare a LocalCloud in at least one system", http.StatusBadRequest)
			return
		}
		if len(seen) > 1 {
			var all []string
			for k := range seen {
				all = append(all, k)
			}
			sort.Strings(all)
			http.Error(w, fmt.Sprintf("Bad Request: multiple LocalClouds detected across systems: %v", all), http.StatusBadRequest)
			return
		}
		for k := range seen {
			cloudIRI = k // the single agreed value
		}
	}

	// 5) Ensure every afo:System has afo:isContainedIn cloudIRI (append a separate triple when missing)
	for i, blk := range uniqueIndividuals {
		if isSystemBlock(blk) && len(extractContainedIns(blk)) == 0 {
			uniqueIndividuals[i] = injectContainedIn(blk, cloudIRI) // helper in your file
		}
	}

	// 6) Build the final graph: prefixes (once), ontology header with imports, then all blocks
	var graph string

	for prefix := range prefixes {
		graph += prefix + "\n"
	}

	ontoImport := "\nalc:ontology a owl:Ontology "
	for _, uri := range ua.Traits.LOntologies {
		ontoImport += fmt.Sprintf(";\n    owl:imports <%s> ", uri)
	}
	ontoImport += ".\n"
	graph += ontoImport + "\n"

	for _, block := range uniqueIndividuals {
		graph += block + "\n\n"
	}

	// 7) Return to browser and POST to GraphDB (now: snapshot + replace current)
	w.Header().Set("Content-Type", "text/turtle")
	w.Write([]byte(graph))

	// Build a snapshot IRI (UTC is simplest to compare later)
	snapshotT := time.Now().UTC().Format(time.RFC3339) // e.g. 2025-11-14T09:17:23Z
	snapshotIRI := "urn:snapshots:" + snapshotT

	// Derive endpoints from ua.TripleStoreURL
	statementsURL := ua.TripleStoreURL // expected: .../repositories/<repo>/statements
	repoBase := strings.TrimSuffix(ua.TripleStoreURL, "/statements")

	// Graph Store HTTP Protocol endpoint for indirectly-referenced graphs
	// If you pass repo *base* (…/repositories/<repo>) instead, use:
	// gspURL := repoBase + "/rdf-graphs/service?graph=" + url.QueryEscape(snapshotIRI)
	gspURL := repoBase + "/rdf-graphs/service?graph=" + url.QueryEscape(snapshotIRI)

	// --- 7a) PUT the TTL into the snapshot named graph
	req, err = http.NewRequest(http.MethodPut, gspURL, bytes.NewBuffer([]byte(graph)))
	if err != nil {
		log.Println("Error creating snapshot PUT:", err)
		return
	}
	req.Header.Set("Content-Type", "text/turtle")

	client = &http.Client{}
	resp, err = client.Do(req)
	if err != nil {
		log.Println("Error PUTting snapshot:", err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		log.Printf("Snapshot PUT failed: %s\n%s\n", resp.Status, string(respBody))
		return
	}

	// --- 7b) Replace <urn:state:current> with this snapshot (single SPARQL UPDATE)
	update := fmt.Sprintf(`CLEAR GRAPH <urn:state:current>;
ADD GRAPH <%s> TO <urn:state:current>;`, snapshotIRI)

	form := url.Values{"update": {update}}
	req2, err := http.NewRequest(http.MethodPost, statementsURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		log.Println("Error creating update POST:", err)
		return
	}
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	resp2, err := client.Do(req2)
	if err != nil {
		log.Println("Error POSTing update:", err)
		return
	}
	defer resp2.Body.Close()
	resp2Body, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode/100 != 2 {
		log.Printf("Update failed: %s\n%s\n", resp2.Status, string(resp2Body))
		return
	}

	fmt.Println("Snapshot stored at:", snapshotIRI)
	fmt.Println("Current graph replaced from snapshot.")

}

// updatePrefix_Target updates the prefixes in the RDF blocks with the new URIs from the local ontologies.
func updatePrefixes(prefixes map[string]bool, prefixUpdates map[string]string) {
	updated := make(map[string]bool)

	for line := range prefixes {
		if strings.HasPrefix(line, "@prefix") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				prefix := strings.TrimSuffix(parts[1], ":") // e.g., "alc"
				if newURI, ok := prefixUpdates[prefix]; ok {
					// Update the line with the new URI
					line = fmt.Sprintf("@prefix %s: <%s#> .", prefix, newURI)
				}
			}
		}
		updated[line] = true
	}

	// Replace the original map with the updated one
	for k := range prefixes {
		delete(prefixes, k)
	}
	for k := range updated {
		prefixes[k] = true
	}
}

// ------------------------------------- Local cloud containing all systems

// ensurePrefixed returns v with "alc:" prefix unless it's already an IRI (<...>) or a prefixed/absolute IRI (contains ':').
func ensurePrefixed(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if (strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")) || strings.Contains(v, ":") {
		return v
	}
	return "alc:" + v
}

// isSystemBlock reports whether this TTL block defines an afo:System individual.
func isSystemBlock(block string) bool {
	// Heuristic: first line looks like: "alc:foo a afo:System ;" or ends with "."
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) == 0 {
		return false
	}
	first := lines[0]
	return strings.Contains(first, " a afo:System ")
}

// extractSubject returns the subject IRI (first token of the first line).
func extractSubject(block string) string {
	first := strings.Split(strings.TrimSpace(block), "\n")[0]
	parts := strings.Fields(first)
	if len(parts) > 0 {
		return parts[0] // e.g., "alc:aiko_ds18b20"
	}
	return ""
}

// extractContainedIns finds all afo:isContainedIn objects in a block.
func extractContainedIns(block string) []string {
	var found []string
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "afo:isContainedIn ") {
			// very simple parse: take everything after predicate up to ';' or '.'
			after := strings.SplitN(line, "afo:isContainedIn", 2)[1]
			after = strings.TrimSpace(after)
			after = strings.TrimRight(after, " ;.")
			if after != "" {
				found = append(found, after)
			}
		}
	}
	return found
}

// injectContainedIn inserts "afo:isContainedIn <iri>" as one of the system's predicates
// by replacing the final '.' of the block with " ;\n    afo:isContainedIn <iri> ."
// This keeps the triple *inside* the subject's predicate list (not as a separate triple).
// If the block doesn't end with '.', we fall back to appending a separate triple.
func injectContainedIn(block, iri string) string {
	iri = ensurePrefixed(iri)

	// Don't add if it's already present.
	if len(extractContainedIns(block)) > 0 {
		return block
	}

	trim := strings.TrimRight(block, " \t\r\n")
	if trim == "" {
		return block
	}

	// Normal case: the subject block ends with a single '.'
	if strings.HasSuffix(trim, ".") {
		// Replace the trailing '.' with " ;\n    afo:isContainedIn <iri> ."
		core := strings.TrimSuffix(trim, ".")
		return core + " ;\n    afo:isContainedIn " + iri + " .\n"
	}

	// Fallback: append as a separate triple with explicit subject.
	subj := extractSubject(block)
	if subj == "" {
		return block
	}
	return trim + "\n" + fmt.Sprintf("%s afo:isContainedIn %s .\n", subj, iri)
}

// detectGlobalCloud validates there is at most one unique LocalCloud across all system blocks.
// Returns that single IRI (normalized) or an error if conflicting values are found.
// If none is provided anywhere, returns "" and no error (caller decides policy).
func detectGlobalCloud(blocks []string) (string, error) {
	set := map[string]struct{}{}
	for _, b := range blocks {
		if !isSystemBlock(b) {
			continue
		}
		vals := extractContainedIns(b)
		// de-dupe within block and normalize
		local := map[string]struct{}{}
		for _, v := range vals {
			local[ensurePrefixed(v)] = struct{}{}
		}
		if len(local) > 1 {
			// a single system declares conflicting values
			var ls []string
			for k := range local {
				ls = append(ls, k)
			}
			sort.Strings(ls)
			return "", fmt.Errorf("a system block has conflicting afo:isContainedIn values: %v", ls)
		}
		for k := range local {
			set[k] = struct{}{}
		}
	}
	if len(set) <= 1 {
		for k := range set {
			return k, nil // the only value, or "" if none
		}
		return "", nil
	}
	var gs []string
	for k := range set {
		gs = append(gs, k)
	}
	sort.Strings(gs)
	return "", fmt.Errorf("multiple LocalClouds detected across systems: %v", gs)
}

// ----------- Local Ontologies Service -----------------------------------------------------------

// localOntologiesHandler handles requests to the /localontologies endpoint
// localOntologies reads the ./ontologies directory and builds an HTML list
func (ua *UnitAsset) localOntologies(sp string) string {
	entries, err := os.ReadDir("./files")
	if err != nil {
		return fmt.Sprintf("<p><strong>Error:</strong> could not read files directory: %v</p>", err)
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html><head>
<meta charset="utf-8"><title>Available Ontologies</title>
</head><body>
<h1>Available Ontologies</h1>
<ul>`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// <a href="/Foo.owl">Foo.owl</a> will now hit your FileServer at "/" and serve ./files/Foo.owl
		link := sp + ua.Name + "/files/" + name
		sb.WriteString(fmt.Sprintf(`<li><a href="%s">%s</a></li>`, link, name))
	}

	sb.WriteString(`</ul>
</body></html>`)
	return sb.String()
}
