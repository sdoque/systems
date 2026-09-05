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
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

// Reading the cloud out of the triple store.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The named graph kgrapher keeps current, and the prefixes every query needs.
const currentGraph = "urn:state:current"

const prefixes = `
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX xsd:  <http://www.w3.org/2001/XMLSchema#>
PREFIX alc:  <http://www.synecdoque.com/lcloud/>
PREFIX afo:  <https://w3id.org/synecdoque/afo#>
PREFIX owl:  <http://www.w3.org/2002/07/owl#>
PREFIX rdf:  <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
`

// sparqlResp is the SELECT result shape, which is a W3C recommendation rather
// than GraphDB's own, so this works against any store that speaks it.
type sparqlResp struct {
	Results struct {
		Bindings []map[string]struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"bindings"`
	} `json:"results"`
}

// ask runs one SELECT and returns its bindings.
func ask(client *http.Client, endpoint, query string) (*sparqlResp, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint,
		strings.NewReader("query="+urlEncode(query)))
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("SPARQL %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out sparqlResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding SPARQL results: %w", err)
	}
	return &out, nil
}

// urlEncode percent-encodes a SPARQL query for a form body.
func urlEncode(s string) string {
	repl := strings.NewReplacer(
		"%", "%25", "&", "%26", "+", "%2B", "=", "%3D", " ", "+",
		"#", "%23", "?", "%3F", "\n", "%0A", "\r", "", "/", "%2F",
		":", "%3A", ";", "%3B", "<", "%3C", ">", "%3E", "\"", "%22",
	)
	return repl.Replace(s)
}

// ReadCloud assembles the assessment scope from the triple store.
//
// Four queries rather than one join, because a single query with five OPTIONAL
// clauses returns the cross product of every optional against every other — the
// same row arriving once per combination — and reassembling that in Go costs
// more than asking four honest questions. Each is keyed on an IRI, so merging
// is a map lookup.
func ReadCloud(client *http.Client, endpoint string) (*Cloud, error) {
	cloud := &Cloud{Hosts: map[string][]string{}}

	assets, err := readAssets(client, endpoint)
	if err != nil {
		return nil, fmt.Errorf("reading unit assets: %w", err)
	}
	if err := readProvided(client, endpoint, assets); err != nil {
		return nil, fmt.Errorf("reading provided services: %w", err)
	}
	if err := readConsumed(client, endpoint, assets); err != nil {
		return nil, fmt.Errorf("reading consumed services: %w", err)
	}
	if err := readHosts(client, endpoint, cloud); err != nil {
		return nil, fmt.Errorf("reading hosts: %w", err)
	}

	// Placement, so a check can ask what a host would take with it. readHosts
	// answers by system, and an asset belongs to exactly one.
	hostOf := map[string]string{}
	for host, systems := range cloud.Hosts {
		for _, sys := range systems {
			hostOf[sys] = host
		}
	}
	for _, a := range assets {
		a.Host = hostOf[a.System]
		cloud.Assets = append(cloud.Assets, a)
	}
	sort.Slice(cloud.Assets, func(i, j int) bool { return cloud.Assets[i].IRI < cloud.Assets[j].IRI })
	cloud.resolve()
	return cloud, nil
}

// readAssets asks which unit assets exist, in which system, with what mission
// and where.
//
// The location comes back as either an IRI or a literal, and both are kept
// as-is rather than normalized: which of the two a system used is one of the
// things worth reporting, since a cloud where one system writes alc:Kitchen and
// another writes "Kitchen " has two rooms as far as any comparison is
// concerned.
func readAssets(client *http.Client, endpoint string) (map[string]*Asset, error) {
	q := prefixes + `
SELECT ?asset ?assetName ?system ?systemName ?mission ?location ?mobility ?tetheredTo
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasName ?systemName ;
          afo:hasUnitAsset ?asset .
  ?asset afo:hasName ?assetName .
  OPTIONAL { ?asset afo:hasMission ?mission . }
  OPTIONAL { ?asset afo:hasFunctionalLocation ?location . }
  OPTIONAL { ?asset alc:hasMobility ?mobility . }
  OPTIONAL { ?asset alc:hasTetheredTo ?tetheredTo . }
}
`
	r, err := ask(client, endpoint, q)
	if err != nil {
		return nil, err
	}
	assets := map[string]*Asset{}
	for _, b := range r.Results.Bindings {
		iri := b["asset"].Value
		a, seen := assets[iri]
		if !seen {
			a = &Asset{IRI: iri, Name: b["assetName"].Value, System: b["systemName"].Value}
			assets[iri] = a
		}
		if m, ok := b["mission"]; ok {
			a.Mission = m.Value
		}
		if l, ok := b["location"]; ok {
			a.Location = l.Value
			a.LocationIsIRI = l.Type == "uri"
		}
		if m, ok := b["mobility"]; ok {
			a.Mobility = localName(m.Value)
		}
		if tt, ok := b["tetheredTo"]; ok {
			a.TetheredTo = appendOnce(a.TetheredTo, localName(tt.Value))
		}
	}
	return assets, nil
}

// readProvided fills in what each asset offers.
func readProvided(client *http.Client, endpoint string, assets map[string]*Asset) error {
	q := prefixes + `
SELECT ?asset ?svc ?svcName ?definition ?subscribable ?unit ?quantityKind ?regPeriod ?url ?method ?range ?form
FROM <` + currentGraph + `>
WHERE {
  ?asset afo:providesService ?svc .
  ?svc afo:hasName ?svcName .
  OPTIONAL { ?svc afo:hasServiceDefinition ?definition . }
  OPTIONAL { ?svc afo:isSubscribable ?subscribable . }
  OPTIONAL { ?svc alc:hasUnit ?unit . }
  OPTIONAL { ?svc alc:hasQuantityKind ?quantityKind . }
  OPTIONAL { ?svc afo:hasRegistrationPeriod ?regPeriod . }
  OPTIONAL { ?svc afo:hasUrl ?url . }
  OPTIONAL { ?svc alc:hasMethods ?method . }
  OPTIONAL { ?svc alc:hasRange ?range . }
  OPTIONAL { ?svc alc:hasForms ?form . }
}
`
	r, err := ask(client, endpoint, q)
	if err != nil {
		return err
	}
	services := map[string]*Service{}
	for _, b := range r.Results.Bindings {
		a, known := assets[b["asset"].Value]
		if !known {
			continue
		}
		iri := b["svc"].Value
		s, seen := services[iri]
		if !seen {
			s = &Service{IRI: iri, Name: b["svcName"].Value}
			services[iri] = s
			a.Provides = append(a.Provides, s)
		}
		if v, ok := b["definition"]; ok {
			s.Definition = v.Value
		}
		if v, ok := b["subscribable"]; ok {
			s.Subscribable = v.Value == "true"
		}
		if v, ok := b["unit"]; ok {
			s.Unit = v.Value
		}
		if v, ok := b["quantityKind"]; ok {
			s.QuantityKind = v.Value
		}
		if v, ok := b["regPeriod"]; ok {
			s.RegPeriod, _ = strconv.Atoi(v.Value)
		}
		if v, ok := b["url"]; ok {
			s.URLs = appendOnce(s.URLs, v.Value)
		}
		if v, ok := b["method"]; ok {
			s.Methods = appendOnce(s.Methods, v.Value)
		}
		if v, ok := b["range"]; ok {
			s.Range = appendOnce(s.Range, v.Value)
		}
		if v, ok := b["form"]; ok {
			s.Forms = appendOnce(s.Forms, localName(v.Value))
		}
	}
	for _, a := range assets {
		sort.Slice(a.Provides, func(i, j int) bool { return a.Provides[i].IRI < a.Provides[j].IRI })
	}
	return nil
}

// readConsumed fills in what each asset depends on.
func readConsumed(client *http.Client, endpoint string, assets map[string]*Asset) error {
	q := prefixes + `
SELECT ?asset ?cervice ?definition ?mode ?fromUrl ?target
FROM <` + currentGraph + `>
WHERE {
  ?asset afo:consumesService ?cervice .
  OPTIONAL { ?cervice afo:consumes ?definition . FILTER(isLiteral(?definition)) }
  OPTIONAL { ?cervice afo:consumes ?target . FILTER(isIRI(?target)) }
  OPTIONAL { ?cervice alc:hasMode ?mode . }
  OPTIONAL { ?cervice alc:fromUrl ?fromUrl . }
}
`
	r, err := ask(client, endpoint, q)
	if err != nil {
		return err
	}
	consumptions := map[string]*Consumption{}
	for _, b := range r.Results.Bindings {
		a, known := assets[b["asset"].Value]
		if !known {
			continue
		}
		iri := b["cervice"].Value
		c, seen := consumptions[iri]
		if !seen {
			c = &Consumption{IRI: iri}
			consumptions[iri] = c
			a.Consumes = append(a.Consumes, c)
		}
		if v, ok := b["definition"]; ok {
			c.Definition = v.Value
		}
		if v, ok := b["target"]; ok {
			// The consumption names the provided service directly, which is what
			// makes an effect traceable rather than guessed.
			c.IRI = v.Value
		}
		if v, ok := b["mode"]; ok {
			c.Mode = v.Value
		}
		if v, ok := b["fromUrl"]; ok {
			c.FromURL = v.Value
		}
	}
	for _, a := range assets {
		sort.Slice(a.Consumes, func(i, j int) bool { return a.Consumes[i].Definition < a.Consumes[j].Definition })
	}
	return nil
}

// readHosts asks which machine each system runs on.
//
// One question because the answer is often one machine, and a cloud whose every
// husk names the same host has a single point of failure that no amount of
// service redundancy addresses.
func readHosts(client *http.Client, endpoint string, cloud *Cloud) error {
	q := prefixes + `
SELECT ?systemName ?hostName ?cloudName
FROM <` + currentGraph + `>
WHERE {
  ?system a afo:System ;
          afo:hasName ?systemName ;
          afo:hasHusk ?husk .
  ?husk afo:runsOnHost ?host .
  ?host afo:hasName ?hostName .
  OPTIONAL { ?system afo:isContainedIn ?cloudName . }
}
`
	r, err := ask(client, endpoint, q)
	if err != nil {
		return err
	}
	for _, b := range r.Results.Bindings {
		host := b["hostName"].Value
		cloud.Hosts[host] = appendOnce(cloud.Hosts[host], b["systemName"].Value)
		if c, ok := b["cloudName"]; ok && cloud.Name == "" {
			cloud.Name = localName(c.Value)
		}
	}
	if cloud.Name == "" {
		cloud.Name = "local cloud"
	}
	return nil
}

// appendOnce adds a value unless it is already present. Every query above
// multiplies its rows across the optionals, so the same URL arrives once per
// method and the same method once per URL.
func appendOnce(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}
