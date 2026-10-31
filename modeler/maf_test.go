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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
	"github.com/sdoque/systems/modeler/mafgen"
)

// mAF is written by hand and the framework moves on without it: it went five
// months without the authorizer, the maitreD, units or the mission type. These
// tests are where that drift now shows.

// The Mission enumeration is the framework's closed list, no more and no less.
func TestMAFMissionsAreTheFrameworks(t *testing.T) {
	body := regexp.MustCompile(`(?s)enum def Mission \{(.*?)\}`).FindStringSubmatch(mAFLibrary)
	if body == nil {
		t.Fatal("mAF declares no Mission enumeration")
	}
	var declared []string
	for _, m := range regexp.MustCompile(`enum '([^']+)';`).FindAllStringSubmatch(body[1], -1) {
		declared = append(declared, m[1])
	}
	var framework []string
	for _, m := range components.Missions {
		framework = append(framework, m.String())
	}
	sort.Strings(declared)
	sort.Strings(framework)
	if strings.Join(declared, " ") != strings.Join(framework, " ") {
		t.Errorf("mAF's missions %v, the framework's %v", declared, framework)
	}
}

// Every unit the SysML mapping expects mAF to define is defined there.
func TestMAFDeclaresTheUnitsTheMappingUses(t *testing.T) {
	for _, ref := range usecases.SysMLUnitRefs() {
		name, inMAF := strings.CutPrefix(ref, "mAF::")
		if !inMAF {
			continue
		}
		// A unit is declared by its short name: attribute <ms> millisecond,
		// attribute <'r/min'> 'revolution per minute'.
		if !strings.Contains(mAFLibrary, "attribute <"+name+">") {
			t.Errorf("the unit mapping refers to %s, which mAF does not declare", ref)
		}
	}
}

// The core systems the modeler types by role are all in mAF.
func TestMAFDeclaresEveryCoreSystem(t *testing.T) {
	for _, role := range []string{"ServiceRegistrar", "Orchestrator", "CertificateAuthority", "Authorizer", "MaitreD"} {
		if !strings.Contains(mAFLibrary, "abstract part def "+role+" ") {
			t.Errorf("mAF has no %s", role)
		}
	}
}

// ── the AFO ↔ mAF mapping ─────────────────────────────────────────────────────

func readSources(t *testing.T) (afo, table, frame string) {
	t.Helper()
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read(filepath.Join("..", "kgrapher", "ontologies", "afo.ttl")),
		read("afo-maf-mapping.tsv"), read("maf-frame.sysml")
}

// Every term AFO declares has a decision recorded, and only those terms.
func TestEveryAFOTermIsMapped(t *testing.T) {
	afo, table, _ := readSources(t)
	terms, err := mafgen.ReadAFO(afo)
	if err != nil {
		t.Fatal(err)
	}
	declared := mafgen.Declared(terms)
	rows, err := mafgen.ReadTable(table)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]int)
	for _, r := range rows {
		if prev, twice := seen[r.Term]; twice {
			t.Errorf("afo-maf-mapping.tsv:%d maps %s again (first on line %d)", r.Line, r.Term, prev)
		}
		seen[r.Term] = r.Line
		if r.Status != mafgen.StatusExtension && !declared[r.Term] {
			t.Errorf("afo-maf-mapping.tsv:%d maps %s, which afo.ttl does not declare", r.Line, r.Term)
		}
	}
	var missing []string
	for term := range declared {
		if _, ok := seen[term]; !ok {
			missing = append(missing, term)
		}
	}
	sort.Strings(missing)
	for _, term := range missing {
		t.Errorf("afo.ttl declares %s and afo-maf-mapping.tsv does not say how SysML shows it", term)
	}
}

// mAF.sysml is what the generator makes of AFO, the table and the frame. A
// hand edit, or an edit to any of the three without `go generate`, fails here.
func TestMAFIsGenerated(t *testing.T) {
	afo, table, frame := readSources(t)
	want, err := mafgen.Generate(afo, table, frame)
	if err != nil {
		t.Fatal(err)
	}
	if mAFLibrary != want {
		t.Error("mAF.sysml is not what mafgen generates from afo.ttl, afo-maf-mapping.tsv and maf-frame.sysml: run `go generate` in the modeler, and edit those rather than mAF.sysml")
	}
}
