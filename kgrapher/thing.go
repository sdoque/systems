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
	owner          *components.System        `json:"-"`
	name           string                    `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
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

	return &components.UnitAsset{
		Name:        "assembler",
		Mission:     "handle_triplestore",
		Details:     map[string][]string{"Type": {"Interactive"}},
		ServicesMap: map[string]*components.Service{cloudgraph.SubPath: &cloudgraph, localOntologies.SubPath: &localOntologies},
		Traits: &Traits{
			TripleStoreURL: "http://localhost:7200/repositories/Arrowhead/statements",
			LOntologies: map[string]string{
				"alc": "alc-ontology-local.ttl",
			},
		},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration
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

	// Ensure that you have a valid local ontology directory
	const dir = "./files"
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("could not create directory %q: %v", dir, err)
	}
	serverAddress := sys.Husk.Host.IPAddresses[0]
	ontologyURL := fmt.Sprintf("http://%s:20105/kgrapher/assembler/files/", serverAddress)
	resolveLocalOntologies(t.LOntologies, dir, ontologyURL)

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
		log.Println("Disconnecting from GraphDB")
	}
}

// resolveLocalOntologies checks if the local ontology files exist in the specified directory.
func resolveLocalOntologies(localOntologies map[string]string, dir string, baseURL string) {
	for prefix, filename := range localOntologies {
		fullPath := filepath.Join(dir, filename)

		if _, err := os.Stat(fullPath); err == nil {
			localOntologies[prefix] = baseURL + filename
		} else {
			fmt.Printf("Warning: ontology file %s not found in %s. Removing prefix '%s'.\n", filename, dir, prefix)
			delete(localOntologies, prefix)
		}
	}
}

// -------------------------------------Unit asset's function methods

// assembleOntologies gets the list of systems from the lead registrar and then the ontology of each system
func (t *Traits) assembleOntologies(w http.ResponseWriter) {
	leadingRegistrarURL, err := components.GetRunningCoreSystemURL(t.owner, "serviceregistrar")
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

	systemsList, ok := sL.(*forms.SystemRecordList_v1)
	if !ok {
		fmt.Println("Problem unpacking the service registration reply")
		return
	}

	prefixes := make(map[string]bool)
	processedBlocks := make(map[string]bool)
	var uniqueIndividuals []string

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

		text := strings.ReplaceAll(string(bodyBytes), "\r\n", "\n")
		blocks := strings.Split(text, "\n\n")

		for _, block := range blocks {
			normalizedBlock := strings.TrimSpace(block)
			if normalizedBlock == "" {
				continue
			}
			if processedBlocks[normalizedBlock] {
				continue
			}

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

	var cloudIRI string
	{
		seen := map[string]struct{}{}
		for _, blk := range uniqueIndividuals {
			if !isSystemBlock(blk) {
				continue
			}
			vals := extractContainedIns(blk)
			local := map[string]struct{}{}
			for _, v := range vals {
				local[v] = struct{}{}
			}
			if len(local) > 1 {
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
			cloudIRI = k
		}
	}

	for i, blk := range uniqueIndividuals {
		if isSystemBlock(blk) && len(extractContainedIns(blk)) == 0 {
			uniqueIndividuals[i] = injectContainedIn(blk, cloudIRI)
		}
	}

	uniqueIndividuals = addCloudPrefixToBlocks(uniqueIndividuals, cloudIRI)

	var graph string

	for prefix := range prefixes {
		graph += prefix + "\n"
	}

	ontoImport := "\nalc:ontology a owl:Ontology "
	for _, uri := range t.LOntologies {
		ontoImport += fmt.Sprintf(";\n    owl:imports <%s> ", uri)
	}
	ontoImport += ".\n"
	graph += ontoImport + "\n"

	for _, block := range uniqueIndividuals {
		graph += block + "\n\n"
	}

	w.Header().Set("Content-Type", "text/turtle")
	w.Write([]byte(graph))

	snapshotT := time.Now().UTC().Format(time.RFC3339)
	snapshotIRI := "urn:snapshots:" + snapshotT

	statementsURL := t.TripleStoreURL
	repoBase := strings.TrimSuffix(t.TripleStoreURL, "/statements")

	gspURL := repoBase + "/rdf-graphs/service?graph=" + url.QueryEscape(snapshotIRI)

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

// updatePrefixes updates the prefixes in the RDF blocks with the new URIs from the local ontologies.
func updatePrefixes(prefixes map[string]bool, prefixUpdates map[string]string) {
	updated := make(map[string]bool)

	for line := range prefixes {
		if strings.HasPrefix(line, "@prefix") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				prefix := strings.TrimSuffix(parts[1], ":")
				if newURI, ok := prefixUpdates[prefix]; ok {
					line = fmt.Sprintf("@prefix %s: <%s#> .", prefix, newURI)
				}
			}
		}
		updated[line] = true
	}

	for k := range prefixes {
		delete(prefixes, k)
	}
	for k := range updated {
		prefixes[k] = true
	}
}

// addCloudPrefixToBlocks prefixes all alc: subjects with "<CloudName>_".
func addCloudPrefixToBlocks(blocks []string, cloudIRI string) []string {
	cloudIRI = ensurePrefixed(cloudIRI)
	cloudName := extractCloudName(cloudIRI)
	if cloudName == "" {
		return blocks
	}

	mapping := map[string]string{}
	for _, blk := range blocks {
		subj := extractSubject(blk)
		if !strings.HasPrefix(subj, "alc:") {
			continue
		}
		rest := strings.TrimPrefix(subj, "alc:")
		if rest == "" {
			continue
		}
		if rest == cloudName || rest == "ontology" {
			continue
		}
		if strings.HasPrefix(rest, cloudName+"_") {
			continue
		}
		newSubj := "alc:" + cloudName + "_" + rest
		mapping[subj] = newSubj
	}

	if len(mapping) == 0 {
		return blocks
	}

	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})

	out := make([]string, len(blocks))
	for i, blk := range blocks {
		txt := blk
		for _, old := range keys {
			txt = strings.ReplaceAll(txt, old, mapping[old])
		}
		out[i] = txt
	}
	return out
}

// extractCloudName gets the local name from a cloud IRI.
func extractCloudName(iri string) string {
	iri = strings.TrimSpace(iri)
	if iri == "" {
		return ""
	}

	if strings.HasPrefix(iri, "<") && strings.HasSuffix(iri, ">") {
		inner := iri[1 : len(iri)-1]
		idx := strings.LastIndexAny(inner, "#/")
		if idx >= 0 && idx+1 < len(inner) {
			return inner[idx+1:]
		}
		return inner
	}

	if strings.Contains(iri, ":") {
		parts := strings.SplitN(iri, ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}

	return iri
}

// ------------------------------------- Local cloud containing all systems

// ensurePrefixed returns v with "alc:" prefix unless it's already an IRI or prefixed.
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
		return parts[0]
	}
	return ""
}

// extractContainedIns finds all afo:isContainedIn objects in a block.
func extractContainedIns(block string) []string {
	var found []string
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "afo:isContainedIn ") {
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

// injectContainedIn inserts "afo:isContainedIn <iri>" as one of the system's predicates.
func injectContainedIn(block, iri string) string {
	iri = ensurePrefixed(iri)

	if len(extractContainedIns(block)) > 0 {
		return block
	}

	trim := strings.TrimRight(block, " \t\r\n")
	if trim == "" {
		return block
	}

	if strings.HasSuffix(trim, ".") {
		core := strings.TrimSuffix(trim, ".")
		return core + " ;\n    afo:isContainedIn " + iri + " ."
	}

	subj := extractSubject(block)
	if subj == "" {
		return block
	}
	return trim + "\n" + fmt.Sprintf("%s afo:isContainedIn %s .", subj, iri)
}

// detectGlobalCloud validates there is at most one unique LocalCloud across all system blocks.
func detectGlobalCloud(blocks []string) (string, error) {
	set := map[string]struct{}{}
	for _, b := range blocks {
		if !isSystemBlock(b) {
			continue
		}
		vals := extractContainedIns(b)
		local := map[string]struct{}{}
		for _, v := range vals {
			local[ensurePrefixed(v)] = struct{}{}
		}
		if len(local) > 1 {
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
			return k, nil
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

// localOntologies reads the ./files directory and builds an HTML list
func (t *Traits) localOntologies(sp string) string {
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
		link := sp + t.name + "/files/" + name
		sb.WriteString(fmt.Sprintf(`<li><a href="%s">%s</a></li>`, link, name))
	}

	sb.WriteString(`</ul>
</body></html>`)
	return sb.String()
}
