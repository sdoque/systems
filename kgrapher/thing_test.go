package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePrefixed(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"foo", "alc:foo"},
		{"alc:foo", "alc:foo"},
		{"<http://x>", "<http://x>"},
	}
	for _, c := range cases {
		got := ensurePrefixed(c.input)
		if got != c.want {
			t.Errorf("ensurePrefixed(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestIsSystemBlock(t *testing.T) {
	sysBlock := "alc:MySys a afo:System ;\n    afo:isContainedIn alc:Cloud ."
	if !isSystemBlock(sysBlock) {
		t.Error("expected isSystemBlock to return true for a system block")
	}

	otherBlock := "alc:MySys a afo:Service ;\n    afo:isContainedIn alc:Cloud ."
	if isSystemBlock(otherBlock) {
		t.Error("expected isSystemBlock to return false for a non-system block")
	}
}

func TestExtractSubject(t *testing.T) {
	block := "alc:MySys a afo:System ;\n    afo:isContainedIn alc:Cloud ."
	got := extractSubject(block)
	if got != "alc:MySys" {
		t.Errorf("extractSubject = %q; want %q", got, "alc:MySys")
	}

	empty := extractSubject("")
	if empty != "" {
		t.Errorf("extractSubject(\"\") = %q; want \"\"", empty)
	}
}

func TestExtractContainedIns(t *testing.T) {
	block := "alc:MySys a afo:System ;\n    afo:isContainedIn alc:Cloud ."
	got := extractContainedIns(block)
	if len(got) != 1 || got[0] != "alc:Cloud" {
		t.Errorf("extractContainedIns = %v; want [alc:Cloud]", got)
	}

	noBlock := "alc:MySys a afo:System ."
	none := extractContainedIns(noBlock)
	if len(none) != 0 {
		t.Errorf("extractContainedIns with no containedIn = %v; want nil/empty", none)
	}
}

func TestInjectContainedIn(t *testing.T) {
	block := "alc:MySys a afo:System ."
	got := injectContainedIn(block, "alc:Cloud")
	if !strings.Contains(got, "afo:isContainedIn") {
		t.Errorf("injectContainedIn did not inject isContainedIn: %q", got)
	}

	// Already has isContainedIn — should be unchanged
	already := "alc:MySys a afo:System ;\n    afo:isContainedIn alc:Cloud ."
	unchanged := injectContainedIn(already, "alc:OtherCloud")
	if unchanged != already {
		t.Errorf("injectContainedIn modified block that already has isContainedIn")
	}
}

func TestDetectGlobalCloud(t *testing.T) {
	// Single cloud
	blocks := []string{
		"alc:MySys a afo:System ;\n    afo:isContainedIn alc:Cloud .",
		"alc:OtherSys a afo:System ;\n    afo:isContainedIn alc:Cloud .",
	}
	cloud, err := detectGlobalCloud(blocks)
	if err != nil {
		t.Fatalf("detectGlobalCloud unexpected error: %v", err)
	}
	if cloud != "alc:Cloud" {
		t.Errorf("detectGlobalCloud = %q; want %q", cloud, "alc:Cloud")
	}

	// Two different clouds — expect error
	conflicting := []string{
		"alc:MySys a afo:System ;\n    afo:isContainedIn alc:CloudA .",
		"alc:OtherSys a afo:System ;\n    afo:isContainedIn alc:CloudB .",
	}
	_, err = detectGlobalCloud(conflicting)
	if err == nil {
		t.Error("detectGlobalCloud expected error for multiple clouds, got nil")
	}

	// No system blocks — expect empty string, no error
	noSys := []string{
		"alc:Thing a afo:Service .",
	}
	cloud, err = detectGlobalCloud(noSys)
	if err != nil {
		t.Fatalf("detectGlobalCloud unexpected error for no system blocks: %v", err)
	}
	if cloud != "" {
		t.Errorf("detectGlobalCloud with no system blocks = %q; want \"\"", cloud)
	}
}

func TestExtractCloudName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"<http://ex.org/ns#AlphaCloud>", "AlphaCloud"},
		{"alc:Beta", "Beta"},
		{"", ""},
	}
	for _, c := range cases {
		got := extractCloudName(c.input)
		if got != c.want {
			t.Errorf("extractCloudName(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestUpdatePrefixes(t *testing.T) {
	prefixes := map[string]bool{
		"@prefix alc: <http://old#> .": true,
	}
	updates := map[string]string{
		"alc": "http://new",
	}
	updatePrefixes(prefixes, updates)

	// Old key should be gone; a key containing "http://new" should exist
	for k := range prefixes {
		if strings.Contains(k, "http://old#") {
			t.Errorf("old prefix still present after update: %q", k)
		}
		if strings.Contains(k, "alc") && !strings.Contains(k, "http://new") {
			t.Errorf("updated prefix does not contain new URL: %q", k)
		}
	}
}

func TestResolveLocalOntologies(t *testing.T) {
	dir := t.TempDir()
	filename := "myonto.ttl"
	fullPath := filepath.Join(dir, filename)
	if err := os.WriteFile(fullPath, []byte("# ontology"), 0644); err != nil {
		t.Fatalf("failed to create temp ontology file: %v", err)
	}

	baseURL := "http://example.com/"

	// File exists — value should become baseURL+filename
	ontologies := map[string]string{
		"alc": filename, // key=prefix, value=filename
	}
	resolveLocalOntologies(ontologies, dir, baseURL)
	want := baseURL + filename
	if got := ontologies["alc"]; got != want {
		t.Errorf("resolveLocalOntologies existing file = %q; want %q", got, want)
	}

	// File missing — key should be deleted
	missing := map[string]string{
		"alc": "nonexistent.ttl",
	}
	resolveLocalOntologies(missing, dir, baseURL)
	if _, ok := missing["alc"]; ok {
		t.Error("resolveLocalOntologies did not delete key for missing file")
	}
}

func TestListOntologies(t *testing.T) {
	tr := &Traits{name: "assembler"}

	// GET → 200 with text/html Content-Type
	req := httptest.NewRequest(http.MethodGet, "/ontologies", nil)
	w := httptest.NewRecorder()
	tr.listOntologies(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("listOntologies GET status = %d; want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("listOntologies GET Content-Type = %q; want to contain \"text/html\"", ct)
	}

	// DELETE → 404
	reqDel := httptest.NewRequest(http.MethodDelete, "/ontologies", nil)
	wDel := httptest.NewRecorder()
	tr.listOntologies(wDel, reqDel)
	if wDel.Code != http.StatusMethodNotAllowed && wDel.Code != http.StatusNotFound {
		t.Errorf("listOntologies DELETE status = %d; want 404 or 405", wDel.Code)
	}
}

func TestAggregate(t *testing.T) {
	tr := &Traits{name: "assembler"}

	// DELETE → 404 (method guard, no live registrar needed)
	req := httptest.NewRequest(http.MethodDelete, "/aggregate", nil)
	w := httptest.NewRecorder()
	tr.aggregate(w, req)
	if w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
		t.Errorf("aggregate DELETE status = %d; want 404 or 405", w.Code)
	}
}
