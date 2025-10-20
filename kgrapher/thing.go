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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters
type Traits struct {
	SystemList    forms.SystemRecordList_v1 `json:"-"`
	RepositoryURL string                    `json:"graphDBurl"`
	LOntologies   map[string]string         `json:"localOntologies"` // map of ontology names to their file paths
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
			RepositoryURL: "http://localhost:7200/repositories/Arrowhead/statements",
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
	// Look for leading service registrar

	leadingRegistrarURL, err := components.GetRunningCoreSystemURL(ua.Owner, "serviceregistrar")
	if err != nil {
		log.Printf("Error getting the leading service registrar URL: %s\n", err)
		http.Error(w, "Internal Server Error: unable to get leading service registrar URL", http.StatusInternalServerError)
		return
	}
	// request list of systems in the cloud from the leading service registrar
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

	// Perform a type assertion to convert the returned Form to ServiceRecord_v1
	systemsList, ok := sL.(*forms.SystemRecordList_v1)
	if !ok {
		fmt.Println("Problem unpacking the service registration reply")
		return
	}

	// Prepare the local cloud's knowledge graph by asking each system their their knowledge graph
	prefixes := make(map[string]bool)        // To store unique prefixes
	processedBlocks := make(map[string]bool) // To track processed RDF blocks
	var uniqueIndividuals []string           // To store unique RDF individuals

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

		// Split into individual RDF blocks
		blocks := strings.Split(string(bodyBytes), "\n\n") // Assuming blocks are separated by newlines

		for _, block := range blocks {
			normalizedBlock := strings.TrimSpace(block)
			if processedBlocks[normalizedBlock] {
				// Skip duplicate block
				continue
			}

			// Extract prefixes only from the first pass and add to the prefixes map
			if strings.HasPrefix(normalizedBlock, "@prefix") {
				lines := strings.Split(normalizedBlock, "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "@prefix") {
						prefixes[line] = true // Add unique prefixes
					}
				}
				continue // Skip adding prefixes as RDF blocks
			}

			// Mark this block as processed and add to individuals
			processedBlocks[normalizedBlock] = true
			uniqueIndividuals = append(uniqueIndividuals, normalizedBlock)
		}
	}

	// Construct the graph string
	var graph string

	// updatePrefixes(prefixes, ua.Traits.LOntologies) //update prefixes with local ontology URIs TO DO: remove function call, it is not used anymore
	// Write unique prefixes once
	for prefix := range prefixes {
		graph += prefix + "\n"
	}

	// Add the ontology definition
	ontoImport := "\n:ontology a owl:Ontology "
	for _, uri := range ua.Traits.LOntologies {
		ontoImport += fmt.Sprintf(";\n    owl:imports <%s> ", uri)
	}
	ontoImport += ".\n"
	graph += ontoImport + "\n"

	// Write unique RDF blocks
	for _, block := range uniqueIndividuals {
		graph += block + "\n\n"
	}

	// Send the knowledge graph to the browser
	w.Header().Set("Content-Type", "text/turtle")
	w.Write([]byte(graph))

	// Send the knowledge graph to GraphDB
	req, err = http.NewRequest("POST", ua.RepositoryURL, bytes.NewBuffer([]byte(graph)))
	if err != nil {
		fmt.Println("Error creating the request to the database:", err)
		return
	}

	// Set appropriate headers
	req.Header.Set("Content-Type", "text/turtle")

	// Send the request
	client = &http.Client{}
	resp, err = client.Do(req)
	if err != nil {
		fmt.Println("Error sending the request to the database:", err)
		return
	}
	defer resp.Body.Close()

	// Read and print the response
	body, err := io.ReadAll(resp.Body)
	fmt.Println("GraphDB Response Status:", resp.Status)
	if err != nil {
		fmt.Println("Error reading the response body:", err)
		fmt.Println("GraphDB Response Body:", string(body))
	}
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
