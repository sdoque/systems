/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
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

// The policy engine decides whether a subject may perform an action on a
// provider's service. It is deliberately free of I/O: no network, no clock, no
// token signing. Everything it needs arrives in a Request, so a decision is a
// pure function of the policy file and the question asked, and every case in
// POLICY.md can be exercised as a table test.
//
// The schema and its rationale are specified in POLICY.md, which is normative;
// this file is its implementation.

package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// DefaultTTL is the lifetime of a token whose authorizing rule sets none.
const DefaultTTL = 5 * time.Minute

// Wildcard matches any subject, mission, service or action.
const Wildcard = "*"

// Actions a consumer can ask for, mapped to cervice modes and HTTP methods in
// POLICY.md. `invoke` is specified ahead of the framework: no cervice can express
// it yet, so it is accepted in a policy file but nothing will request it.
const (
	ActionRead   = "read"
	ActionWrite  = "write"
	ActionInvoke = "invoke"
)

var validActions = []string{ActionRead, ActionWrite, ActionInvoke}

// Rule is one allow entry. A request is authorized when at least one rule
// matches it and no denial does.
type Rule struct {
	Subject            string   `json:"subject"`
	Missions           []string `json:"missions"`
	Services           []string `json:"services,omitempty"`
	Actions            []string `json:"actions"`
	MustMatchAttribute string   `json:"must_match_attribute,omitempty"`
	TTL                string   `json:"ttl,omitempty"`
}

// Denial blocks one (subject, asset) pair whatever the rules say. Asset is
// qualified by its system, as "system/asset".
type Denial struct {
	Subject string `json:"subject"`
	Asset   string `json:"asset"`
}

// Policies is the content of policies.json. Its zero value denies everything,
// so a missing or empty file leaves every authenticated system inert rather
// than unrestricted.
type Policies struct {
	Rules   []Rule   `json:"policies"`
	Denials []Denial `json:"denials"`
}

// Request is one authorization question: who is asking, what they want to do,
// and which registered service they want to do it to.
type Request struct {
	// Subject is the Common Name of the caller's verified client certificate.
	// It is never a name the caller supplied in a form.
	Subject string
	// SubjectAttributes are the subject's own registered details, used by a
	// rule's MustMatchAttribute. The framework invariant that every system
	// provides at least one service is what guarantees these exist.
	SubjectAttributes map[string][]string
	Action            string
	Record            forms.ServiceRecord_v1
}

// Decision is the engine's answer. Reason explains it either way: an operator
// debugging a refused control loop needs to know which rule was missing, and an
// audit trail of allowances is worth as much as one of refusals.
type Decision struct {
	Allowed bool
	TTL     time.Duration
	Reason  string
}

// LoadPolicies parses a policies.json document and validates it.
//
// An unparsable or invalid file is an error rather than an empty policy set:
// silently falling back to "deny everything" would look identical to a correct
// lockdown, and an operator would have no way to tell a typo from an intent.
func LoadPolicies(data []byte) (Policies, error) {
	var p Policies
	if err := json.Unmarshal(data, &p); err != nil {
		return Policies{}, fmt.Errorf("parsing policies: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Policies{}, err
	}
	return p, nil
}

// Validate rejects a policy file that cannot mean what it says: a mission
// outside the taxonomy, an unknown action, or a duration that will not parse.
// Catching these at load keeps them from silently never matching at runtime,
// which would read as "the rule is wrong" rather than "the rule is misspelt".
func (p Policies) Validate() error {
	for i, r := range p.Rules {
		if r.Subject == "" {
			return fmt.Errorf("policy %d: no subject", i)
		}
		if len(r.Missions) == 0 {
			return fmt.Errorf("policy %d (subject %q): no missions", i, r.Subject)
		}
		if len(r.Actions) == 0 {
			return fmt.Errorf("policy %d (subject %q): no actions", i, r.Subject)
		}
		for _, m := range r.Missions {
			if m == Wildcard {
				continue
			}
			if !components.ValidMission(m) {
				return fmt.Errorf("policy %d (subject %q): unknown mission %q: expected one of %s",
					i, r.Subject, m, strings.Join(components.Missions, ", "))
			}
		}
		for _, a := range r.Actions {
			if a == Wildcard {
				continue
			}
			if !contains(validActions, a) {
				return fmt.Errorf("policy %d (subject %q): unknown action %q: expected one of %s",
					i, r.Subject, a, strings.Join(validActions, ", "))
			}
		}
		if r.TTL != "" {
			if _, err := time.ParseDuration(r.TTL); err != nil {
				return fmt.Errorf("policy %d (subject %q): bad ttl %q: %w", i, r.Subject, r.TTL, err)
			}
		}
	}
	for i, d := range p.Denials {
		if d.Subject == "" || d.Asset == "" {
			return fmt.Errorf("denial %d: both subject and asset are required", i)
		}
	}
	return nil
}

// Decide answers one request. It denies by default: an unmatched request is
// refused, and so is one this engine cannot reason about.
func Decide(p Policies, req Request) Decision {
	if req.Subject == "" {
		return Decision{Reason: "no subject: the caller presented no verified certificate"}
	}
	if req.Action == "" {
		return Decision{Reason: "no action requested"}
	}

	asset := AssetOf(req.Record)

	// Denials win over every rule, so they are checked first and cheaply.
	for _, d := range p.Denials {
		if matches(d.Subject, req.Subject) && d.Asset == asset {
			return Decision{Reason: fmt.Sprintf("denied: %q is blocked from %q", d.Subject, d.Asset)}
		}
	}

	// The shortest TTL among the matching rules wins. POLICY.md does not say
	// which rule's lifetime applies when several authorize the same request;
	// taking the shortest bounds revocation latency by the most cautious rule an
	// operator wrote rather than by the order they happened to list them in.
	allowed := false
	ttl := time.Duration(0)
	reason := ""

	for i, r := range p.Rules {
		if !matches(r.Subject, req.Subject) {
			continue
		}
		if !matchesAny(r.Missions, req.Record.Mission) {
			continue
		}
		if !matchesService(r.Services, req.Record.ServiceDefinition) {
			continue
		}
		if !matchesAny(r.Actions, req.Action) {
			continue
		}
		if !attributeMatches(r.MustMatchAttribute, req) {
			continue
		}

		ruleTTL := DefaultTTL
		if r.TTL != "" {
			if parsed, err := time.ParseDuration(r.TTL); err == nil {
				ruleTTL = parsed
			}
		}
		if !allowed || ruleTTL < ttl {
			ttl = ruleTTL
			reason = fmt.Sprintf("policy %d (subject %q) permits %s on mission %q",
				i, r.Subject, req.Action, req.Record.Mission)
		}
		allowed = true
	}

	if !allowed {
		return Decision{Reason: fmt.Sprintf("no policy permits %q to %s %q (mission %q)",
			req.Subject, req.Action, asset, req.Record.Mission)}
	}
	return Decision{Allowed: true, TTL: ttl, Reason: reason}
}

// AssetOf returns the "system/asset" identity a denial names. A registration
// record's SubPath is "<asset>/<service subpath>", so the asset is its first
// segment.
func AssetOf(rec forms.ServiceRecord_v1) string {
	assetName := rec.SubPath
	if i := strings.Index(assetName, "/"); i >= 0 {
		assetName = assetName[:i]
	}
	return rec.SystemName + "/" + assetName
}

// matches compares a policy pattern against a concrete value. A pattern may be
// the wildcard, a literal, or a shell-style glob such as "thermostat-*" — the
// form POLICY.md's worked examples use to cover a family of subjects.
func matches(pattern, value string) bool {
	if pattern == Wildcard {
		return true
	}
	if pattern == value {
		return true
	}
	ok, err := path.Match(pattern, value)
	return err == nil && ok
}

// matchesAny reports whether any pattern in the list matches the value. An empty
// list matches nothing: a rule listing no missions authorizes nothing, which
// Validate refuses to load in the first place.
func matchesAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if matches(p, value) {
			return true
		}
	}
	return false
}

// matchesService applies the optional service selector. Unlike missions and
// actions, omitting it means "every service of a matching asset" — it narrows a
// rule rather than defining it, so its absence cannot be an authorization gap.
func matchesService(patterns []string, definition string) bool {
	if len(patterns) == 0 {
		return true
	}
	return matchesAny(patterns, definition)
}

// attributeMatches implements the pairing rule of POLICY.md.
//
// The asset having no value for the attribute satisfies the constraint: in a
// plant many assets are cloud-wide utilities with no location, and requiring
// every consumer to declare a key for those would be over-engineering. The
// asset having one while the subject does not violates it, because that is an
// operator misconfiguration — and it is only safe to read it that way because
// every system provides at least one service and therefore has attributes at
// all.
//
// The attribute is looked up under its exact registration key, e.g.
// "FunctionalLocation".
func attributeMatches(attribute string, req Request) bool {
	if attribute == "" {
		return true
	}

	assetValues := req.Record.Details[attribute]
	if len(assetValues) == 0 {
		return true
	}

	subjectValues := req.SubjectAttributes[attribute]
	if len(subjectValues) == 0 {
		return false
	}

	for _, sv := range subjectValues {
		if contains(assetValues, sv) {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
