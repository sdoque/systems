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

// The picture, as data. What the page draws and nothing more.

package main

import (
	"net/url"
	"sort"
	"strings"
)

// Cloud is everything the painter knows, ready to be drawn.
//
// A picture rather than a knowledge base. It carries what an operator needs to
// see at a glance — who is here, where, how secure, and what depends on what —
// and it carries nothing that would only be useful to a query, because queries
// belong to the kgrapher and the triple store behind it.
type Cloud struct {
	Name  string   `json:"name"`
	Hosts []*Host  `json:"hosts"`
	Links []*Link  `json:"links"`
	Notes []string `json:"notes,omitempty"`
}

// Host is one machine, and the systems running on it.
type Host struct {
	Name    string    `json:"name"`
	IPs     []string  `json:"ips,omitempty"`
	Systems []*System `json:"systems"`
}

// System is one Arrowhead system as it describes itself.
type System struct {
	Name    string   `json:"name"`
	Level   string   `json:"level"` // open, enrolling, identified, authorized
	Posture []string `json:"posture,omitempty"`
	Assets  []*Asset `json:"assets"`
	// Doc is where a person can read this system's own description of itself.
	//
	// Derived from a service URL rather than configured, because the graph
	// already carries the addresses and a second place to record them is a
	// second place to get them wrong. The plaintext one: a browser holds no
	// client certificate, so the https endpoint would refuse it at the
	// handshake.
	Doc string `json:"doc,omitempty"`
}

// Asset is a unit asset: the thing a system actually does something with.
type Asset struct {
	Name     string   `json:"name"`
	Mission  string   `json:"mission,omitempty"`
	Provides []*Offer `json:"provides,omitempty"`
	Wants    []*Want  `json:"wants,omitempty"`
}

// Offer is a service an asset provides.
//
// The picture does not need this to draw a line — a line is found by matching a
// consumer's URL to whoever answers it — but a person clicking on a system does.
// "What does this offer, and what does it ask for" is the pair that says whether
// a system is doing its job, and half a pair answers nothing.
type Offer struct {
	Definition string `json:"definition"`
	Mission    string `json:"mission,omitempty"`
}

// Want is a service an asset consumes, and whether anything provides it.
//
// Satisfied is false when the asset asked for a service and discovery has not
// found one. That is the state worth showing on its own: a cloud where every
// system is running and one controller is quietly reaching for a sensor nobody
// provides looks entirely healthy from every other angle.
type Want struct {
	Definition string `json:"definition"`
	Satisfied  bool   `json:"satisfied"`
	ProviderID string `json:"providerId,omitempty"`
}

// Link is a line to draw: one asset consuming a service another asset provides.
type Link struct {
	From       string `json:"from"` // asset id, the consumer
	To         string `json:"to"`   // asset id, the provider
	Definition string `json:"definition"`
	Mission    string `json:"mission,omitempty"`
	// Action is what the consumer does on this line — read, write or invoke.
	//
	// It is not the provider's mission, and the difference is the whole point.
	// A line used to be drawn as acting whenever the thing at the far end was an
	// actuator, so a collector logging a valve position looked exactly like the
	// controller driving it. Reading an actuator is observation; only the writer
	// acts. The picture has to say which, because an operator scanning it for
	// what can move the plant is reading those dashes as the answer.
	Action string `json:"action,omitempty"`
}

// actionForMode mirrors usecases.ActionForMode. A cervice that declares no mode
// reads, which is what the framework assumes when it mints the token.
func actionForMode(mode string) string {
	switch mode {
	case "set":
		return "write"
	case "do":
		return "invoke"
	default:
		return "read"
	}
}

// docURL turns any plaintext service URL of a system into the address of that
// system's own documentation page. It returns "" for an https URL, so a system
// serving only TLS simply has no link rather than a broken one.
func docURL(rawURL, systemName string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/" + systemName + "/doc"
}

// assetID names an asset the same way everywhere, so a link's ends can be found
// on the page without searching by three separate names.
func assetID(host, system, asset string) string {
	return host + "/" + system + "/" + asset
}

// provided is one service somebody offers, kept only long enough to turn a
// consumer's URL into the asset at the other end of the line.
type provided struct {
	assetID    string
	definition string
	mission    string
}

// build turns each system's self-description into one picture.
//
// The graphs arrive per system because that is how the cloud stores what it
// knows: no system holds the whole truth, and the painter is the place the
// separate accounts are put side by side. A system whose graph could not be
// read at all is still drawn — with a note — because an operator looking for a
// system needs to see that it is there.
func build(fallbackName string, graphs map[string]string) *Cloud {
	cloud := &Cloud{}

	// What the cloud is called is something the systems say, not something this
	// one knows. Each carries the name in its own configuration and states it as
	// afo:isContainedIn, so the painter reports what the cloud says about itself
	// rather than what the painter was told — and a system configured into a
	// different cloud than the rest is then visible instead of silently drawn as
	// though it belonged.
	claimed := map[string]int{}

	byHost := map[string]*Host{}
	offered := map[string]provided{} // service URL -> what is behind it
	type pendingWant struct {
		asset  *Asset
		want   *Want
		url    string
		action string
	}
	var pending []pendingWant

	names := make([]string, 0, len(graphs))
	for name := range graphs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		facts := readTurtle(graphs[name])

		for _, sysSubject := range subjectsOfType(facts, "afo:System") {
			if declared := localName(object(facts, sysSubject, "afo:isContainedIn")); declared != "" {
				claimed[declared]++
			}
			sysName := object(facts, sysSubject, "afo:hasName")
			if sysName == "" {
				sysName = localName(sysSubject)
			}

			hostSubject := ""
			for _, huskSubject := range objects(facts, sysSubject, "afo:hasHusk") {
				hostSubject = object(facts, huskSubject, "afo:runsOnHost")
			}
			hostName := object(facts, hostSubject, "afo:hasName")
			if hostName == "" {
				hostName = localName(hostSubject)
			}
			if hostName == "" {
				hostName = "unknown host"
			}

			host := byHost[hostName]
			if host == nil {
				host = &Host{Name: hostName, IPs: objects(facts, hostSubject, "afo:hasIPAddress")}
				byHost[hostName] = host
			}

			system := &System{Name: sysName}
			postureSubject := object(facts, sysSubject, "afo:hasSecurityPosture")
			system.Level = object(facts, postureSubject, "afo:hasSecurityLevel")
			for _, flag := range []string{
				"namesCertificateAuthority", "namesAuthorizer", "isIdentified",
				"canVerifyPeers", "verifiesTokens", "offersTLS", "acceptsPlaintext",
			} {
				if object(facts, postureSubject, "afo:"+flag) == "true" {
					system.Posture = append(system.Posture, flag)
				}
			}

			for _, assetSubject := range objects(facts, sysSubject, "afo:hasUnitAsset") {
				asset := &Asset{
					Name:    object(facts, assetSubject, "afo:hasName"),
					Mission: object(facts, assetSubject, "afo:hasMission"),
				}
				if asset.Name == "" {
					asset.Name = localName(assetSubject)
				}
				id := assetID(hostName, sysName, asset.Name)

				// What this asset offers, remembered by URL so a consumer
				// elsewhere can be joined to it.
				for _, svcSubject := range objects(facts, assetSubject, "afo:providesService") {
					def := object(facts, svcSubject, "afo:hasServiceDefinition")
					mission := object(facts, svcSubject, "afo:hasMission")
					if mission == "" {
						mission = asset.Mission
					}
					// Once per service, not once per URL: a service reachable
					// over both http and https is one offer, and listing it
					// twice would read as two.
					asset.Provides = append(asset.Provides, &Offer{Definition: def, Mission: mission})
					for _, rawURL := range objects(facts, svcSubject, "afo:hasUrl") {
						offered[rawURL] = provided{assetID: id, definition: def, mission: mission}
						if system.Doc == "" {
							system.Doc = docURL(rawURL, sysName)
						}
					}
				}

				// What it asks for, and whether it found it.
				for _, cerviceSubject := range objects(facts, assetSubject, "afo:consumesService") {
					want := &Want{Definition: wantedDefinition(facts, cerviceSubject)}
					urls := objects(facts, cerviceSubject, "alc:fromUrl")
					want.Satisfied = len(urls) > 0
					asset.Wants = append(asset.Wants, want)
					action := actionForMode(object(facts, cerviceSubject, "alc:hasMode"))
					for _, url := range urls {
						pending = append(pending, pendingWant{asset: asset, want: want, url: url, action: action})
					}
				}

				system.Assets = append(system.Assets, asset)
			}

			host.Systems = append(host.Systems, system)
		}
	}

	// The lines, drawn once both ends are known. A consumer bound to a provider
	// this painter never reached is left unresolved rather than pointed at
	// nothing: the want stays satisfied, because it is, and the line is simply
	// not drawn.
	for _, p := range pending {
		target, known := offered[p.url]
		if !known {
			// The URL a consumer was bound to says who provides the service, in
			// its own path: /<system>/<asset>/<subpath>. Falling back to reading
			// it means a line is still drawn when the two ends spell the same
			// endpoint differently — a provider that published its address over
			// one protocol while the consumer was orchestrated to the other, or a
			// host whose several addresses were enumerated in a different order
			// than when it registered.
			target, known = byPath(byHost, p.url)
			if !known {
				continue
			}
		}
		p.want.ProviderID = target.assetID
		from := assetOwnerID(byHost, p.asset)
		if from == "" || from == target.assetID {
			continue
		}
		cloud.Links = append(cloud.Links, &Link{
			From:       from,
			To:         target.assetID,
			Definition: firstNonEmpty(p.want.Definition, target.definition),
			Mission:    target.mission,
			Action:     p.action,
		})
	}

	cloud.Name = agreedName(claimed, fallbackName)
	if len(claimed) > 1 {
		// Systems that disagree about which cloud they are in. Worth saying out
		// loud: it is a configuration mistake that looks like nothing at all,
		// and the painter is where it becomes apparent.
		var names []string
		for name := range claimed {
			names = append(names, name)
		}
		sort.Strings(names)
		cloud.Notes = append(cloud.Notes,
			"systems disagree about the cloud they are in: "+strings.Join(names, ", "))
	}

	for _, host := range byHost {
		sort.Slice(host.Systems, func(i, j int) bool { return host.Systems[i].Name < host.Systems[j].Name })
		cloud.Hosts = append(cloud.Hosts, host)
	}
	sort.Slice(cloud.Hosts, func(i, j int) bool { return cloud.Hosts[i].Name < cloud.Hosts[j].Name })
	sort.Slice(cloud.Links, func(i, j int) bool {
		if cloud.Links[i].From != cloud.Links[j].From {
			return cloud.Links[i].From < cloud.Links[j].From
		}
		return cloud.Links[i].Definition < cloud.Links[j].Definition
	})
	return cloud
}

// agreedName is the name most systems give the cloud they are in.
//
// Most rather than all, because one system configured into the wrong cloud
// should not rename the cloud for everyone else — and because a cloud where no
// system declares one at all still has to be called something.
func agreedName(claimed map[string]int, fallback string) string {
	best, bestCount := "", 0
	for name, count := range claimed {
		if count > bestCount || (count == bestCount && name < best) {
			best, bestCount = name, count
		}
	}
	if best != "" {
		return best
	}
	return fallback
}

// wantedDefinition returns the service definition an asset asked for.
//
// A consumed service states afo:consumes more than once: the definition it
// wants as a literal, and the provider it found as a reference. The literal is
// the one that survives when nothing was found, which is exactly the case worth
// drawing.
func wantedDefinition(facts []statement, cervice string) string {
	for _, candidate := range objects(facts, cervice, "afo:consumes") {
		if !strings.Contains(candidate, ":") {
			return candidate
		}
	}
	return localName(object(facts, cervice, "afo:consumes"))
}

// byPath finds a provider from the shape of the URL a consumer was bound to.
//
// An Arrowhead endpoint spells out what is behind it — the system, then the unit
// asset, then the service — so the path alone identifies the provider even when
// the address in front of it does not match anything published.
func byPath(byHost map[string]*Host, rawURL string) (provided, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return provided{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return provided{}, false
	}
	systemName, assetName := parts[0], parts[1]

	for _, host := range byHost {
		for _, system := range host.Systems {
			if system.Name != systemName {
				continue
			}
			for _, asset := range system.Assets {
				if asset.Name != assetName {
					continue
				}
				return provided{
					assetID: assetID(host.Name, system.Name, asset.Name),
					mission: asset.Mission,
				}, true
			}
		}
	}
	return provided{}, false
}

// assetOwnerID finds the identifier of an asset already placed under a host.
func assetOwnerID(byHost map[string]*Host, want *Asset) string {
	for _, host := range byHost {
		for _, system := range host.Systems {
			for _, asset := range system.Assets {
				if asset == want {
					return assetID(host.Name, system.Name, asset.Name)
				}
			}
		}
	}
	return ""
}

// localName strips a prefix from a reference, leaving the part a person reads.
func localName(reference string) string {
	if i := strings.LastIndexAny(reference, ":/#"); i >= 0 {
		return reference[i+1:]
	}
	return reference
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
