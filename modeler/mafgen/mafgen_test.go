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
 ***************************************************************************SDG*/

package mafgen

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func afoSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "kgrapher", "ontologies", "afo.ttl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The reader finds every term the ontology declares — the same set a plain
// search for ":Name rdf:type" at the start of a line finds, so a construct it
// misreads shows up as a difference.
func TestTheReaderFindsEveryDeclaration(t *testing.T) {
	src := afoSource(t)
	terms, err := ReadAFO(src)
	if err != nil {
		t.Fatal(err)
	}
	got := Declared(terms)
	want := make(map[string]bool)
	for _, m := range regexp.MustCompile(`(?m)^:([A-Za-z]+)\s+(?:rdf:type|a)\s`).FindAllStringSubmatch(src, -1) {
		want[m[1]] = true
	}
	for term := range want {
		if !got[term] {
			t.Errorf("the reader missed %s", term)
		}
	}
	for term := range got {
		if !want[term] {
			t.Errorf("the reader found %s, which is not declared at the start of a line", term)
		}
	}
}

// Facts about a term are merged across statements: CertificateAuthority gets
// its second superclass in a statement of its own.
func TestFactsAreMergedAcrossStatements(t *testing.T) {
	terms, err := ReadAFO(afoSource(t))
	if err != nil {
		t.Fatal(err)
	}
	ca := terms["CertificateAuthority"]
	if ca == nil || !slices.Contains(ca.Supers, "System") || !slices.Contains(ca.Supers, "CoreSystem") {
		t.Fatalf("CertificateAuthority: %+v", ca)
	}
	if !strings.HasPrefix(terms["Authorizer"].Comment, "Decides what a certified system may do") {
		t.Errorf("a long comment: %q", terms["Authorizer"].Comment)
	}
}

func TestTurtleConstructs(t *testing.T) {
	src := `@prefix : <https://w3id.org/synecdoque/afo#> .
# a comment ; with punctuation .
:A a owl:Class ;
    rdfs:subClassOf :B , [ a owl:Restriction ; owl:onProperty :p ; owl:minCardinality "1"^^xsd:nonNegativeInteger ] ;
    rdfs:label "a"@en ;
    rdfs:comment """Two
lines.""" ;
    owl:versionIRI <https://w3id.org/synecdoque/afo/2.4.0> .
:B rdf:type owl:Class ; rdfs:comment "Say \"B\"." .
:A rdfs:subClassOf :C .
`
	terms, err := ReadAFO(src)
	if err != nil {
		t.Fatal(err)
	}
	if a := terms["A"]; a == nil || strings.Join(a.Supers, ",") != "B,C" || a.Comment != "Two lines." {
		t.Errorf("A: %+v", a)
	}
	if b := terms["B"]; b == nil || b.Comment != `Say "B".` {
		t.Errorf("B: %+v", b)
	}
}

// A superclass implied by another is left out: CertificateAuthority is a
// System and a CoreSystem in AFO, and specializes only CoreSystem in mAF.
func TestImpliedSuperclassesAreLeftOut(t *testing.T) {
	afo := `:System a owl:Class .
:CoreSystem a owl:Class ; rdfs:subClassOf :System .
:CertificateAuthority a owl:Class ; rdfs:subClassOf :System , :CoreSystem .
`
	table := "System\tmaf\tabstract part def\tArrowheadSystem\t-\t-\t-\n" +
		"CoreSystem\tmaf\tabstract part def\tCoreSystem\t-\t-\t-\n" +
		"CertificateAuthority\tmaf\tabstract part def\tCertificateAuthority\t-\t-\t-\n"
	out, err := Generate(afo, table, "package mAF {\n    // @generated\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "abstract part def CertificateAuthority :> CoreSystem {") {
		t.Errorf("CertificateAuthority:\n%s", out)
	}
	if !strings.Contains(out, "abstract part def CoreSystem :> ArrowheadSystem {") {
		t.Errorf("CoreSystem:\n%s", out)
	}
}

func TestAnOwnerCarriesItsOwnMultiplicity(t *testing.T) {
	table := "E\tmaf\tattribute def\tEndpoint\t-\t-\t-\n" +
		"X\tmaf\tpart def\tExternalAttachment\t-\t-\t-\n" +
		"p\tmaf\tattribute\tprotocol\tEndpoint, ExternalAttachment[0..1]\tString\t-\n"
	out, err := Generate(":E a owl:Class .\n:X a owl:Class .\n", table, "    // @generated\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "attribute def Endpoint {\n        attribute protocol : String;") ||
		!strings.Contains(out, "part def ExternalAttachment {\n        attribute protocol : String[0..1];") {
		t.Errorf("multiplicities:\n%s", out)
	}
}

func TestAFeatureOfAnUndefinedOwnerIsRefused(t *testing.T) {
	table := "p\tmaf\tattribute\tprotocol\tNowhere\tString\t-\n"
	if _, err := Generate("", table, "    // @generated\n"); err == nil || !strings.Contains(err.Error(), "Nowhere") {
		t.Errorf("an attribute of a definition no row defines: %v", err)
	}
}
