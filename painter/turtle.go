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

// Reading just enough of a system's ontology to draw it.

package main

import (
	"strings"
)

// statement is one fact read from a system's graph: a subject, a predicate and
// an object, with the prefixes left on.
type statement struct {
	subject   string // e.g. "alc:home_beekeeper"
	predicate string // e.g. "afo:hasName" or "a"
	object    string // e.g. `"beekeeper"` or "alc:home" or "<https://…>"
}

// readTurtle reads the subset of Turtle the framework emits.
//
// Not a Turtle parser, and deliberately not: the painter needs a picture, not a
// knowledge base. Anything it cannot read it ignores, because a system that
// describes itself in a way this does not understand should be drawn with what
// it did say rather than left out of the cloud altogether — an operator looking
// for a system needs to see that it is there far more than they need its
// details.
//
// What the framework writes is regular. A block opens with a subject and a
// type, continues with one predicate and object per line separated by
// semicolons, and closes with a full stop:
//
//	alc:home_beekeeper a afo:System ;
//	    afo:hasName "beekeeper" ;
//	    afo:runsOnHost alc:home .
//
// If that ever stops being true, the model tests will notice before an operator
// does: they drive the framework's own emitters rather than fixtures written
// here.
func readTurtle(doc string) []statement {
	var out []statement
	subject := ""

	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "@prefix") {
			continue
		}

		// A block ends at a full stop; a statement within it at a semicolon.
		terminated := strings.HasSuffix(line, ".")
		line = strings.TrimRight(line, " ;.")
		if line == "" {
			subject = ""
			continue
		}

		if subject == "" || !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
			// A new block: the subject is the first token.
			parts := splitTwo(line)
			if parts[1] == "" {
				continue
			}
			subject = parts[0]
			line = parts[1]
		}

		parts := splitTwo(line)
		if parts[1] == "" {
			continue
		}
		out = append(out, statement{subject: subject, predicate: parts[0], object: unquote(parts[1])})

		if terminated {
			subject = ""
		}
	}
	return out
}

// splitTwo splits a line into its first token and the remainder, which is the
// shape every statement here has: one predicate and one object.
func splitTwo(line string) [2]string {
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return [2]string{line, ""}
	}
	return [2]string{line[:i], strings.TrimSpace(line[i+1:])}
}

// unquote strips the punctuation a Turtle object carries so the value can be
// compared and displayed: the quotes around a literal, any datatype suffix, and
// the angle brackets around an IRI.
func unquote(object string) string {
	if strings.HasPrefix(object, "<") && strings.HasSuffix(object, ">") {
		return object[1 : len(object)-1]
	}
	if strings.HasPrefix(object, `"`) {
		if end := strings.LastIndex(object, `"`); end > 0 {
			return strings.ReplaceAll(object[1:end], `\"`, `"`)
		}
	}
	return object
}

// objects returns every object stated for one subject and predicate. A unit
// asset has many services and a host many addresses, so more than one answer is
// the normal case rather than an error.
func objects(statements []statement, subject, predicate string) []string {
	var out []string
	for _, s := range statements {
		if s.subject == subject && s.predicate == predicate {
			out = append(out, s.object)
		}
	}
	return out
}

// object returns the first object stated for one subject and predicate, or "".
func object(statements []statement, subject, predicate string) string {
	if found := objects(statements, subject, predicate); len(found) > 0 {
		return found[0]
	}
	return ""
}

// subjectsOfType returns every subject declared to be of one type.
func subjectsOfType(statements []statement, class string) []string {
	var out []string
	for _, s := range statements {
		if s.predicate == "a" && s.object == class {
			out = append(out, s.subject)
		}
	}
	return out
}
