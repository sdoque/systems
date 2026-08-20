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

// What can go wrong, derived rather than imagined.
//
// Every finding below is a statement the graph entails. None of them is a
// prediction: where a row says a service has no consumer, no consumer exists;
// where it says a controller depends on one sensor, there is one sensor. The
// judgment about how much that matters happens elsewhere, in valuation.json.
//
// The effects are derived too, and that is the part a spreadsheet makes people
// do by hand. A local effect is whoever consumes the failing service; an end
// effect is what that reaches in turn, following afo:consumesService to its
// leaves. In a cottage with three heaters and one indoor module, the fact that
// losing that module opens the loop on two zones is a graph traversal, not an
// insight.
package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Finding is one row of the FMEA before it is valued.
type Finding struct {
	ID    string
	Block string
	Item  string
	// Function is what the item is for, so a reader who does not know the cloud
	// can judge the rest of the row.
	Function    string
	FailureMode string
	LocalEffect string
	EndEffect   string

	// The three classes the valuation file rates. They are the join keys
	// between what the graph knows and what the owner has decided.
	EffectClass    string
	CauseClass     string
	DetectionClass string

	Cause     string
	Detection string
	Action    string
	// Evidence is what in the graph makes this true, so a reader can check the
	// claim rather than trust it.
	Evidence string
}

// blockOf groups an asset the way an FMEA reader expects: by what it does in
// the chain rather than by which system happens to host it.
func blockOf(a *Asset) string {
	switch a.Mission {
	case "measurement":
		return "Sensing"
	case "actuation":
		return "Actuation"
	case "control":
		return "Control"
	case "core":
		return "Core"
	case "aggregation":
		return "Supervisory"
	case "":
		return "Unclassified"
	default:
		return strings.ToUpper(a.Mission[:1]) + a.Mission[1:]
	}
}

func itemOf(a *Asset) string { return a.System + " / " + a.Name }

// namesOf lists assets for an effect column, in a form a person reads.
func namesOf(assets []*Asset) string {
	if len(assets) == 0 {
		return "Nothing in the model depends on it"
	}
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		out = append(out, itemOf(a))
	}
	return strings.Join(out, "; ")
}

// Assess runs every check and returns the findings, ordered so the output is
// stable between runs — a diff of two FMEAs should show what changed in the
// cloud, not what changed in a map iteration.
func Assess(c *Cloud) []*Finding {
	var out []*Finding
	for _, check := range []func(*Cloud) []*Finding{
		checkDanglingConsumption,
		checkOrphanService,
		checkSingleProvider,
		checkUnboundedWritable,
		checkPolledDependence,
		checkSharedSensorAcrossLocations,
		checkLocationVocabulary,
		checkLocationLiteral,
		checkSingleHost,
	} {
		out = append(out, check(c)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Block != out[j].Block {
			return out[i].Block < out[j].Block
		}
		return out[i].Item < out[j].Item
	})
	for i, f := range out {
		f.ID = fmt.Sprintf("A-%02d", i+1)
	}
	return out
}

// A consumer bound to something the cloud does not provide. This is not a
// failure that might happen: it has happened, and the only reason it is not an
// outage is that nobody has looked.
func checkDanglingConsumption(c *Cloud) []*Finding {
	var out []*Finding
	for _, a := range c.Assets {
		for _, cons := range a.Consumes {
			if cons.Target != nil {
				continue
			}
			out = append(out, &Finding{
				Block: blockOf(a), Item: itemOf(a),
				Function:    "Consume " + cons.Definition,
				FailureMode: "Dependence on " + cons.Definition + " resolves to nothing",
				LocalEffect: itemOf(a) + " never receives " + cons.Definition,
				EndEffect:   namesOf(c.Downstream(&Service{Consumers: []*Consumption{cons}})),
				EffectClass: "loss-of-input", CauseClass: "model-omission",
				DetectionClass: "graph-only",
				Cause:          "No system in the cloud provides " + cons.Definition + ", or the binding names a service that has been withdrawn",
				Detection:      "Visible in the graph; the consumer sees a failed discovery at runtime and nothing aggregates it",
				Action:         "Deploy a provider of " + cons.Definition + ", or remove the dependence",
				Evidence:       cons.IRI + " afo:consumes \"" + cons.Definition + "\" with no matching afo:providesService",
			})
		}
	}
	return out
}

// A service nobody consumes. The graph cannot say whether that is spare
// capacity or an omission — but it can say it is one of the two, and in the
// cottage it was both: ButtonEvent had no consumer because the reconciliation
// logic was never written, while Power and Energy had none because nothing had
// been asked to watch them.
func checkOrphanService(c *Cloud) []*Finding {
	var out []*Finding
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			if len(s.Consumers) > 0 || a.Mission == "core" {
				continue
			}
			// A page for a person to read is documentation, not a link in a
			// chain: nothing downstream stops working when it is unavailable.
			if s.ForPeople() {
				continue
			}
			out = append(out, &Finding{
				Block: blockOf(a), Item: itemOf(a) + " / " + s.Definition,
				Function:    "Publish " + s.Definition,
				FailureMode: s.Definition + " fails, degrades or reports wrongly",
				LocalEffect: "None the model can name: no declared consumer depends on this service",
				EndEffect: "Its failure and its success look alike from outside. Either nothing " +
					"acts on it — a control deviation nobody watches will not raise an alarm when " +
					"it drifts — or something does and never said so, in which case the model " +
					"understates what this cloud depends on",
				EffectClass: "unused-information", CauseClass: "model-omission",
				DetectionClass: "published-not-consumed",
				Cause: "No afo:consumesService binding names this service. Either none exists, " +
					"or a consumer reads it by URL without declaring the dependence",
				Detection: "None. A service with no consumer cannot be observed to have failed",
				Action: "Bind a consumer — an analytics asset watching a deviation is how a loop " +
					"drifting out of bounds gets noticed — or, if one already reads it, declare " +
					"that dependence so the model shows it. Withdraw the service if neither is true",
				Evidence: s.IRI + " is provided and appears as the object of no afo:consumes",
			})
		}
	}
	return out
}

// One provider of something a controller depends on. Redundancy is a decision
// with a cost, so this is not automatically a defect — but it is always worth
// stating, because the alternative is discovering it during the failure.
func checkSingleProvider(c *Cloud) []*Finding {
	var out []*Finding
	reported := map[string]bool{}
	for _, a := range c.Assets {
		for _, cons := range a.Consumes {
			if cons.Target == nil || reported[cons.Definition] {
				continue
			}
			providers := c.Providers(cons.Definition)
			if len(providers) != 1 {
				continue
			}
			reported[cons.Definition] = true
			owner := c.AssetOf(providers[0])
			out = append(out, &Finding{
				Block: blockOf(owner), Item: itemOf(owner) + " / " + cons.Definition,
				Function:    "Sole source of " + cons.Definition,
				FailureMode: "The only provider of " + cons.Definition + " stops answering",
				LocalEffect: namesOf(dependents(cons.Target)),
				EndEffect:   namesOf(c.Downstream(cons.Target)),
				EffectClass: "loss-of-input", CauseClass: "device-unavailable",
				DetectionClass: detectionClassOf(cons.Target),
				Cause: "Hardware, link or process failure of a single device. Nothing else " +
					"in the cloud provides " + cons.Definition,
				Detection: detectionProseOf(cons.Target),
				Action:    "Deploy a second provider of " + cons.Definition + ", or accept the exposure explicitly",
				Evidence:  "exactly one afo:providesService with afo:hasServiceDefinition \"" + cons.Definition + "\"",
			})
		}
	}
	return out
}

// Something that can be written and declares no permitted range. A setpoint
// with a unit and a quantity kind and no bounds is fully described and entirely
// unprotected: every consumer knows what the number means and nothing knows
// what it may not be.
func checkUnboundedWritable(c *Cloud) []*Finding {
	var out []*Finding
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			if !s.Writable() || len(s.Range) > 0 {
				continue
			}
			// A range bounds a quantity. A boolean command has no values between
			// its two, so "outside the permitted range" is not a thing that can
			// happen to it — and reporting it would put a row in front of a
			// reader that has no action behind it.
			if s.Unit == "" && s.QuantityKind == "" {
				continue
			}
			out = append(out, &Finding{
				Block: blockOf(a), Item: itemOf(a) + " / " + s.Definition,
				Function:    "Accept a written " + s.Definition,
				FailureMode: s.Definition + " is written to a value outside anything the plant can survive",
				LocalEffect: itemOf(a) + " acts on the value as given",
				EndEffect:   namesOf(c.Downstream(s)),
				EffectClass: "loss-of-control", CauseClass: "model-omission",
				DetectionClass: "no-constraint",
				Cause: "The service declares a unit and a quantity kind but no permitted range, " +
					"so nothing rejects an implausible value",
				Detection: "None. The write succeeds and the resulting behaviour looks like ordinary operation",
				Action:    "Declare a Range detail on the service and refuse writes outside it",
				Evidence:  s.IRI + " declares alc:hasMethods PUT or POST and no alc:hasRange",
			})
		}
	}
	return out
}

// A dependence on a value that is polled rather than followed. The consumer
// cannot tell a value that has not changed from one that stopped arriving,
// which is the difference between a steady room and a dead sensor.
func checkPolledDependence(c *Cloud) []*Finding {
	var out []*Finding
	for _, a := range c.Assets {
		for _, cons := range a.Consumes {
			if cons.Target == nil || cons.Target.Subscribable {
				continue
			}
			// Only a value being read can be stale. A consumer in "set" mode is
			// driving that service, not believing it, and an instruction cannot
			// be out of date in the way a reading can.
			if cons.Mode == "set" {
				continue
			}
			out = append(out, &Finding{
				Block: blockOf(a), Item: itemOf(a) + " / " + cons.Definition,
				Function:    "Act on a current " + cons.Definition,
				FailureMode: "A stale value is served as though it were fresh",
				LocalEffect: itemOf(a) + " acts on a reading older than it believes",
				EndEffect:   namesOf(c.Downstream(cons.Target)),
				EffectClass: "silent-wrong-control", CauseClass: "upstream-stalled",
				DetectionClass: "no-staleness-signal",
				Cause: "The provider is polled rather than followed, so a reading that stopped " +
					"being updated is indistinguishable from one that has not changed",
				Detection: "None. The value carries no age, and registration expiry governs the " +
					"record rather than the reading",
				Action: "Make the service subscribable so a heartbeat bounds its silence, or carry " +
					"a sample timestamp in the payload",
				Evidence: cons.Target.IRI + " afo:isSubscribable false",
			})
		}
	}
	return out
}

// One sensor driving control in more than one place. Whichever room lacks the
// sensor is regulated to another room's temperature, which is not a fault
// anything can detect: both loops behave perfectly.
func checkSharedSensorAcrossLocations(c *Cloud) []*Finding {
	var out []*Finding
	for _, a := range c.Assets {
		for _, s := range a.Provides {
			locations := map[string]bool{}
			for _, cons := range s.Consumers {
				if cons.Owner != nil && cons.Owner.Location != "" {
					locations[cons.Owner.Location] = true
				}
			}
			if len(locations) < 2 {
				continue
			}
			where := make([]string, 0, len(locations))
			for l := range locations {
				where = append(where, localName(l))
			}
			sort.Strings(where)
			out = append(out, &Finding{
				Block: blockOf(a), Item: itemOf(a) + " / " + s.Definition,
				Function:    "Provide " + s.Definition + " for control",
				FailureMode: "One source drives control in " + strings.Join(where, " and "),
				LocalEffect: "Every consumer regulates to the same value regardless of where it is",
				EndEffect: "The places without the sensor are held at another place's condition. " +
					"Both loops behave correctly and one room is wrong",
				EffectClass: "wrong-by-design", CauseClass: "architectural-choice",
				DetectionClass: "graph-only",
				Cause:          "No dedicated provider was deployed for each location",
				Detection:      "Visible in the graph; nothing at runtime compares a controller's location with its source's",
				Action:         "Deploy a provider per location, or state that the locations are thermally coupled",
				Evidence:       s.IRI + " is consumed by assets whose afo:hasFunctionalLocation differ: " + strings.Join(where, ", "),
			})
		}
	}
	return out
}

// The same room named two ways. Nothing can pair a consumer with a provider
// when one says alc:Kitchen and the other says "Kitchen" — and the authorizer's
// location pairing is exactly such a comparison, so this is not cosmetic.
func checkLocationVocabulary(c *Cloud) []*Finding {
	byName := map[string]map[bool]bool{}
	for _, a := range c.Assets {
		if a.Location == "" {
			continue
		}
		name := strings.TrimSpace(localName(a.Location))
		if byName[name] == nil {
			byName[name] = map[bool]bool{}
		}
		byName[name][a.LocationIsIRI] = true
	}

	var out []*Finding
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(byName[name]) < 2 {
			continue
		}
		out = append(out, &Finding{
			Block: "Model", Item: "Functional location " + name,
			Function:    "Identify where an asset is",
			FailureMode: name + " is written both as an IRI and as a literal",
			LocalEffect: "Two spellings of one place, which no comparison treats as equal",
			EndEffect: "Any rule that pairs assets by location silently fails to pair these. " +
				"The authorizer's must_match_attribute is such a rule",
			EffectClass: "silent-rule-failure", CauseClass: "model-inconsistency",
			DetectionClass: "graph-only",
			Cause:          "Systems were configured independently and no convention was enforced",
			Detection:      "Nothing validates location vocabulary across systems",
			Action:         "Settle on the IRI form and migrate the literals",
			Evidence:       "afo:hasFunctionalLocation for " + name + " appears with both an IRI and a literal object",
		})
	}
	return out
}

// A location literal that is not what its author typed: a trailing space, or a
// character that has been through the wrong encoding. Both compare unequal to
// the clean form, and neither is visible when read.
func checkLocationLiteral(c *Cloud) []*Finding {
	var out []*Finding
	seen := map[string]bool{}
	for _, a := range c.Assets {
		if a.Location == "" || a.LocationIsIRI || seen[a.Location] {
			continue
		}
		problem := literalProblem(a.Location)
		if problem == "" {
			continue
		}
		seen[a.Location] = true
		out = append(out, &Finding{
			Block: "Model", Item: itemOf(a),
			Function:    "Identify where the asset is",
			FailureMode: "The declared location " + problem,
			LocalEffect: "The value does not compare equal to the same name written cleanly",
			EndEffect: "Location-based pairing and grouping put this asset on its own, and the " +
				"defect is invisible to anyone reading the value",
			EffectClass: "silent-rule-failure", CauseClass: "configuration-typo",
			DetectionClass: "no-validation",
			Cause:          "The literal was never trimmed or was re-encoded through the wrong character set",
			Detection:      "Nothing validates literal values at registration or at graph assembly",
			Action:         "Correct the configuration and validate location literals when they are read",
			Evidence:       "afo:hasFunctionalLocation \"" + a.Location + "\" on " + a.IRI,
		})
	}
	return out
}

// literalProblem names what is wrong with a location string, or returns "".
func literalProblem(s string) string {
	if strings.TrimSpace(s) != s {
		return "carries leading or trailing whitespace"
	}
	// Mojibake: the tell-tale of UTF-8 read as Latin-1 and re-encoded is a
	// capital A-with-diacritic immediately before another non-ASCII byte.
	for i, r := range s {
		if r == 'Ã' || r == 'Â' {
			return "appears to have been decoded with the wrong character set"
		}
		if r > unicode.MaxASCII && i == 0 {
			continue
		}
	}
	return ""
}

// Every system on one machine. No amount of service redundancy survives it,
// and it is the failure most likely to be discovered by a power cut rather than
// by an analysis.
func checkSingleHost(c *Cloud) []*Finding {
	if len(c.Hosts) != 1 {
		return nil
	}
	for host, systems := range c.Hosts {
		if len(systems) < 2 {
			return nil
		}
		sort.Strings(systems)
		return []*Finding{{
			Block: "Platform", Item: "Host " + host,
			Function:    "Run the local cloud",
			FailureMode: "The host stops",
			LocalEffect: "Every system stops at once: " + strings.Join(systems, ", "),
			EndEffect: "The whole cloud is gone — control, supervision and the means of " +
				"noticing, together. Nothing inside can report it",
			EffectClass: "total-loss", CauseClass: "host-failure",
			DetectionClass: "unobservable-from-within",
			Cause:          "All " + fmt.Sprint(len(systems)) + " systems declare afo:runsOnHost " + host,
			Detection:      "None from inside the cloud. An outside observer is required",
			Action:         "Distribute the core systems across hosts, or accept the exposure and monitor from outside",
			Evidence:       "every afo:Husk in the graph names host " + host,
		}}
	}
	return nil
}

// dependents is the immediate consumers of a service, for the local-effect
// column — one hop, where Downstream is the whole reach.
func dependents(s *Service) []*Asset {
	var out []*Asset
	seen := map[*Asset]bool{}
	for _, cons := range s.Consumers {
		if cons.Owner == nil || seen[cons.Owner] {
			continue
		}
		seen[cons.Owner] = true
		out = append(out, cons.Owner)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IRI < out[j].IRI })
	return out
}

// detectionClassOf and detectionProseOf say how a failure of this service would
// be noticed — which is a property of the model, not an opinion. A followed
// service has a heartbeat, so silence is detectable; a polled one does not.
func detectionClassOf(s *Service) string {
	if s.Subscribable {
		return "heartbeat"
	}
	return "no-staleness-signal"
}

func detectionProseOf(s *Service) string {
	if s.Subscribable {
		return "A subscriber hears nothing and can treat the publisher as gone; no alarm service aggregates that"
	}
	return "None modeled. The consumer sees a failed call; nothing aggregates it into an alarm"
}
