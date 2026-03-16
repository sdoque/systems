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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// samplePackage returns the SysML v2 text that SModeling would produce for a
// minimal single-asset system with one provided and one consumed service.
func samplePackage(pkgName, assetName, provided, consumed string) string {
	return fmt.Sprintf(`package '%s' {

    // ── Port Definitions ─────────────────────────────────────────────────────
    port def '%s';
    port def '%s';

    // ── Block Definitions (BDD) ──────────────────────────────────────────────
    part def '%sSystem' {
        attribute name : String = "%s";
        part '%s' : '%sBlock';
    }

    part def '%sBlock' {
        attribute mission : String = "test_mission";
        out port '%s' : ~'%s';  // provided service
        in port '%s' : '%s';  // consumed service
    }

    // ── Internal Block Diagram (IBD) ─────────────────────────────────────────
    part '%s' : '%sSystem' {
        attribute host : String = "testhost";
        attribute ipAddress : String = "127.0.0.1";
        attribute httpPort : Integer = 9000;

        part '%s' : '%sBlock' {
            // provides: http://127.0.0.1:9000/%s/%s/%s
        }
    }

}
`,
		pkgName,
		provided, consumed,
		assetName, assetName, assetName, assetName,
		assetName, provided, provided, consumed, consumed,
		assetName, assetName,
		assetName, assetName,
		assetName, assetName, provided,
	)
}

// systemRecordListJSON serialises a SystemRecordList_v1 whose List contains the
// given URLs and returns the JSON bytes.
func systemRecordListJSON(urls []string) []byte {
	var sl forms.SystemRecordList_v1
	sl.NewForm()
	sl.List = urls
	b, err := json.Marshal(sl)
	if err != nil {
		panic(err)
	}
	return b
}

// ── extractPortDefName ────────────────────────────────────────────────────────

func TestExtractPortDefName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal indented line", "    port def 'temperature';", "temperature"},
		{"no indentation", "port def 'rotation';", "rotation"},
		{"extra spaces around name", "    port def '  setpoint  ';", "setpoint"},
		{"not a port def", "    part def 'thermostatBlock' {", ""},
		{"empty string", "", ""},
		{"comment line", "    // port def 'ignored';", ""},
		{"multi-word definition", "    port def 'pumpSpeed';", "pumpSpeed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPortDefName(tc.input)
			if got != tc.want {
				t.Errorf("extractPortDefName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ── extractPackageContent ─────────────────────────────────────────────────────

func TestExtractPackageContent(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantEmpty  bool
		wantContains string
	}{
		{
			name:         "normal package",
			input:        "package 'test' {\n    port def 'x';\n}",
			wantContains: "port def 'x'",
		},
		{
			name:      "no opening brace",
			input:     "package 'test'",
			wantEmpty: true,
		},
		{
			name:      "empty string",
			input:     "",
			wantEmpty: true,
		},
		{
			name:      "closing before opening returns empty",
			input:     "} something {",
			wantEmpty: true,
		},
		{
			name:         "nested braces preserve inner content",
			input:        "package 'p' {\n    part def 'A' {\n        out port 'x';\n    }\n}",
			wantContains: "part def 'A'",
		},
		{
			name:      "only opening brace",
			input:     "package 'p' {",
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPackageContent(tc.input)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("expected content to contain %q, got %q", tc.wantContains, got)
			}
		})
	}
}

// ── extractBlock ──────────────────────────────────────────────────────────────

func TestExtractBlock(t *testing.T) {
	t.Run("single-level block", func(t *testing.T) {
		lines := []string{
			"    part def 'A' {",
			"        out port 'x';",
			"    }",
			"    port def 'y';",
		}
		block, consumed := extractBlock(lines, 0)
		if consumed != 3 {
			t.Errorf("consumed = %d, want 3", consumed)
		}
		if !strings.Contains(block, "out port 'x'") {
			t.Errorf("block missing expected content: %q", block)
		}
		if strings.Contains(block, "port def 'y'") {
			t.Error("block should not include the line after the closing brace")
		}
	})

	t.Run("nested block", func(t *testing.T) {
		lines := []string{
			"    part 'sys' : 'sysBlock' {",
			"        attribute host : String = \"h\";",
			"        part 'a' : 'aBlock' {",
			"            // provides: http://...",
			"        }",
			"    }",
			"    // trailing",
		}
		block, consumed := extractBlock(lines, 0)
		if consumed != 6 {
			t.Errorf("consumed = %d, want 6", consumed)
		}
		if !strings.Contains(block, "part 'a'") {
			t.Errorf("nested block content missing: %q", block)
		}
	})

	t.Run("start mid-slice", func(t *testing.T) {
		lines := []string{
			"    port def 'x';",
			"    part def 'B' {",
			"        in port 'y';",
			"    }",
		}
		block, consumed := extractBlock(lines, 1)
		if consumed != 3 {
			t.Errorf("consumed = %d, want 3", consumed)
		}
		if !strings.Contains(block, "in port 'y'") {
			t.Errorf("unexpected block content: %q", block)
		}
	})

	t.Run("unclosed block returns all remaining lines", func(t *testing.T) {
		lines := []string{
			"    part def 'C' {",
			"        out port 'z';",
		}
		_, consumed := extractBlock(lines, 0)
		if consumed != 2 {
			t.Errorf("consumed = %d, want 2", consumed)
		}
	})
}

// ── parsePackage ──────────────────────────────────────────────────────────────

func TestParsePackage(t *testing.T) {
	t.Run("full realistic package", func(t *testing.T) {
		input := samplePackage("myhost_thermostat", "controller", "setpoint", "temperature")
		portDefs, blockDefs, ibdParts := parsePackage(input)

		if len(portDefs) != 2 {
			t.Errorf("portDefs count = %d, want 2", len(portDefs))
		}
		if len(blockDefs) != 2 {
			t.Errorf("blockDefs count = %d, want 2 (system + asset)", len(blockDefs))
		}
		if len(ibdParts) != 1 {
			t.Errorf("ibdParts count = %d, want 1", len(ibdParts))
		}

		// port defs contain the right names
		combined := strings.Join(portDefs, "\n")
		if !strings.Contains(combined, "'setpoint'") {
			t.Error("portDefs missing 'setpoint'")
		}
		if !strings.Contains(combined, "'temperature'") {
			t.Error("portDefs missing 'temperature'")
		}

		// block defs contain provided/consumed ports
		blockText := strings.Join(blockDefs, "\n")
		if !strings.Contains(blockText, "out port 'setpoint'") {
			t.Error("blockDefs missing provided port")
		}
		if !strings.Contains(blockText, "in port 'temperature'") {
			t.Error("blockDefs missing consumed port")
		}
		if !strings.Contains(blockText, "mission") {
			t.Error("blockDefs missing mission attribute")
		}

		// IBD part carries host metadata
		if !strings.Contains(ibdParts[0], "testhost") {
			t.Error("IBD part missing host attribute")
		}
	})

	t.Run("empty package body", func(t *testing.T) {
		input := "package 'empty' {\n}\n"
		portDefs, blockDefs, ibdParts := parsePackage(input)
		if len(portDefs)+len(blockDefs)+len(ibdParts) != 0 {
			t.Errorf("expected all empty, got pd=%d bd=%d ibd=%d", len(portDefs), len(blockDefs), len(ibdParts))
		}
	})

	t.Run("only port defs", func(t *testing.T) {
		input := "package 'p' {\n    port def 'a';\n    port def 'b';\n}\n"
		portDefs, blockDefs, ibdParts := parsePackage(input)
		if len(portDefs) != 2 {
			t.Errorf("portDefs = %d, want 2", len(portDefs))
		}
		if len(blockDefs) != 0 || len(ibdParts) != 0 {
			t.Error("expected no blockDefs or ibdParts")
		}
	})

	t.Run("comment and blank lines are skipped", func(t *testing.T) {
		input := "package 'p' {\n\n    // a comment\n\n    port def 'x';\n\n}\n"
		portDefs, _, _ := parsePackage(input)
		if len(portDefs) != 1 {
			t.Errorf("portDefs = %d, want 1", len(portDefs))
		}
	})

	t.Run("invalid text returns empty", func(t *testing.T) {
		portDefs, blockDefs, ibdParts := parsePackage("not sysml at all")
		if len(portDefs)+len(blockDefs)+len(ibdParts) != 0 {
			t.Error("expected all empty for invalid input")
		}
	})

	t.Run("two systems share a port def name", func(t *testing.T) {
		// parsePackage itself does not deduplicate — that is assembleModel's job.
		// Both packages contain 'temperature'; each call returns it independently.
		pkg1 := samplePackage("host_sys1", "asset1", "temperature", "rotation")
		pkg2 := samplePackage("host_sys2", "asset2", "temperature", "level")
		pd1, _, _ := parsePackage(pkg1)
		pd2, _, _ := parsePackage(pkg2)
		count := 0
		for _, l := range append(pd1, pd2...) {
			if extractPortDefName(l) == "temperature" {
				count++
			}
		}
		if count != 2 {
			t.Errorf("expected 'temperature' to appear in both parses, count = %d", count)
		}
	})
}

// ── assembleModel (integration with mock HTTP servers) ────────────────────────

// newMockRegistrar starts a test HTTP server that behaves like a minimal service
// registrar: GET /status reports it is the lead registrar, and GET /syslist
// returns the provided JSON body.
func newMockRegistrar(sysListJSON []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			// GetRunningCoreSystemURL checks that the response starts with this
			// string to identify the lead registrar.
			fmt.Fprint(w, components.ServiceRegistrarLeader+" 2026-01-01T00:00:00Z")
		case strings.HasSuffix(r.URL.Path, "/syslist"):
			w.Header().Set("Content-Type", "application/json")
			w.Write(sysListJSON)
		default:
			http.NotFound(w, r)
		}
	}))
}

// newTestTraits wires a Traits struct to a mock service-registrar URL so that
// assembleModel can resolve the registrar without a real Arrowhead deployment.
func newTestTraits(registrarURL, cloudName string) *Traits {
	sys := components.NewSystem("test", nil)
	sys.Husk = &components.Husk{
		CoreS: []*components.CoreSystem{
			{
				Name: components.ServiceRegistrarName,
				Url:  registrarURL,
			},
		},
	}
	return &Traits{
		CloudName: cloudName,
		owner:     &sys,
		name:      "assembler",
	}
}

func TestAssembleModel_SingleSystem(t *testing.T) {
	// Fake /smodel response for one system.
	pkg := samplePackage("host_thermostat", "controller", "setpoint", "temperature")
	smodelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, pkg)
	}))
	defer smodelSrv.Close()

	// Fake service-registrar /syslist response pointing at the smodel server.
	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{smodelSrv.URL}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "testCloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "package 'testCloud'") {
		t.Errorf("output should start with package declaration, got: %q", body[:min(80, len(body))])
	}
	if !strings.Contains(body, "port def 'setpoint'") {
		t.Error("merged output missing port def 'setpoint'")
	}
	if !strings.Contains(body, "part def 'controllerBlock'") {
		t.Error("merged output missing block def for controller")
	}
	if !strings.Contains(body, "part 'controller'") {
		t.Error("merged output missing IBD part")
	}
}

func TestAssembleModel_DeduplicatesPortDefs(t *testing.T) {
	// Two systems both provide/consume 'temperature' — port def must appear once.
	pkg1 := samplePackage("host_sys1", "asset1", "temperature", "level")
	pkg2 := samplePackage("host_sys2", "asset2", "pressure", "temperature")

	handler := func(pkg string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, pkg)
		}
	}
	srv1 := httptest.NewServer(handler(pkg1))
	defer srv1.Close()
	srv2 := httptest.NewServer(handler(pkg2))
	defer srv2.Close()

	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{srv1.URL, srv2.URL}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "cloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	body := w.Body.String()
	count := strings.Count(body, "port def 'temperature'")
	if count != 1 {
		t.Errorf("'temperature' port def should appear exactly once, found %d times", count)
	}
	// Other port defs should be present too.
	if !strings.Contains(body, "port def 'level'") {
		t.Error("missing port def 'level'")
	}
	if !strings.Contains(body, "port def 'pressure'") {
		t.Error("missing port def 'pressure'")
	}
	// Both system block defs should appear.
	if !strings.Contains(body, "part def 'asset1Block'") {
		t.Error("missing block def for asset1")
	}
	if !strings.Contains(body, "part def 'asset2Block'") {
		t.Error("missing block def for asset2")
	}
}

func TestAssembleModel_MultipleSystemsMergeIBD(t *testing.T) {
	pkg1 := samplePackage("host_a", "sensorA", "tempA", "none")
	pkg2 := samplePackage("host_b", "sensorB", "tempB", "none")

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pkg1)
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pkg2)
	}))
	defer srv2.Close()

	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{srv1.URL, srv2.URL}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "myCloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	body := w.Body.String()
	if !strings.Contains(body, "part 'sensorA'") {
		t.Error("missing IBD part for sensorA")
	}
	if !strings.Contains(body, "part 'sensorB'") {
		t.Error("missing IBD part for sensorB")
	}
}

func TestAssembleModel_SkipsUnreachableSystem(t *testing.T) {
	// One reachable system, one URL that will refuse connections.
	pkg := samplePackage("host_ok", "okAsset", "okService", "none")
	reachableSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pkg)
	}))
	defer reachableSrv.Close()

	// Use the address of a closed server to guarantee a connection failure.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close() // immediately close so requests to it fail

	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{reachableSrv.URL, deadURL}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "partialCloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	// Should still return 200 with the reachable system's content.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "port def 'okService'") {
		t.Error("missing content from reachable system")
	}
}

func TestAssembleModel_RegistrarUnreachable(t *testing.T) {
	// Point to a dead registrar.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadSrv.URL
	deadSrv.Close()

	tr := newTestTraits(deadURL, "cloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	if w.Code != http.StatusInternalServerError && w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 5xx status, got %d", w.Code)
	}
}

func TestAssembleModel_EmptySystemList(t *testing.T) {
	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "emptyCloud")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "package 'emptyCloud'") {
		t.Errorf("expected empty package, got: %q", body)
	}
	// No port defs, block defs, or IBD parts — just the package shell.
	if strings.Contains(body, "port def") || strings.Contains(body, "part def") || strings.Contains(body, "part '") {
		t.Error("empty system list should produce an empty package body")
	}
}

func TestAssembleModel_CustomCloudName(t *testing.T) {
	registrarSrv := newMockRegistrar(systemRecordListJSON([]string{}))
	defer registrarSrv.Close()

	tr := newTestTraits(registrarSrv.URL, "myFactory_2026")
	w := httptest.NewRecorder()
	tr.assembleModel(w)

	if !strings.HasPrefix(w.Body.String(), "package 'myFactory_2026'") {
		t.Errorf("package name not honoured, got: %q", w.Body.String()[:min(60, w.Body.Len())])
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
