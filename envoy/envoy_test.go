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
 ***************************************************************************SDG*/

package main

import (
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
)

func node(url string, details map[string][]string) components.NodeInfo {
	return components.NodeInfo{URL: url, Details: details}
}

// TestACaptureIsNamedAfterWhereItCameFrom: a directory of captures has to be
// readable without opening any of them, so the system, the asset and the
// service all belong in the name.
func TestACaptureIsNamedAfterWhereItCameFrom(t *testing.T) {
	n := node("https://192.168.1.109:30105/kgrapher/assembler/cloudgraph",
		map[string][]string{"Format": {"Turtle"}})
	got := filename("cloudgraph", n, "text/turtle", false)
	if got != "kgrapher-assembler-cloudgraph.ttl" {
		t.Errorf("filename = %q; want kgrapher-assembler-cloudgraph.ttl", got)
	}
}

func TestATimestampIsAddedWhenAsked(t *testing.T) {
	n := node("https://h:30106/modeler/assembler/cloudmodel", map[string][]string{"Format": {"SysML v2"}})
	got := filename("cloudmodel", n, "text/plain", true)
	if !strings.HasPrefix(got, "modeler-assembler-cloudmodel-") || !strings.HasSuffix(got, ".sysml") {
		t.Errorf("filename = %q; want modeler-assembler-cloudmodel-<stamp>.sysml", got)
	}
	// Two captures of the same service must not overwrite each other, which is
	// the whole reason the stamp exists.
	if plain := filename("cloudmodel", n, "text/plain", false); plain == got {
		t.Error("the timestamped and untimestamped names are identical")
	}
}

// TestTheRegisteredFormatBeatsTheContentType: a provider registers "Turtle"
// because a person wrote it there, while the Content-Type is often just
// text/plain. The more considered answer should win.
func TestTheRegisteredFormatBeatsTheContentType(t *testing.T) {
	n := node("https://h:30105/kgrapher/assembler/cloudgraph", map[string][]string{"Format": {"Turtle"}})
	if got := extensionFor(n, "text/plain"); got != ".ttl" {
		t.Errorf("extension = %q; want .ttl", got)
	}
}

func TestTheContentTypeIsUsedWhenNothingWasRegistered(t *testing.T) {
	n := node("https://h/x/y/z", nil)
	for ctype, want := range map[string]string{
		"text/turtle":              ".ttl",
		"application/json":         ".json",
		"text/html; charset=utf-8": ".html",
		"application/xml":          ".xml",
		"application/octet-stream": ".txt",
	} {
		if got := extensionFor(n, ctype); got != want {
			t.Errorf("extensionFor(%q) = %q; want %q", ctype, got, want)
		}
	}
}

// TestAProviderCannotChooseWhereThisWrites is the one that matters for safety.
//
// The path comes off a URL handed out by the registry, and a service definition
// is a string a provider chose for itself. Without sanitizing, a definition
// containing "../" would walk out of the capture directory, and one containing a
// NUL or a slash would produce a path the operator never asked for.
func TestAProviderCannotChooseWhereThisWrites(t *testing.T) {
	hostile := []string{
		"https://h/../../etc/passwd",
		"https://h/sys/asset/../../../../tmp/evil",
		"https://h/sys/asset/serv%00.ttl",
	}
	for _, u := range hostile {
		got := filename("x", node(u, nil), "text/plain", false)
		if strings.Contains(got, "..") || strings.ContainsAny(got, `/\`) || strings.ContainsRune(got, 0) {
			t.Errorf("filename(%q) = %q — escapes the capture directory", u, got)
		}
	}
}

// TestAnUnparseableURLStillProducesAName: falling back to the definition keeps
// a capture from being dropped because its URL was odd.
func TestAnUnparseableURLStillProducesAName(t *testing.T) {
	if got := filename("cloudgraph", node("://not a url", nil), "", false); got != "cloudgraph.txt" {
		t.Errorf("filename = %q; want cloudgraph.txt", got)
	}
}

// TestTheTemplateDeclaresAMission: a subject the authorizer cannot classify is
// a subject no policy can name, and the framework refuses to start a system
// whose asset has no mission.
func TestTheTemplateDeclaresAMission(t *testing.T) {
	ua := initTemplate()
	if !components.ValidMission(ua.Mission) {
		t.Error("the template asset declares no mission")
	}
	if ua.Mission != components.MissionLogging {
		t.Errorf("mission = %q; want logging — this system keeps a record", ua.Mission)
	}
}
