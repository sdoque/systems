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
	"fmt"
	"strings"
)

// Term is what the generator needs to know about one AFO term.
type Term struct {
	Types   []string // e.g. owl:Class, owl:ObjectProperty, :Mode
	Comment string   // rdfs:comment
	Supers  []string // named rdfs:subClassOf, without the leading ':'
}

// ReadAFO reads the terms afo.ttl declares in its own namespace — the default
// prefix ':' — with their types, comments and named superclasses.
//
// Not a general Turtle parser: enough of the grammar for how this ontology is
// written — prefixed names, IRIs, short and long strings with a language tag
// or datatype, predicate lists, object lists, blank-node and collection
// objects (skipped) — and it says where it stopped if it meets anything else.
// A statement may be split: afo.ttl gives CertificateAuthority its second
// superclass in a statement of its own, so facts about a term are merged.
func ReadAFO(src string) (map[string]*Term, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	terms := make(map[string]*Term)
	p := &parser{toks: toks}
	for !p.done() {
		if p.peek() == "@prefix" || p.peek() == "@base" {
			p.skipTo(".")
			continue
		}
		subject := p.next()
		if subject == "[" {
			p.skipGroup("[", "]")
			p.skipTo(".")
			continue
		}
		var term *Term
		if name, ok := strings.CutPrefix(subject, ":"); ok && name != "" {
			if terms[name] == nil {
				terms[name] = &Term{}
			}
			term = terms[name]
		}
		if err := p.predicateList(term); err != nil {
			return nil, fmt.Errorf("afo.ttl, statement about %s: %w", subject, err)
		}
	}
	for name, t := range terms {
		if len(t.Types) == 0 && len(t.Supers) == 0 && t.Comment == "" {
			delete(terms, name)
		}
	}
	return terms, nil
}

// Declared lists the terms with an rdf:type — the ones the ontology declares
// rather than merely mentions.
func Declared(terms map[string]*Term) map[string]bool {
	d := make(map[string]bool)
	for name, t := range terms {
		if len(t.Types) > 0 {
			d[name] = true
		}
	}
	return d
}

type parser struct {
	toks []string
	i    int
}

func (p *parser) done() bool   { return p.i >= len(p.toks) }
func (p *parser) peek() string { return p.toks[p.i] }
func (p *parser) next() string {
	t := p.toks[p.i]
	p.i++
	return t
}

func (p *parser) skipTo(end string) {
	for !p.done() && p.next() != end {
	}
}

func (p *parser) skipGroup(open, close string) {
	for depth := 1; !p.done() && depth > 0; {
		switch p.next() {
		case open:
			depth++
		case close:
			depth--
		}
	}
}

func (p *parser) predicateList(term *Term) error {
	for !p.done() {
		pred := p.next()
		if pred == "." {
			return nil
		}
		for {
			if p.done() {
				return fmt.Errorf("the file ends inside a statement")
			}
			obj := p.next()
			switch obj {
			case "[":
				p.skipGroup("[", "]")
			case "(":
				p.skipGroup("(", ")")
			default:
				if term != nil {
					record(term, pred, obj)
				}
			}
			if p.done() {
				return fmt.Errorf("the file ends inside a statement")
			}
			switch sep := p.next(); sep {
			case ",":
				continue
			case ";":
				// A trailing ';' before the '.' is legal Turtle.
				if !p.done() && p.peek() == "." {
					p.next()
					return nil
				}
			case ".":
				return nil
			default:
				return fmt.Errorf("expected ',', ';' or '.', found %q", sep)
			}
			break
		}
	}
	return fmt.Errorf("the file ends inside a statement")
}

func record(t *Term, pred, obj string) {
	switch pred {
	case "a", "rdf:type":
		t.Types = append(t.Types, obj)
	case "rdfs:subClassOf":
		if name, ok := strings.CutPrefix(obj, ":"); ok && name != "" {
			t.Supers = append(t.Supers, name)
		}
	case "rdfs:comment":
		if t.Comment == "" && strings.HasPrefix(obj, `"`) {
			t.Comment = unquote(obj)
		}
	}
}

// unquote takes a string token — with any language tag or datatype — to its
// text, with runs of whitespace (the long strings' line breaks) made single
// spaces.
func unquote(tok string) string {
	body := tok
	if strings.HasPrefix(body, `"""`) {
		body = body[3:]
		body = body[:strings.Index(body, `"""`)]
	} else {
		end := strings.LastIndex(body, `"`)
		body = body[1:end]
		body = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(body)
	}
	return strings.Join(strings.Fields(body), " ")
}

// tokenize splits Turtle into IRIs, strings (with their tags), punctuation and
// names, dropping comments.
func tokenize(src string) ([]string, error) {
	var toks []string
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '<':
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				return nil, fmt.Errorf("an IRI is not closed")
			}
			toks = append(toks, src[i:i+end+1])
			i += end + 1
		case c == '"':
			start := i
			if strings.HasPrefix(src[i:], `"""`) {
				end := strings.Index(src[i+3:], `"""`)
				if end < 0 {
					return nil, fmt.Errorf("a long string is not closed")
				}
				i += 3 + end + 3
			} else {
				i++
				for i < len(src) && src[i] != '"' {
					if src[i] == '\\' {
						i++
					}
					i++
				}
				if i >= len(src) {
					return nil, fmt.Errorf("a string is not closed")
				}
				i++
			}
			// A language tag or a datatype belongs to the literal.
			if i < len(src) && src[i] == '@' {
				for i < len(src) && !isDelimiter(src, i) {
					i++
				}
			} else if strings.HasPrefix(src[i:], "^^") {
				i += 2
				if i < len(src) && src[i] == '<' {
					i += strings.IndexByte(src[i:], '>') + 1
				} else {
					for i < len(src) && !isDelimiter(src, i) {
						i++
					}
				}
			}
			toks = append(toks, src[start:i])
		case strings.IndexByte("[](),;", c) >= 0:
			toks = append(toks, string(c))
			i++
		case c == '.' && isDelimiter(src, i):
			toks = append(toks, ".")
			i++
		default:
			start := i
			for i < len(src) && !isDelimiter(src, i) {
				i++
			}
			toks = append(toks, src[start:i])
		}
	}
	return toks, nil
}

// isDelimiter reports whether src[i] ends a name: whitespace, punctuation, or
// a '.' that ends the statement rather than sitting inside a name.
func isDelimiter(src string, i int) bool {
	c := src[i]
	switch {
	case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		return true
	case strings.IndexByte("[](),;<\"#", c) >= 0:
		return true
	case c == '.':
		return i+1 >= len(src) || strings.IndexByte(" \t\r\n", src[i+1]) >= 0
	}
	return false
}
