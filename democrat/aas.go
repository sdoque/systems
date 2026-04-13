/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

// aas.go contains pure functions for:
//
//   - AAS and Submodel data types (Asset Administration Shell Part 2, v3)
//   - SPARQL query types and HTTP helper
//   - SystemInfo domain model extracted from the knowledge graph
//   - loadSystems — queries GraphDB and returns a map of SystemInfo
//   - buildAASEnv  — converts SystemInfo map to an AASEnv (generate-only mode)
//   - upsertShell / upsertSubmodel — push one AAS or Submodel to FA³ST
//
// No build constraints — this file compiles on all platforms so the
// pure-function tests (buildAASEnv, sanitizeIDShort, etc.) run without
// a running GraphDB or FA³ST instance.

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// ── AAS data types (AAS Part 2 v3 JSON serialisation) ─────────────────────────

type AASEnv struct {
	AssetAdministrationShells []AAS         `json:"assetAdministrationShells"`
	Submodels                 []Submodel    `json:"submodels"`
	ConceptDescriptions       []interface{} `json:"conceptDescriptions"`
}

type AAS struct {
	ModelType        string           `json:"modelType"`
	ID               string           `json:"id"`
	IDShort          string           `json:"idShort"`
	AssetInformation AssetInformation `json:"assetInformation"`
	Submodels        []ModelReference `json:"submodels"`
}

type AssetInformation struct {
	AssetKind     string `json:"assetKind"`
	GlobalAssetID string `json:"globalAssetId"`
}

type ModelReference struct {
	Type string `json:"type"`
	Keys []Key  `json:"keys"`
}

type Key struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Submodel struct {
	ModelType        string            `json:"modelType"`
	ID               string            `json:"id"`
	IDShort          string            `json:"idShort"`
	SubmodelElements []SubmodelElement `json:"submodelElements"`
}

type SubmodelElement struct {
	ModelType string `json:"modelType"`
	IDShort   string `json:"idShort"`
	ValueType string `json:"valueType"`
	Value     any    `json:"value,omitempty"`
}

// ── SPARQL types ──────────────────────────────────────────────────────────────

type sparqlResp struct {
	Results struct {
		Bindings []map[string]sparqlTerm `json:"bindings"`
	} `json:"results"`
}

type sparqlTerm struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ── Domain model ──────────────────────────────────────────────────────────────

// SystemInfo holds the data extracted from the knowledge graph for one
// Arrowhead system.
type SystemInfo struct {
	SystemURI  string
	SystemName string
	HostName   string
	IPs        []string
	Services   []ServiceInfo
}

// ServiceInfo describes one registered service endpoint.
type ServiceInfo struct {
	ServiceName string // afo:hasName
	ServiceDef  string // afo:hasServiceDefinition (optional)
	URL         string // afo:hasUrl
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// b64url encodes s as base64url without padding — required for FA³ST path segments.
func b64url(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

var (
	reBad        = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	reMultiUnder = regexp.MustCompile(`_+`)
)

// sanitizeIDShort converts an arbitrary string to a valid AAS idShort
// (letters, digits, underscores; must start with a letter).
func sanitizeIDShort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "S_unnamed"
	}
	s = reBad.ReplaceAllString(s, "_")     // replace non-alnum runs with _
	s = reMultiUnder.ReplaceAllString(s, "_") // collapse consecutive underscores
	s = strings.Trim(s, "_")
	if s == "" {
		return "S_unnamed"
	}
	r := rune(s[0])
	if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
		s = "S_" + s
	}
	return s
}

// titleCaseURL converts a service definition string to a URL property idShort.
// e.g. "temperature" → "TemperatureUrl", "windSpeed" → "WindSpeedUrl".
func titleCaseURL(def string) string {
	if def == "" {
		return ""
	}
	return strings.ToUpper(def[:1]) + def[1:] + "Url"
}

// ── SPARQL helpers ────────────────────────────────────────────────────────────

const sparqlPrefixes = `
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX xsd:  <http://www.w3.org/2001/XMLSchema#>
PREFIX alc:  <http://www.synecdoque.com/lcloud/>
PREFIX afo:  <http://www.synecdoque.com/2025/afo#>
PREFIX owl:  <http://www.w3.org/2002/07/owl#>
PREFIX rdf:  <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
`

// urlEncodeForm performs minimal percent-encoding suitable for SPARQL query POST bodies.
func urlEncodeForm(s string) string {
	repl := strings.NewReplacer(
		"%", "%25",
		" ", "%20",
		"\n", "%0A",
		"\r", "%0D",
		"+", "%2B",
		"&", "%26",
		"=", "%3D",
		"#", "%23",
	)
	return repl.Replace(s)
}

// sparqlSelect sends a SPARQL SELECT query via HTTP POST and returns the parsed result.
func sparqlSelect(client *http.Client, endpoint, query string) (*sparqlResp, error) {
	form := "query=" + urlEncodeForm(query)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("SPARQL endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var out sparqlResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("SPARQL JSON decode: %w", err)
	}
	return &out, nil
}

// ── KG extraction ─────────────────────────────────────────────────────────────

// loadSystems queries GraphDB using three SPARQL SELECT queries and returns a
// map from system URI to SystemInfo.  The named graph urn:state:current is
// where kgrapher stores the latest snapshot of the local cloud.
func loadSystems(client *http.Client, sparqlEndpoint string) (map[string]*SystemInfo, error) {
	const currentGraph = "urn:state:current"

	// 1 — systems and their names
	qSystems := sparqlPrefixes + `
SELECT ?system ?name
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasName ?name .
}
`
	r1, err := sparqlSelect(client, sparqlEndpoint, qSystems)
	if err != nil {
		return nil, fmt.Errorf("query systems: %w", err)
	}

	systems := map[string]*SystemInfo{}
	for _, b := range r1.Results.Bindings {
		systems[b["system"].Value] = &SystemInfo{
			SystemURI:  b["system"].Value,
			SystemName: b["name"].Value,
		}
	}

	// 2 — host name and IP addresses (via husk → host)
	qHost := sparqlPrefixes + `
SELECT ?system ?hostName ?ip
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasHusk ?husk .
  ?husk afo:runsOnHost ?host .
  ?host afo:hasName ?hostName .
  OPTIONAL { ?host afo:hasIPaddress ?ip . }
}
`
	r2, err := sparqlSelect(client, sparqlEndpoint, qHost)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	for _, b := range r2.Results.Bindings {
		s, ok := systems[b["system"].Value]
		if !ok {
			continue
		}
		s.HostName = b["hostName"].Value
		if ip, ok := b["ip"]; ok {
			s.IPs = append(s.IPs, ip.Value)
		}
	}

	// 3 — services (system → unitAsset → providesService → service)
	qSvc := sparqlPrefixes + `
SELECT ?system ?svcName ?svcDef ?url
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasUnitAsset ?ua .
  ?ua afo:providesService ?svc .
  ?svc afo:hasName ?svcName ;
       afo:hasUrl ?url .
  OPTIONAL { ?svc afo:hasServiceDefinition ?svcDef . }
}
`
	r3, err := sparqlSelect(client, sparqlEndpoint, qSvc)
	if err != nil {
		return nil, fmt.Errorf("query services: %w", err)
	}
	for _, b := range r3.Results.Bindings {
		s, ok := systems[b["system"].Value]
		if !ok {
			continue
		}
		svcDef := ""
		if d, ok := b["svcDef"]; ok {
			svcDef = d.Value
		}
		s.Services = append(s.Services, ServiceInfo{
			ServiceName: b["svcName"].Value,
			ServiceDef:  svcDef,
			URL:         b["url"].Value,
		})
	}

	// Deduplicate and sort IP addresses for stable output.
	for _, s := range systems {
		seen := map[string]bool{}
		var uniq []string
		for _, ip := range s.IPs {
			if !seen[ip] {
				seen[ip] = true
				uniq = append(uniq, ip)
			}
		}
		sort.Strings(uniq)
		s.IPs = uniq
	}

	return systems, nil
}

// ── AAS model generation ──────────────────────────────────────────────────────

// buildAASEnv converts a SystemInfo map into an AASEnv that can be serialised
// to JSON and uploaded to FA³ST.  Each Arrowhead system becomes one AAS with
// up to three submodels: Identity, Host (when host data is available), and
// Services.
func buildAASEnv(systems map[string]*SystemInfo) AASEnv {
	env := AASEnv{
		AssetAdministrationShells: []AAS{},
		Submodels:                 []Submodel{},
		ConceptDescriptions:       []interface{}{},
	}

	// Stable iteration order so output diffs are minimal.
	keys := make([]string, 0, len(systems))
	for k := range systems {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, sysURI := range keys {
		s := systems[sysURI]
		idShort := sanitizeIDShort(s.SystemName)

		aasID := "urn:alc:aas:" + idShort
		smIdentity := "urn:alc:sm:" + idShort + ":Identity"
		smHost := "urn:alc:sm:" + idShort + ":Host"
		smServices := "urn:alc:sm:" + idShort + ":Services"

		// Build submodel references for the AAS header.
		refs := []ModelReference{
			{Type: "ModelReference", Keys: []Key{{Type: "Submodel", Value: smIdentity}}},
			{Type: "ModelReference", Keys: []Key{{Type: "Submodel", Value: smServices}}},
		}
		hasHost := s.HostName != "" || len(s.IPs) > 0
		if hasHost {
			refs = append(refs, ModelReference{
				Type: "ModelReference",
				Keys: []Key{{Type: "Submodel", Value: smHost}},
			})
		}

		env.AssetAdministrationShells = append(env.AssetAdministrationShells, AAS{
			ModelType: "AssetAdministrationShell",
			ID:        aasID,
			IDShort:   idShort,
			AssetInformation: AssetInformation{
				AssetKind:     "Instance",
				GlobalAssetID: s.SystemURI,
			},
			Submodels: refs,
		})

		// Identity submodel — static; does not need periodic patching.
		env.Submodels = append(env.Submodels, Submodel{
			ModelType: "Submodel",
			ID:        smIdentity,
			IDShort:   "Identity",
			SubmodelElements: []SubmodelElement{
				{ModelType: "Property", IDShort: "SystemName", ValueType: "xs:string", Value: s.SystemName},
				{ModelType: "Property", IDShort: "SystemUri", ValueType: "xs:string", Value: s.SystemURI},
			},
		})

		// Host submodel — omitted when kgrapher has no host data.
		if hasHost {
			elems := []SubmodelElement{}
			if s.HostName != "" {
				elems = append(elems, SubmodelElement{
					ModelType: "Property", IDShort: "HostName",
					ValueType: "xs:string", Value: s.HostName,
				})
			}
			for i, ip := range s.IPs {
				if i >= 8 {
					break
				}
				elems = append(elems, SubmodelElement{
					ModelType: "Property", IDShort: fmt.Sprintf("IP_%d", i+1),
					ValueType: "xs:string", Value: ip,
				})
			}
			env.Submodels = append(env.Submodels, Submodel{
				ModelType:        "Submodel",
				ID:               smHost,
				IDShort:          "Host",
				SubmodelElements: elems,
			})
		}

		// Services submodel — one property per service name URL, plus a
		// shortcut property per unique service definition.
		svcElems := []SubmodelElement{}
		for _, svc := range s.Services {
			prop := "ServiceUrl_" + sanitizeIDShort(svc.ServiceName)
			svcElems = append(svcElems, SubmodelElement{
				ModelType: "Property", IDShort: prop,
				ValueType: "xs:anyURI", Value: svc.URL,
			})
		}
		// Definition shortcuts (only when a definition maps to exactly one URL).
		defMap := map[string][]string{}
		for _, svc := range s.Services {
			if svc.ServiceDef != "" {
				defMap[svc.ServiceDef] = append(defMap[svc.ServiceDef], svc.URL)
			}
		}
		for def, urls := range defMap {
			if len(urls) == 1 {
				svcElems = append(svcElems, SubmodelElement{
					ModelType: "Property", IDShort: titleCaseURL(def),
					ValueType: "xs:anyURI", Value: urls[0],
				})
			}
		}
		sort.Slice(svcElems, func(i, j int) bool {
			return svcElems[i].IDShort < svcElems[j].IDShort
		})
		env.Submodels = append(env.Submodels, Submodel{
			ModelType:        "Submodel",
			ID:               smServices,
			IDShort:          "Services",
			SubmodelElements: svcElems,
		})
	}

	return env
}

// ── FA³ST upsert ──────────────────────────────────────────────────────────────

// upsertShell creates or replaces an AAS in FA³ST.
// It tries PUT (update) first; if FA³ST returns 404 it falls back to POST (create).
func upsertShell(client *http.Client, faaastBase string, aas AAS) error {
	body, _ := json.Marshal(aas)
	idB64 := b64url(aas.ID)
	putURL := faaastBase + "/shells/" + idB64

	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("PUT /shells/%s: HTTP %d", idB64, resp.StatusCode)
	}

	// Shell does not exist yet — create it.
	req2, _ := http.NewRequest(http.MethodPost, faaastBase+"/shells", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		return err
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusCreated || resp2.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("POST /shells: HTTP %d", resp2.StatusCode)
}

// upsertSubmodel creates or replaces a Submodel in FA³ST.
func upsertSubmodel(client *http.Client, faaastBase string, sm Submodel) error {
	body, _ := json.Marshal(sm)
	idB64 := b64url(sm.ID)
	putURL := faaastBase + "/submodels/" + idB64

	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("PUT /submodels/%s: HTTP %d", idB64, resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, faaastBase+"/submodels", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		return err
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusCreated || resp2.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("POST /submodels: HTTP %d", resp2.StatusCode)
}
