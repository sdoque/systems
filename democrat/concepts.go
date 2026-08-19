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

// Concept descriptions: what the identifiers scattered through this shell mean,
// for a reader who cannot fetch them.
//
// A semanticId is a promise that somebody, somewhere, has written down what the
// identifier stands for. A concept description is that writing-down brought
// inside the environment, so a consumer with no internet — or no interest in
// dereferencing an IRI it has never seen — can still learn that a value is a
// temperature in degrees Celsius.
//
// The rule for what gets one is: describe what only we can describe.
//
//   - The local cloud's own identifiers get a description, because nothing else
//     in the world defines them. If democrat does not say what
//     alc:aas/ServicesSubmodel means, nobody can.
//   - QUDT units and quantity kinds get one, not to redefine QUDT — which is
//     authoritative and dereferences — but to carry the IEC 61360 translation
//     the Asset Administration Shell world actually reads: the symbol °C beside
//     a unitId pointing at the QUDT IRI. QUDT does not publish that pairing;
//     this bridge is the only place it can be made.
//   - AFO terms get none. The Arrowhead Framework Ontology is published with a
//     DOI and defines its own terms properly. Restating those definitions inside
//     every shell would be the same duplication democrat exists to remove, and
//     the copies would drift the first time the ontology was revised.
//   - IDTA and Web of Things identifiers get none either, for the same reason
//     and with less claim: they are not ours to define.
package main

import (
	"sort"
	"strings"

	"github.com/sdoque/mbaigo/usecases"
)

// iec61360 is the data specification template that gives a concept description
// its content. The Asset Administration Shell has one shape for saying "this is
// a real measure whose unit is °C", and this is it.
const iec61360 = "https://admin-shell.io/DataSpecificationTemplates/DataSpecificationIec61360/3/0"

// The QUDT namespaces, matched on rather than parsed, so a value that merely
// looks like an IRI is not mistaken for a unit.
const (
	qudtUnit = "http://qudt.org/vocab/unit/"
	qudtKind = "http://qudt.org/vocab/quantitykind/"
)

// ConceptDescription is one identifier explained.
type ConceptDescription struct {
	ModelType                  string                      `json:"modelType"`
	ID                         string                      `json:"id"`
	IDShort                    string                      `json:"idShort"`
	EmbeddedDataSpecifications []EmbeddedDataSpecification `json:"embeddedDataSpecifications,omitempty"`
}

// EmbeddedDataSpecification pairs a template with content shaped by it.
type EmbeddedDataSpecification struct {
	DataSpecification        Reference       `json:"dataSpecification"`
	DataSpecificationContent Iec61360Content `json:"dataSpecificationContent"`
}

// Iec61360Content is the subset of IEC 61360 this bridge can fill honestly.
// The specification allows a good deal more — value lists, level types, source
// of definition — and a field left empty says more than one filled with a guess.
type Iec61360Content struct {
	ModelType     string     `json:"modelType"`
	PreferredName []LangText `json:"preferredName"`
	ShortName     []LangText `json:"shortName,omitempty"`
	Unit          string     `json:"unit,omitempty"`
	UnitID        *Reference `json:"unitId,omitempty"`
	DataType      string     `json:"dataType,omitempty"`
	Definition    []LangText `json:"definition,omitempty"`
}

// LangText is a string that knows what language it is in.
type LangText struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}

// en tags a string as English, which is what every definition here is written
// in and what a consumer should be told rather than left to assume.
func en(text string) []LangText {
	return []LangText{{Language: "en", Text: text}}
}

// What the local cloud's own identifiers mean. These are the definitions, not
// copies of definitions: no ontology holds them, so this is where they live.
var localConcepts = map[string]string{
	smtIdentity: "The identity of an Arrowhead system: the name it registered under and " +
		"the URI the local cloud's knowledge graph knows it by.",
	smtHost: "The machine an Arrowhead system runs on, as its husk reports it: the host " +
		"name and every address it answers on.",
	smtServices: "The services an Arrowhead system offers, each as the address it is " +
		"reachable at, together with the methods it answers.",
	alc + "hasMethods": "The HTTP methods a service answers, as W3C HTTP method IRIs. " +
		"A service that says nothing answers a read.",
}

// buildConceptDescriptions explains every identifier in the environment that
// this bridge is entitled to explain.
//
// It walks what was built rather than being handed a list, so a submodel or
// property added later brings its concept along instead of leaving a semanticId
// pointing at nothing.
func buildConceptDescriptions(env AASEnv) []ConceptDescription {
	wanted := map[string]bool{}

	var visit func(el SubmodelElement)
	visit = func(el SubmodelElement) {
		if el.SemanticID != nil {
			wanted[el.SemanticID.Keys[0].Value] = true
		}
		// A reference element points at a concept — that is the whole of what it
		// does — so what it points at needs describing as much as a semanticId.
		if ref, ok := el.Value.(*Reference); ok && len(ref.Keys) > 0 {
			wanted[ref.Keys[0].Value] = true
		}
		if text, ok := el.Value.(string); ok && strings.HasPrefix(text, qudtUnit) {
			wanted[text] = true
		}
		if kids, ok := el.Value.([]SubmodelElement); ok {
			for _, kid := range kids {
				visit(kid)
			}
		}
	}
	for _, sm := range env.Submodels {
		if sm.SemanticID != nil {
			wanted[sm.SemanticID.Keys[0].Value] = true
		}
		for _, el := range sm.SubmodelElements {
			visit(el)
		}
	}

	iris := make([]string, 0, len(wanted))
	for iri := range wanted {
		iris = append(iris, iri)
	}
	sort.Strings(iris)

	out := []ConceptDescription{}
	for _, iri := range iris {
		if cd, ok := describe(iri); ok {
			out = append(out, cd)
		}
	}
	return out
}

// describe returns the concept description for one identifier, and whether this
// bridge is the right place for it to come from.
func describe(iri string) (ConceptDescription, bool) {
	switch {
	case strings.HasPrefix(iri, qudtUnit):
		return describeUnit(iri)
	case strings.HasPrefix(iri, qudtKind):
		return describeQuantityKind(iri), true
	case localConcepts[iri] != "":
		return ConceptDescription{
			ModelType: "ConceptDescription",
			ID:        iri,
			IDShort:   sanitizeIDShort(localName(iri)),
			EmbeddedDataSpecifications: []EmbeddedDataSpecification{{
				DataSpecification: *meaning(iec61360),
				DataSpecificationContent: Iec61360Content{
					ModelType:     "DataSpecificationIec61360",
					PreferredName: en(localName(iri)),
					DataType:      "STRING",
					Definition:    en(localConcepts[iri]),
				},
			}},
		}, true
	default:
		// An AFO, IDTA or Web of Things identifier: published by somebody who
		// defines it properly, and not ours to restate.
		return ConceptDescription{}, false
	}
}

// describeUnit carries a unit across from QUDT into IEC 61360.
//
// The framework already holds the symbol and the quantity kind for every unit
// its systems may register — it needs them to convert a reading — so this reads
// them rather than keeping a second table that could disagree with the one the
// conversions use. A unit the framework does not know is left undescribed:
// inventing a symbol for it would be worse than a consumer following the IRI.
func describeUnit(iri string) (ConceptDescription, bool) {
	def, ok := usecases.LookupUnit(iri)
	if !ok {
		return ConceptDescription{}, false
	}

	content := Iec61360Content{
		ModelType:     "DataSpecificationIec61360",
		PreferredName: en(localName(iri)),
		// REAL_MEASURE is what IEC 61360 calls a real number with a unit, which
		// is what every value carrying one of these is.
		DataType: "REAL_MEASURE",
		UnitID:   meaning(iri),
	}
	// A symbol only when there is one to give. A dimensionless count has none,
	// and an empty shortName would claim its symbol is the empty string.
	if def.Symbol != "" {
		content.ShortName = en(def.Symbol)
		content.Unit = def.Symbol
	}
	if def.QuantityKind != "" {
		content.Definition = en("The QUDT unit " + localName(iri) + ", which measures " +
			localName(def.QuantityKind) + ".")
	}

	return ConceptDescription{
		ModelType: "ConceptDescription",
		ID:        iri,
		IDShort:   sanitizeIDShort(localName(iri)),
		EmbeddedDataSpecifications: []EmbeddedDataSpecification{{
			DataSpecification:        *meaning(iec61360),
			DataSpecificationContent: content,
		}},
	}, true
}

// describeQuantityKind says what a value is, as opposed to what it is counted
// in. It carries no unit and no data type: a quantity kind is not a value, and
// filling those fields would be describing the wrong thing.
func describeQuantityKind(iri string) ConceptDescription {
	name := localName(iri)
	return ConceptDescription{
		ModelType: "ConceptDescription",
		ID:        iri,
		IDShort:   sanitizeIDShort(name),
		EmbeddedDataSpecifications: []EmbeddedDataSpecification{{
			DataSpecification: *meaning(iec61360),
			DataSpecificationContent: Iec61360Content{
				ModelType:     "DataSpecificationIec61360",
				PreferredName: en(name),
				Definition: en("The QUDT quantity kind " + name +
					": what the value is a measure of, independent of the unit it is given in."),
			},
		}},
	}
}
