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

// What the cloud says about itself, read once so the checks can reason about it.
//
// Against GraphDB rather than each system's /kgraph, because the store holds
// more than the systems said: the reasoner's entailments, and — where the plant
// design and lifecycle views have been loaded — the P&ID tags and serial numbers
// the alignment bridges bring in. An analysis of what can go wrong should see
// everything that is known, not only what a system was willing to say about
// itself in one HTTP response.
package main

import (
	"sort"
	"strings"
)

// Cloud is the whole scope of the assessment.
type Cloud struct {
	Name   string
	Assets []*Asset
	Hosts  map[string][]string // host name → the systems that declare they run on it
}

// Asset is one unit asset: the thing that does something, and therefore the
// thing that can fail to do it.
type Asset struct {
	IRI      string
	System   string
	Name     string
	Mission  string
	Location string
	// LocationIsIRI distinguishes alc:Kitchen from the string "Kitchen ". Both
	// appear in the same cloud, which is itself a finding: two systems naming
	// the same room in two vocabularies cannot be paired by anything that
	// compares them.
	LocationIsIRI bool
	// Host is the machine this asset's system declares it runs on. Placement is
	// the graph's to know; whether it could be otherwise is Mobility's.
	Host string
	// Mobility is "fixed", "tethered" or "movable", and empty when the asset has
	// not said. TetheredTo names what a tethered asset must still reach — a
	// tethered asset naming nothing is read as fixed, because a move nobody can
	// check is a move nobody should make.
	Mobility   string
	TetheredTo []string
	Provides   []*Service
	Consumes   []*Consumption
}

// Service is something an asset offers.
type Service struct {
	IRI          string
	Definition   string
	Name         string
	Subscribable bool
	Unit         string
	QuantityKind string
	RegPeriod    int
	// Range is the permitted span a writable service declares, if any. Its
	// absence on something that can be written is a finding: nothing then stops
	// a setpoint being driven to a value the plant cannot survive.
	Range []string
	URLs  []string
	// Forms is the payload form or media type the service registered.
	Forms []string
	// Methods is what the service declared it answers, as W3C HTTP method IRIs.
	// Empty means it never said, and a consumer assumes a read.
	Methods []string
	// Consumers is filled in after the query: which consumptions point here.
	// A service nobody consumes is either spare capacity or an omission, and
	// the graph cannot tell which — but it can say that it is one of the two.
	Consumers []*Consumption
}

// Consumption is one asset's dependence on another's service.
type Consumption struct {
	IRI        string
	Definition string
	Mode       string
	FromURL    string
	// Target is the provided service this resolves to, or nil when the binding
	// names something the graph does not hold — a dangling dependence, which is
	// a failure that has already happened rather than one that might.
	Target *Service
	Owner  *Asset
}

// Posture is what a system says about its own security.
type Posture struct {
	System           string
	Level            string
	NamesAuthorizer  bool
	VerifiesTokens   bool
	AcceptsPlaintext bool
	OffersTLS        bool
}

// resolve links the graph's pieces to each other: a consumption to the service
// it consumes, and a service back to whoever consumes it.
//
// Done here rather than in SPARQL because it is a join the query would have to
// repeat for every question afterwards, and because what matters most is the
// case where the join *fails* — a consumption whose target is absent. A LEFT
// JOIN that silently drops those would hide exactly the findings worth having.
func (c *Cloud) resolve() {
	byIRI := map[string]*Service{}
	byDefinition := map[string][]*Service{}
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			byIRI[s.IRI] = s
			byDefinition[s.Definition] = append(byDefinition[s.Definition], s)
		}
	}

	for _, a := range c.Assets {
		for _, cons := range a.Consumes {
			cons.Owner = a
			if s, ok := byIRI[cons.IRI]; ok {
				cons.Target = s
			} else if matches := byDefinition[cons.Definition]; len(matches) == 1 {
				// Bound by definition rather than by identity. One provider is
				// unambiguous; several is what orchestration exists to resolve,
				// and picking one here would invent a binding the cloud has not
				// made.
				cons.Target = matches[0]
			}
			if cons.Target != nil {
				cons.Target.Consumers = append(cons.Target.Consumers, cons)
			}
		}
	}
}

// Immovable reports whether this asset cannot be moved off its host.
//
// A tethered asset that names no dependency counts as immovable: the framework
// documents TetheredTo as the obligation that comes with the claim, and a
// balancer cannot verify reachability against a dependency nobody stated.
//
// An asset that has said nothing at all is *not* immovable here. It is unknown,
// which is a different finding with a different remedy — one is a fact about the
// plant, the other is a gap in the model.
func (a *Asset) Immovable() bool {
	switch a.Mobility {
	case "fixed":
		return true
	case "tethered":
		return len(a.TetheredTo) == 0
	default:
		return false
	}
}

// Providers returns every service in the cloud offering one definition. Two of
// them is redundancy; one is a single point of failure; none is a dangling
// dependence.
func (c *Cloud) Providers(definition string) []*Service {
	var out []*Service
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			if s.Definition == definition {
				out = append(out, s)
			}
		}
	}
	return out
}

// AssetOf returns the asset providing a service.
func (c *Cloud) AssetOf(s *Service) *Asset {
	for _, a := range c.Assets {
		for _, provided := range a.Provides {
			if provided == s {
				return a
			}
		}
	}
	return nil
}

// Downstream returns everything that depends on a service, transitively.
//
// This is the half of an FMEA that a spreadsheet makes people do by hand and
// that a graph simply knows: the local effect is who consumes this, and the end
// effect is what that reaches in turn. A controller losing its temperature is a
// local effect; the room it no longer regulates is the end effect, and the path
// between them is `afo:consumesService` followed to its leaves.
func (c *Cloud) Downstream(s *Service) []*Asset {
	seen := map[*Asset]bool{}
	var walk func(*Service)
	walk = func(s *Service) {
		for _, cons := range s.Consumers {
			a := cons.Owner
			if a == nil || seen[a] {
				continue
			}
			seen[a] = true
			for _, onward := range a.Provides {
				walk(onward)
			}
		}
	}
	walk(s)

	out := make([]*Asset, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IRI < out[j].IRI })
	return out
}

// Writable reports whether a service can be driven, and therefore whether an
// unconstrained value can do harm rather than merely be wrong.
func (s *Service) Writable() bool {
	return s.hasMethod("PUT") || s.hasMethod("POST")
}

// ForPeople reports whether this service exists for somebody to read rather
// than for a system to consume.
//
// A page describing a service is documentation, and documentation has no
// failure mode worth an FMEA row: nothing downstream stops working when it is
// unavailable. It is also, in a cloud that enforces authorization, not reachable
// from a browser at all — a browser presents no client certificate, so the
// subject is empty and no policy can name it.
func (s *Service) ForPeople() bool {
	for _, f := range s.Forms {
		if strings.Contains(strings.ToLower(f), "html") {
			return true
		}
	}
	return false
}

func (s *Service) hasMethod(name string) bool {
	for _, m := range s.Methods {
		if strings.EqualFold(localName(m), name) {
			return true
		}
	}
	return false
}

// localName is the last segment of an IRI, and the value itself when it is not
// one.
//
// Both the full form and the prefixed one occur: SPARQL returns
// http://www.synecdoque.com/lcloud/Kitchen, while a graph read as text carries
// alc:Kitchen. Both name the same room and an FMEA reader wants to see
// "Kitchen".
func localName(value string) string {
	if i := strings.LastIndexAny(value, "#/"); i >= 0 && i+1 < len(value) {
		return value[i+1:]
	}
	if i := strings.IndexByte(value, ':'); i > 0 && i+1 < len(value) && !strings.Contains(value, " ") {
		return value[i+1:]
	}
	return value
}
