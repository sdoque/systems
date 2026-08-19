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

// The Asset Interfaces Description: how to talk to a system, in the vocabulary
// the rest of the world uses for that.
//
// The other three submodels this bridge writes are the local cloud's own, and
// say so. This one is not: IDTA 02017-1-0 is a published template built on the
// W3C Web of Things Thing Description, and an Arrowhead service registration
// happens to hold nearly everything it asks for. That is worth taking seriously
// rather than paraphrasing — a consumer that has never heard of Arrowhead can
// read this submodel with tooling it already has.
//
// Three of the mappings are exact rather than approximate:
//
//	observable      ← afo:isSubscribable   a value you may follow, not poll
//	unit            ← the QUDT unit IRI    schema.org/unitCode admits a URL
//	valueSemantics  ← the QUDT quantity kind, which is what that slot is for
//
// so this is the first submodel here entitled to an admin-shell.io semanticId.
// It carries them because it implements the template, not because they look
// more official than a local identifier.
package main

import (
	"net/url"
	"sort"
	"strings"
)

// The identifiers IDTA 02017-1-0 defines, copied from the published template
// rather than reconstructed, because they are what a consumer matches on.
//
// aidPropertyDefinition really does say "AssetInterfaceDescription" while every
// other identifier says "AssetInterfacesDescription". That inconsistency is in
// the published template; correcting it here would mint an identifier nobody
// else uses, which is the opposite of the point.
const (
	aidSubmodel            = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/Submodel"
	aidInterface           = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/Interface"
	aidEndpointMetadata    = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/EndpointMetadata"
	aidInteractionMetadata = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/InteractionMetadata"
	aidPropertyDefinition  = "https://admin-shell.io/idta/AssetInterfaceDescription/1/0/PropertyDefinition"
	aidKey                 = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/key"
	aidValueSemantics      = "https://admin-shell.io/idta/AssetInterfacesDescription/1/0/valueSemantics"
)

// The Web of Things vocabulary the template borrows, which is where the
// interesting terms actually live.
const (
	wotTitle                 = "https://www.w3.org/2019/wot/td#title"
	wotBaseURI               = "https://www.w3.org/2019/wot/td#baseURI"
	wotHasForm               = "https://www.w3.org/2019/wot/td#hasForm"
	wotPropertyAffordance    = "https://www.w3.org/2019/wot/td#PropertyAffordance"
	wotIsObservable          = "https://www.w3.org/2019/wot/td#isObservable"
	wotDefinesSecurityScheme = "https://www.w3.org/2019/wot/td#definesSecurityScheme"
	wotNoSecurityScheme      = "https://www.w3.org/2019/wot/security#NoSecurityScheme"
	wotAutoSecurityScheme    = "https://www.w3.org/2019/wot/security#AutoSecurityScheme"
	wotForContentType        = "https://www.w3.org/2019/wot/hypermedia#forContentType"
	wotHasTarget             = "https://www.w3.org/2019/wot/hypermedia#hasTarget"
	rdfType                  = "https://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	schemaUnitCode           = "https://schema.org/unitCode"
	htvMethodName            = "https://www.w3.org/2011/http#methodName"
)

// buildAID returns the Asset Interfaces Description for one system, and whether
// there was anything to describe.
//
// One interface per protocol the husk opens, because http and https are not two
// spellings of one endpoint: they have different addresses and, more to the
// point, different security. A system that offers both gets two interfaces, and
// a consumer picks the one it can use.
func buildAID(s *SystemInfo, submodelID string) (Submodel, bool) {
	byScheme := map[string][]ServiceInfo{}
	baseOf := map[string]string{}

	for _, svc := range s.Services {
		for _, raw := range addressesOf(svc) {
			parsed, err := url.Parse(raw)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				continue
			}
			base := parsed.Scheme + "://" + parsed.Host + "/" + s.SystemName + "/"
			byScheme[parsed.Scheme] = append(byScheme[parsed.Scheme], svc)
			baseOf[parsed.Scheme] = base
		}
	}
	if len(byScheme) == 0 {
		return Submodel{}, false
	}

	schemes := make([]string, 0, len(byScheme))
	for scheme := range byScheme {
		schemes = append(schemes, scheme)
	}
	sort.Strings(schemes)

	interfaces := make([]SubmodelElement, 0, len(schemes))
	for _, scheme := range schemes {
		interfaces = append(interfaces, oneInterface(s, scheme, baseOf[scheme], byScheme[scheme]))
	}

	return Submodel{
		ModelType:        "Submodel",
		ID:               submodelID,
		IDShort:          "AssetInterfacesDescription",
		SemanticID:       meaning(aidSubmodel),
		SubmodelElements: interfaces,
	}, true
}

// oneInterface describes how to reach this system over one protocol.
func oneInterface(s *SystemInfo, scheme, base string, services []ServiceInfo) SubmodelElement {
	return SubmodelElement{
		ModelType:  "SubmodelElementCollection",
		IDShort:    "Interface" + strings.ToUpper(scheme),
		SemanticID: meaning(aidInterface),
		Value: []SubmodelElement{
			{ModelType: "Property", IDShort: "title",
				SemanticID: meaning(wotTitle),
				ValueType:  "xs:string", Value: s.SystemName},
			endpointMetadata(scheme, base),
			interactionMetadata(base, services),
		},
	}
}

// endpointMetadata says where the system is and how a consumer gets in.
//
// The security scheme is the one place where the Web of Things vocabulary has
// no name for what mbaigo actually does. Its schemes are nosec, basic, digest,
// bearer, psk, apikey, oauth2, combo and auto; none of them is "mutual TLS with
// a cloud certificate authority, plus a per-action token from the authorizer".
//
// So: plain http says nosec_sc, which is true — that port is unauthenticated.
// https says auto_sc, the Web of Things term for security arranged out of band,
// which is honest about being underspecified. Claiming bearer_sc would describe
// the token and quietly drop the certificate, and the certificate is the half
// that decides whether a connection happens at all.
func endpointMetadata(scheme, base string) SubmodelElement {
	schemeName, schemeMeaning := "nosec_sc", wotNoSecurityScheme
	if scheme != "http" {
		schemeName, schemeMeaning = "auto_sc", wotAutoSecurityScheme
	}

	return SubmodelElement{
		ModelType:  "SubmodelElementCollection",
		IDShort:    "EndpointMetadata",
		SemanticID: meaning(aidEndpointMetadata),
		Value: []SubmodelElement{
			{ModelType: "Property", IDShort: "base",
				SemanticID: meaning(wotBaseURI),
				ValueType:  "xs:anyURI", Value: base},
			{ModelType: "Property", IDShort: "contentType",
				SemanticID: meaning(wotForContentType),
				ValueType:  "xs:string", Value: "application/json"},
			{ModelType: "SubmodelElementCollection",
				IDShort:    "securityDefinitions",
				SemanticID: meaning(wotDefinesSecurityScheme),
				Value: []SubmodelElement{{
					ModelType:  "SubmodelElementCollection",
					IDShort:    schemeName,
					SemanticID: meaning(schemeMeaning),
					Value:      []SubmodelElement{},
				}},
			},
		},
	}
}

// interactionMetadata turns the system's services into property affordances.
func interactionMetadata(base string, services []ServiceInfo) SubmodelElement {
	seen := map[string]bool{}
	props := []SubmodelElement{}
	for _, svc := range services {
		idShort := sanitizeIDShort(svc.ServiceName)
		if seen[idShort] {
			continue
		}
		seen[idShort] = true
		props = append(props, propertyDefinition(base, svc, idShort))
	}
	sort.Slice(props, func(i, j int) bool { return props[i].IDShort < props[j].IDShort })

	return SubmodelElement{
		ModelType:  "SubmodelElementCollection",
		IDShort:    "InteractionMetadata",
		SemanticID: meaning(aidInteractionMetadata),
		Value: []SubmodelElement{{
			ModelType:  "SubmodelElementCollection",
			IDShort:    "properties",
			SemanticID: meaning(wotPropertyAffordance),
			Value:      props,
		}},
	}
}

// propertyDefinition describes one service as something a consumer can read.
func propertyDefinition(base string, svc ServiceInfo, idShort string) SubmodelElement {
	key := svc.ServiceDef
	if key == "" {
		key = svc.ServiceName
	}

	elems := []SubmodelElement{
		{ModelType: "Property", IDShort: "key",
			SemanticID: meaning(aidKey),
			ValueType:  "xs:string", Value: key},
		{ModelType: "Property", IDShort: "type",
			SemanticID: meaning(rdfType),
			ValueType:  "xs:string", Value: jsonTypeOf(svc)},
		{ModelType: "Property", IDShort: "title",
			SemanticID: meaning(wotTitle),
			ValueType:  "xs:string", Value: svc.ServiceName},
		// The framework's own word for it. A subscribable service is one a
		// consumer may follow instead of asking again, which is what the Web of
		// Things means by observable — the same idea, arrived at separately.
		{ModelType: "Property", IDShort: "observable",
			SemanticID: meaning(wotIsObservable),
			ValueType:  "xs:boolean", Value: svc.Subscribable},
	}

	// schema.org/unitCode takes a UN/CEFACT code or a URL, so the QUDT IRI goes
	// in as it stands rather than being translated into a three-letter code
	// that would lose the conversion factors behind it.
	if svc.Unit != "" {
		elems = append(elems, SubmodelElement{
			ModelType: "Property", IDShort: "unit",
			SemanticID: meaning(schemaUnitCode),
			ValueType:  "xs:string", Value: svc.Unit,
		})
	}
	// valueSemantics is a reference element because what it points at is a
	// concept elsewhere — here the QUDT quantity kind, which says a number is a
	// temperature rather than merely a number with °C after it.
	if svc.QuantityKind != "" {
		elems = append(elems, SubmodelElement{
			ModelType: "ReferenceElement", IDShort: "valueSemantics",
			SemanticID: meaning(aidValueSemantics),
			Value:      externalReference(svc.QuantityKind),
		})
	}

	elems = append(elems, forms(base, svc))

	return SubmodelElement{
		ModelType:  "SubmodelElementCollection",
		IDShort:    idShort,
		SemanticID: meaning(aidPropertyDefinition),
		Value:      elems,
	}
}

// forms says how to reach this one service.
//
// AID 1.0 gives a property exactly one form and that form one htv_methodName,
// so a service answering both GET and PUT cannot state both here. This emits
// the method a consumer reads with, or the sole method when the service does
// not read at all — beehive's toggle is a PUT and the certificate authority's
// certify is a POST, and calling either of those a GET would be worse than
// saying nothing.
//
// What the template cannot hold is not thrown away: the Services submodel
// carries the complete method list against alc:hasMethods. A consumer reading
// only the AID sees how to read the value, and one reading the whole shell sees
// that the setpoint can also be written.
func forms(base string, svc ServiceInfo) SubmodelElement {
	elems := []SubmodelElement{
		{ModelType: "Property", IDShort: "href",
			SemanticID: meaning(wotHasTarget),
			ValueType:  "xs:string", Value: hrefFor(base, svc)},
		{ModelType: "Property", IDShort: "contentType",
			SemanticID: meaning(wotForContentType),
			ValueType:  "xs:string", Value: "application/json"},
	}
	if method := readMethod(svc.Methods); method != "" {
		elems = append(elems, SubmodelElement{
			ModelType: "Property", IDShort: "htv_methodName",
			SemanticID: meaning(htvMethodName),
			ValueType:  "xs:string", Value: method,
		})
	}

	return SubmodelElement{
		ModelType:  "SubmodelElementCollection",
		IDShort:    "forms",
		SemanticID: meaning(wotHasForm),
		Value:      elems,
	}
}

// readMethod picks the method a consumer would use to read a value: GET when
// the service offers it, the only method when it offers one, and nothing when
// the service never said — where a consumer assumes GET anyway, so stating it
// would add a claim rather than a fact.
func readMethod(methods []string) string {
	names := make([]string, 0, len(methods))
	for _, m := range methods {
		names = append(names, localName(m))
	}
	for _, name := range names {
		if name == "GET" {
			return "GET"
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// addressesOf is every address a service answers on. A service that came from
// the graph has them all; one built by hand, or from a registration that stated
// a single URL, has the one.
func addressesOf(svc ServiceInfo) []string {
	if len(svc.URLs) > 0 {
		return svc.URLs
	}
	if svc.URL != "" {
		return []string{svc.URL}
	}
	return nil
}

// hrefFor returns the service address relative to the interface's base, which
// is what a form holds: the base is stated once for the whole interface, and
// repeating it per property would be a second place for the host to be wrong.
func hrefFor(base string, svc ServiceInfo) string {
	for _, raw := range addressesOf(svc) {
		if strings.HasPrefix(raw, base) {
			return strings.TrimPrefix(raw, base)
		}
	}
	// No URL under this base: fall back to the absolute address, which a form
	// is allowed to hold and which is at least reachable.
	if svc.URL != "" {
		return svc.URL
	}
	return ""
}

// jsonTypeOf reports the JSON type of a service's value, from the payload form
// it registered. SignalB carries a boolean and SignalA a number; anything else
// is described as a string, which is what an unparsed body is.
func jsonTypeOf(svc ServiceInfo) string {
	switch {
	case strings.HasPrefix(svc.Form, "SignalB"):
		return "boolean"
	case strings.HasPrefix(svc.Form, "SignalA"):
		return "number"
	case svc.Unit != "" || svc.QuantityKind != "":
		// It measures something, so it is a number even if the form did not
		// reach the graph.
		return "number"
	default:
		return "string"
	}
}

// externalReference is the value of a reference element pointing outside the
// shell, which is the same shape meaning() builds for a semanticId.
func externalReference(iri string) *Reference {
	return meaning(iri)
}
