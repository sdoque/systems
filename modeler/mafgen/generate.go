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

// Package mafgen generates mAF, the SysML v2 library of the Arrowhead
// framework, from AFO and the mapping table that says how each AFO term
// appears in SysML v2.
//
// mAF was written by hand and drifted from AFO for five months: no authorizer,
// no maitreD, no units, no security posture. Generated, it cannot: a term AFO
// gains either has a row saying how SysML shows it or the modeler's tests fail,
// and each definition's documentation is AFO's own comment.
package mafgen

import (
	"fmt"
	"slices"
	"strings"
)

// Row is one line of afo-maf-mapping.tsv.
type Row struct {
	Term, Status, Kind, Name, Owner, Type, Mult, Note string
	Line                                              int
}

// Statuses a row may have.
const (
	StatusMAF        = "maf"
	StatusGenerated  = "generated"
	StatusNotModeled = "not-modeled"
	StatusExtension  = "extension"
)

// ReadTable parses the mapping table.
func ReadTable(src string) ([]Row, error) {
	var rows []Row
	for i, line := range strings.Split(src, "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "term\t") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 7 {
			return nil, fmt.Errorf("afo-maf-mapping.tsv:%d has %d columns, want 7 or 8", i+1, len(cols))
		}
		for len(cols) < 8 {
			cols = append(cols, "")
		}
		for j := range cols {
			if cols[j] == "-" {
				cols[j] = ""
			}
		}
		r := Row{cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], i + 1}
		switch r.Status {
		case StatusMAF, StatusExtension:
			if r.Kind == "" || r.Name == "" {
				return nil, fmt.Errorf("afo-maf-mapping.tsv:%d: a %s row needs a kind and a name", r.Line, r.Status)
			}
			if !r.IsDef() && r.Owner == "" {
				return nil, fmt.Errorf("afo-maf-mapping.tsv:%d: %s %s has no owner", r.Line, r.Kind, r.Name)
			}
		case StatusGenerated, StatusNotModeled:
		default:
			return nil, fmt.Errorf("afo-maf-mapping.tsv:%d: status %q is not maf, generated, not-modeled or extension", r.Line, r.Status)
		}
		if (r.Status == StatusExtension) != strings.HasPrefix(r.Term, "+") {
			return nil, fmt.Errorf("afo-maf-mapping.tsv:%d: an extension row, and only one, names its term with a leading '+'", r.Line)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// IsDef reports whether the row is a definition rather than a feature or literal.
func (r Row) IsDef() bool { return strings.HasSuffix(r.Kind, " def") }

// Token is text mAF contains when the row is in it: "def Host",
// "attribute qudtUnit", "enum 'get'".
func (r Row) Token() string {
	if r.IsDef() {
		return "def " + r.Name
	}
	return r.Kind + " " + r.Name
}

// owners splits "Endpoint, ExternalAttachment[0..1]" into owners and their own
// multiplicities.
func (r Row) owners() map[string]string {
	o := make(map[string]string)
	for _, item := range strings.Split(r.Owner, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		mult := r.Mult
		if i := strings.Index(item, "["); i >= 0 {
			mult, item = item[i:], item[:i]
		}
		o[item] = mult
	}
	return o
}

// Generate writes mAF: the frame, with the definitions the table maps AFO to
// in place of the frame's "// @generated" line.
func Generate(afo, table, frame string) (string, error) {
	terms, err := ReadAFO(afo)
	if err != nil {
		return "", err
	}
	rows, err := ReadTable(table)
	if err != nil {
		return "", err
	}
	const marker = "    // @generated\n"
	if strings.Count(frame, marker) != 1 {
		return "", fmt.Errorf("maf-frame.sysml must contain the line %q exactly once", strings.TrimSpace(marker))
	}

	// The definitions, by AFO term and by name.
	defOfTerm := make(map[string]string)
	isDef := make(map[string]bool)
	for _, r := range rows {
		if (r.Status == StatusMAF || r.Status == StatusExtension) && r.IsDef() {
			defOfTerm[r.Term] = r.Name
			isDef[r.Name] = true
		}
	}
	for _, r := range rows {
		if r.Status != StatusMAF && r.Status != StatusExtension || r.IsDef() {
			continue
		}
		for owner := range r.owners() {
			if !isDef[owner] {
				return "", fmt.Errorf("afo-maf-mapping.tsv:%d: %s is owned by %s, which no row defines", r.Line, r.Name, owner)
			}
		}
	}

	// supers maps a definition to those it specializes: AFO's named
	// superclasses that are definitions here, less any implied by another.
	// CertificateAuthority is a subclass of System and of CoreSystem in AFO;
	// in mAF it specializes CoreSystem, which specializes ArrowheadSystem.
	direct := make(map[string][]string)
	for term, name := range defOfTerm {
		if t := terms[term]; t != nil {
			for _, s := range t.Supers {
				if sup, ok := defOfTerm[s]; ok && !slices.Contains(direct[name], sup) {
					direct[name] = append(direct[name], sup)
				}
			}
		}
	}
	var ancestors func(string, map[string]bool)
	ancestors = func(name string, seen map[string]bool) {
		for _, s := range direct[name] {
			if !seen[s] {
				seen[s] = true
				ancestors(s, seen)
			}
		}
	}
	supersOf := func(name string) []string {
		var keep []string
		for _, s := range direct[name] {
			implied := false
			for _, other := range direct[name] {
				if other == s {
					continue
				}
				seen := make(map[string]bool)
				ancestors(other, seen)
				if seen[s] {
					implied = true
				}
			}
			if !implied {
				keep = append(keep, s)
			}
		}
		slices.Sort(keep)
		return keep
	}

	var b strings.Builder
	for _, def := range rows {
		if def.Status != StatusMAF && def.Status != StatusExtension || !def.IsDef() {
			continue
		}
		fmt.Fprintf(&b, "    %s %s", def.Kind, def.Name)
		if sup := supersOf(def.Name); len(sup) > 0 {
			fmt.Fprintf(&b, " :> %s", strings.Join(sup, ", "))
		}
		b.WriteString(" {\n")
		if t := terms[def.Term]; t != nil && t.Comment != "" {
			b.WriteString(doc(t.Comment, "        "))
		}
		for _, r := range rows {
			if r.Status != StatusMAF && r.Status != StatusExtension || r.IsDef() {
				continue
			}
			mult, owned := r.owners()[def.Name]
			if !owned {
				continue
			}
			if r.Kind == "enum" {
				fmt.Fprintf(&b, "        enum %s;\n", r.Name)
				continue
			}
			fmt.Fprintf(&b, "        %s %s : %s%s;\n", r.Kind, r.Name, r.Type, mult)
		}
		b.WriteString("    }\n\n")
	}
	return strings.Replace(frame, marker, b.String(), 1), nil
}

// doc renders a comment as a SysML v2 documentation comment, wrapped.
func doc(text, indent string) string {
	var b strings.Builder
	b.WriteString(indent + "doc\n" + indent + "/*\n")
	line := ""
	for _, w := range strings.Fields(strings.ReplaceAll(text, "*/", "* /")) {
		if line != "" && len(line)+1+len(w) > 72 {
			b.WriteString(indent + " * " + line + "\n")
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		b.WriteString(indent + " * " + line + "\n")
	}
	b.WriteString(indent + " */\n")
	return b.String()
}
