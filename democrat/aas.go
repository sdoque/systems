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

// ── AAS data types (AAS Part 2 v3 JSON serialization) ─────────────────────────

type AASEnv struct {
	AssetAdministrationShells []AAS                `json:"assetAdministrationShells"`
	Submodels                 []Submodel           `json:"submodels"`
	ConceptDescriptions       []ConceptDescription `json:"conceptDescriptions"`
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
	SemanticID       *Reference        `json:"semanticId,omitempty"`
	SubmodelElements []SubmodelElement `json:"submodelElements"`
}

type SubmodelElement struct {
	ModelType  string     `json:"modelType"`
	IDShort    string     `json:"idShort"`
	SemanticID *Reference `json:"semanticId,omitempty"`
	// ValueType is absent on a collection or a reference element, which hold
	// other elements rather than a typed literal.
	ValueType string `json:"valueType,omitempty"`
	Value     any    `json:"value,omitempty"`
}

// Reference points at the concept an element means, rather than at another
// element of this shell.
//
// An ExternalReference, because what it names lives outside the Asset
// Administration Shell entirely: a term in an ontology, with a definition
// somebody published and a consumer can look up. A ModelReference — the type
// already used above for "this shell has that submodel" — would say something
// quite different.
type Reference struct {
	Type string `json:"type"`
	Keys []Key  `json:"keys"`
}

// meaning builds the semantic identifier for one concept IRI.
func meaning(iri string) *Reference {
	if iri == "" {
		return nil
	}
	return &Reference{
		Type: "ExternalReference",
		Keys: []Key{{Type: "GlobalReference", Value: iri}},
	}
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
	URL         string // afo:hasUrl — the first, kept for the Services submodel

	// URLs is every address this service answers on, one per protocol the husk
	// opens. A system listening on both http and https states two, and an
	// interface description needs both: they are different endpoints with
	// different security, not two spellings of one.
	URLs []string

	// Unit and QuantityKind are the QUDT IRIs the service registered, which is
	// what turns a number into a measurement.
	Unit         string
	QuantityKind string

	// Methods is what the service answers, as W3C HTTP method IRIs. Empty means
	// the service never said, and a consumer assumes a read.
	Methods []string

	// Subscribable says a consumer may follow this value instead of asking for
	// it repeatedly — which is exactly what WoT calls observable.
	Subscribable bool

	// Form is the payload form the service registered, e.g. "SignalA_v1a",
	// which is how a number is told from a boolean.
	Form string
}

// ── What the elements mean ────────────────────────────────────────────────────

// The namespaces this bridge names concepts from.
//
// An Asset Administration Shell without semantic identifiers is a shape without
// a meaning. A consumer receiving a property called "ServiceUrl_temperature" can
// display the string and do nothing else with it: there is nothing to look up,
// nothing to compare with a property from another vendor's shell, and no way to
// tell a URL from a serial number except by reading the name and guessing. Every
// element below therefore carries a semanticId naming the concept its value came
// from.
//
// The identifiers are the ontology's own, because that is where these values
// actually came from — democrat reads a knowledge graph, and each property is a
// literal that was the object of one predicate. Pointing at that predicate is
// both true and useful. Inventing an IDTA submodel template identifier instead
// would claim conformance to a template this does not implement, which is worse
// than claiming nothing.
const (
	// afo is the Arrowhead Framework Ontology, published with a DOI, which is
	// what makes these identifiers worth dereferencing.
	afo = "http://www.synecdoque.com/2025/afo#"
	// alc is the local cloud's own namespace, for what this project mints.
	alc = "http://www.synecdoque.com/lcloud/"
)

// Submodel templates. No IDTA template describes an Arrowhead system, so these
// are the local cloud's own and are named as such. When a submodel here does
// come to implement an IDTA template — an Asset Interfaces Description built
// from these same endpoints is the obvious candidate — its identifier becomes
// the IDTA one and this comment should shrink.
const (
	smtIdentity = alc + "aas/IdentitySubmodel"
	smtHost     = alc + "aas/HostSubmodel"
	smtServices = alc + "aas/ServicesSubmodel"
)

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
	s = reBad.ReplaceAllString(s, "_")        // replace non-alnum runs with _
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
  OPTIONAL { ?host afo:hasIPAddress ?ip . }
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
	//
	// The service IRI is selected as well as its name, because a service that a
	// husk serves over both http and https states one afo:hasUrl per protocol
	// and so comes back as two rows. Grouping on the IRI collects them into one
	// service with two addresses; grouping on the name would work today and
	// break the day two unit assets in one system name a service alike.
	//
	// Unit, quantity kind, methods and subscribability are all optional: a
	// service that measures nothing has no unit, and one that only answers GET
	// says nothing about methods. Each is a separate OPTIONAL rather than one
	// block, so a service missing any one of them still returns the others.
	qSvc := sparqlPrefixes + `
SELECT ?system ?svc ?svcName ?svcDef ?url ?unit ?quantityKind ?method ?subscribable ?form
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasUnitAsset ?ua .
  ?ua afo:providesService ?svc .
  ?svc afo:hasName ?svcName ;
       afo:hasUrl ?url .
  OPTIONAL { ?svc afo:hasServiceDefinition ?svcDef . }
  OPTIONAL { ?svc alc:hasUnit ?unit . }
  OPTIONAL { ?svc alc:hasQuantityKind ?quantityKind . }
  OPTIONAL { ?svc alc:hasMethods ?method . }
  OPTIONAL { ?svc afo:isSubscribable ?subscribable . }
  OPTIONAL { ?svc alc:hasForms ?form . }
}
`
	r3, err := sparqlSelect(client, sparqlEndpoint, qSvc)
	if err != nil {
		return nil, fmt.Errorf("query services: %w", err)
	}
	// One row per combination of the optionals, so the same service arrives
	// several times over: two URLs and two methods make four rows saying the
	// same thing. Merging by IRI is what turns them back into one service.
	byIRI := map[string]map[string]*ServiceInfo{}
	for _, b := range r3.Results.Bindings {
		s, ok := systems[b["system"].Value]
		if !ok {
			continue
		}
		iri := b["svc"].Value
		if byIRI[s.SystemURI] == nil {
			byIRI[s.SystemURI] = map[string]*ServiceInfo{}
		}
		svc, seen := byIRI[s.SystemURI][iri]
		if !seen {
			svc = &ServiceInfo{ServiceName: b["svcName"].Value}
			byIRI[s.SystemURI][iri] = svc
			s.Services = append(s.Services, ServiceInfo{})
		}
		if d, ok := b["svcDef"]; ok {
			svc.ServiceDef = d.Value
		}
		if u, ok := b["url"]; ok {
			svc.URLs = appendOnce(svc.URLs, u.Value)
		}
		if u, ok := b["unit"]; ok {
			svc.Unit = u.Value
		}
		if q, ok := b["quantityKind"]; ok {
			svc.QuantityKind = q.Value
		}
		if m, ok := b["method"]; ok {
			svc.Methods = appendOnce(svc.Methods, m.Value)
		}
		if sub, ok := b["subscribable"]; ok {
			svc.Subscribable = sub.Value == "true"
		}
		if f, ok := b["form"]; ok {
			svc.Form = localName(f.Value)
		}
	}
	// Replace the placeholders with the merged services, in a stable order.
	for uri, svcs := range byIRI {
		s := systems[uri]
		iris := make([]string, 0, len(svcs))
		for iri := range svcs {
			iris = append(iris, iri)
		}
		sort.Strings(iris)
		s.Services = s.Services[:0]
		for _, iri := range iris {
			svc := svcs[iri]
			sort.Strings(svc.URLs)
			sort.Strings(svc.Methods)
			if len(svc.URLs) > 0 {
				svc.URL = svc.URLs[0]
			}
			s.Services = append(s.Services, *svc)
		}
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

// buildAASEnv converts a SystemInfo map into an AASEnv that can be serialized
// to JSON and uploaded to FA³ST.  Each Arrowhead system becomes one AAS with
// up to three submodels: Identity, Host (when host data is available), and
// Services.
func buildAASEnv(systems map[string]*SystemInfo) AASEnv {
	env := AASEnv{
		AssetAdministrationShells: []AAS{},
		Submodels:                 []Submodel{},
		ConceptDescriptions:       []ConceptDescription{},
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
		smInterfaces := "urn:alc:sm:" + idShort + ":AssetInterfacesDescription"

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
			ModelType:  "Submodel",
			ID:         smIdentity,
			IDShort:    "Identity",
			SemanticID: meaning(smtIdentity),
			SubmodelElements: []SubmodelElement{
				{ModelType: "Property", IDShort: "SystemName",
					SemanticID: meaning(afo + "hasName"),
					ValueType:  "xs:string", Value: s.SystemName},
				{ModelType: "Property", IDShort: "SystemUri",
					// The subject the graph knows this system by, which is what
					// lets a consumer follow it back into the graph rather than
					// treat this shell as the whole story.
					SemanticID: meaning(afo + "System"),
					ValueType:  "xs:anyURI", Value: s.SystemURI},
			},
		})

		// Host submodel — omitted when kgrapher has no host data.
		if hasHost {
			elems := []SubmodelElement{}
			if s.HostName != "" {
				elems = append(elems, SubmodelElement{
					ModelType: "Property", IDShort: "HostName",
					SemanticID: meaning(afo + "hasName"),
					ValueType:  "xs:string", Value: s.HostName,
				})
			}
			for i, ip := range s.IPs {
				if i >= 8 {
					break
				}
				elems = append(elems, SubmodelElement{
					ModelType: "Property", IDShort: fmt.Sprintf("IP_%d", i+1),
					SemanticID: meaning(afo + "hasIPAddress"),
					ValueType:  "xs:string", Value: ip,
				})
			}
			env.Submodels = append(env.Submodels, Submodel{
				ModelType:        "Submodel",
				ID:               smHost,
				IDShort:          "Host",
				SemanticID:       meaning(smtHost),
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
				SemanticID: meaning(afo + "hasUrl"),
				ValueType:  "xs:anyURI", Value: svc.URL,
			})
			// What the Asset Interfaces Description cannot say. AID 1.0 gives a
			// property one form and one method name, so a setpoint that answers
			// both GET and PUT states only the read there. Here there is room
			// for the whole list, and a shell that mentions the write in one
			// place is better than one that mentions it nowhere.
			if len(svc.Methods) > 1 {
				svcElems = append(svcElems, SubmodelElement{
					ModelType: "Property",
					IDShort:   "Methods_" + sanitizeIDShort(svc.ServiceName),
					// Local, because afo: does not define it yet; the framework
					// writes it as alc:hasMethods for the same reason.
					SemanticID: meaning(alc + "hasMethods"),
					ValueType:  "xs:string", Value: strings.Join(methodNames(svc.Methods), " "),
				})
			}
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
					// The shortcut is named for the service definition, so it
					// means the definition rather than merely an address.
					SemanticID: meaning(afo + "hasServiceDefinition"),
					ValueType:  "xs:anyURI", Value: urls[0],
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
			SemanticID:       meaning(smtServices),
			SubmodelElements: svcElems,
		})

		// Asset Interfaces Description — the one submodel here that implements
		// a published IDTA template rather than a local convention, and the one
		// a consumer outside this cloud has tooling for.
		if aid, ok := buildAID(s, smInterfaces); ok {
			env.Submodels = append(env.Submodels, aid)
			env.AssetAdministrationShells[len(env.AssetAdministrationShells)-1].Submodels = append(
				env.AssetAdministrationShells[len(env.AssetAdministrationShells)-1].Submodels,
				ModelReference{Type: "ModelReference",
					Keys: []Key{{Type: "Submodel", Value: smInterfaces}}},
			)
		}
	}

	// Last, because it reads what the loop above produced: every identifier the
	// shell now mentions and this bridge is entitled to explain.
	env.ConceptDescriptions = buildConceptDescriptions(env)

	return env
}

// ── FA³ST upsert ──────────────────────────────────────────────────────────────

// upsert creates or replaces one element in FA³ST.
//
// PUT first and POST on a 404, which is the only way to be sure without asking:
// a GET to find out whether the element exists would be a second round trip and
// would still be a guess by the time the write arrived.
//
// One function for shells, submodels and concept descriptions because the AAS
// API treats them alike — collection, base64url identifier, same status codes.
// Three copies of this existed in spirit; the third was the one that made it
// obvious.
func upsert(client *http.Client, faaastBase, collection, id string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s %s: %w", collection, id, err)
	}
	idB64 := b64url(id)

	req, _ := http.NewRequest(http.MethodPut, faaastBase+"/"+collection+"/"+idB64, bytes.NewReader(body))
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
		return fmt.Errorf("PUT /%s/%s: HTTP %d", collection, idB64, resp.StatusCode)
	}

	// It does not exist yet — create it.
	req2, _ := http.NewRequest(http.MethodPost, faaastBase+"/"+collection, bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		return err
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusCreated || resp2.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("POST /%s: HTTP %d", collection, resp2.StatusCode)
}

// upsertShell creates or replaces an Asset Administration Shell.
func upsertShell(client *http.Client, faaastBase string, aas AAS) error {
	return upsert(client, faaastBase, "shells", aas.ID, aas)
}

// upsertSubmodel creates or replaces a submodel.
func upsertSubmodel(client *http.Client, faaastBase string, sm Submodel) error {
	return upsert(client, faaastBase, "submodels", sm.ID, sm)
}

// upsertConcept creates or replaces a concept description.
//
// Written before the submodels that point at it, so a consumer that reads the
// shell between two syncs finds the meaning already there rather than a
// semanticId leading nowhere.
func upsertConcept(client *http.Client, faaastBase string, cd ConceptDescription) error {
	return upsert(client, faaastBase, "concept-descriptions", cd.ID, cd)
}

// ── Small helpers ─────────────────────────────────────────────────────────────

// appendOnce adds a value to a slice unless it is already there. The SPARQL
// result multiplies rows across every OPTIONAL, so the same URL arrives once per
// method and the same method once per URL.
func appendOnce(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// localName is the last segment of an IRI, and the value itself when it is not
// one. A detail whose value is a legal name reaches the graph as an entity in
// the local cloud's namespace — alc:SignalA_v1a — while one with a slash in it
// stays a literal, so both shapes come back from the same query.
func localName(value string) string {
	if i := strings.LastIndexAny(value, "#/"); i >= 0 && i+1 < len(value) {
		return value[i+1:]
	}
	return value
}

// methodNames turns W3C HTTP method IRIs back into the names a reader expects,
// for a value that is meant to be read by a person as well as a machine.
func methodNames(methods []string) []string {
	out := make([]string, 0, len(methods))
	for _, m := range methods {
		out = append(out, localName(m))
	}
	sort.Strings(out)
	return out
}
