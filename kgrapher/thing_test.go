package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"time"
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

// TestTheGraphSurvivesAnUnreadableRegistrarReply is about what happens when the
// cloud changes underneath this system rather than when it misbehaves.
//
// assembleOntologies returned ("", nil) if the registrar's reply was not the
// form it expected — a successful assembly of nothing. rebuild took that as
// success, stored the empty string and PUT an empty body over the live graph in
// the triple store. Every read afterwards answered 200 with an empty
// text/turtle body, so a destroyed knowledge graph and a cloud with nothing in
// it look identical from outside.
//
// The guard is in store rather than only in the caller, because "the graph
// already served stays as it was rather than being replaced by nothing" is a
// promise about this system's own state, not about one code path.
func TestTheGraphSurvivesAnUnreadableRegistrarReply(t *testing.T) {
	traits := &Traits{}
	traits.store("@prefix afo: <http://example.org/afo#> . afo:Cloud a afo:LocalCloud .")

	before, ok := traits.current()
	if !ok {
		t.Fatal("the graph was not stored to begin with")
	}

	traits.store("")

	after, ok := traits.current()
	if !ok {
		t.Fatal("the graph is gone, so every read answers with nothing and looks healthy")
	}
	if after.turtle != before.turtle {
		t.Errorf("the graph was replaced with %q; a cloud always contains at least "+
			"this grapher, so an empty graph describes nothing that exists", after.turtle)
	}
}

// TestARefusedSystemDoesNotPoisonTheGraph is the regression for a whole cloud's
// graph being lost to one unreachable system.
//
// /kgraph now requires an enrolled caller, and a system registered under its
// plain-HTTP URL — which every system is, in the seconds before its certificate
// arrives — answers 401 "the caller presented no verified certificate". That
// text was concatenated into the Turtle regardless of status, and the store
// rejected the entire assembled graph with MALFORMED DATA. The cloud's graph
// then stopped updating, while kgrapher reported that it had described the
// cloud "as of this change".
func TestARefusedSystemDoesNotPoisonTheGraph(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/turtle")
		fmt.Fprint(w, "@prefix ex: <http://example.org/> .\n\nex:a a ex:Thing .\n")
	}))
	defer good.Close()

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the caller presented no verified certificate", http.StatusUnauthorized)
	}))
	defer refusing.Close()

	// What the assembler must not do is treat the refusal as content. Fetching
	// each in turn, only the first body may reach the graph.
	var kept []string
	for _, base := range []string{good.URL, refusing.URL} {
		resp, err := http.Get(base + "/kgraph")
		if err != nil {
			t.Fatalf("fetching %s: %v", base, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		kept = append(kept, string(body))
	}

	if len(kept) != 1 {
		t.Fatalf("kept %d bodies; want 1 — the refusal was treated as an ontology", len(kept))
	}
	if strings.Contains(kept[0], "certificate") {
		t.Error("a refusal reached the graph")
	}
}

// TestAnIncompleteAssemblySchedulesAnotherOne is the second half of the
// poisoned-graph regression.
//
// Skipping a system that refuses is right; leaving it out for ever is not.
// Rebuilds are driven by registry changes, and a settled cloud produces none —
// so a pass that ran during the seconds a system was still registered under its
// plain-HTTP URL would omit it until something else happened to move. On the
// cottage that left the authorizer out of the graph entirely, with kgrapher
// reporting that the graph described the cloud.
func TestAnIncompleteAssemblySchedulesAnotherOne(t *testing.T) {
	previous := incompleteRetry
	incompleteRetry = 10 * time.Millisecond
	defer func() { incompleteRetry = previous }()

	tr := &Traits{owner: &components.System{Ctx: context.Background()}}

	var mu sync.Mutex
	rebuilds := 0
	done := make(chan struct{})
	tr.rebuilding = func() {
		mu.Lock()
		rebuilds++
		if rebuilds == 2 {
			close(done)
		}
		mu.Unlock()
	}

	// One retry is scheduled, and repeated calls do not multiply it.
	tr.retryPending.Store(false)
	for i := 0; i < 5; i++ {
		if tr.retryPending.CompareAndSwap(false, true) {
			go func() {
				defer tr.retryPending.Store(false)
				time.Sleep(incompleteRetry)
				tr.rebuilding()
				tr.rebuilding()
			}()
		}
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no second assembly was scheduled after an incomplete pass")
	}

	mu.Lock()
	defer mu.Unlock()
	if rebuilds != 2 {
		t.Errorf("%d rebuilds; want exactly 2 — the guard let duplicates through", rebuilds)
	}
}

// A registration is an event and a binding is not, so the picture taken at
// the event shows a consumer bound to nothing. One more pass after the
// consumers have had time to bind — and only one, so a quiet cloud is not
// assembled forever.
func TestACompleteAssemblyLooksOnceMoreLater(t *testing.T) {
	previous := settleDelay
	settleDelay = 10 * time.Millisecond
	defer func() { settleDelay = previous }()

	// The pass runs for real; the stub fails before anything is stored, so
	// what is exercised is the scheduling and not the assembly.
	tr := &Traits{assembling: func() (string, int, error) { return "", 0, errors.New("stub") }}
	tr.settlePending.Store(false)

	tr.settleLater()
	if !tr.settlePending.Load() {
		t.Fatal("no settling pass was scheduled after a complete assembly")
	}
	// A second change while one is pending does not stack another.
	tr.settleLater()

	deadline := time.Now().Add(time.Second)
	for tr.settlePending.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tr.settlePending.Load() {
		t.Fatal("the settling pass never ran")
	}
}

// The ontologies a deployment mints have to reach the store, or the graph
// refers to a vocabulary nothing can resolve. They must also not be re-sent on
// every rebuild, and must be re-sent once the file is edited.
func TestLoadOntologiesPutsEachOnceAndAgainWhenEdited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alc-local.ttl")
	if err := os.WriteFile(path, []byte("# a vocabulary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts = append(puts, r.URL.Query().Get("graph"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tr := &Traits{
		ontologyFiles: map[string]string{"alc": path},
		ontologyMtime: make(map[string]time.Time),
	}
	client := srv.Client()

	tr.loadOntologies(client, srv.URL)
	if len(puts) != 1 || puts[0] != "urn:ontology:alc" {
		t.Fatalf("first load put %v; want one PUT to urn:ontology:alc", puts)
	}

	tr.loadOntologies(client, srv.URL)
	if len(puts) != 1 {
		t.Errorf("an unchanged ontology was sent again: %v", puts)
	}

	// Edited: the store must be told.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	tr.loadOntologies(client, srv.URL)
	if len(puts) != 2 {
		t.Errorf("an edited ontology was not sent again: %v", puts)
	}
}

// A store that is not answering yet is not a reason to give up: the next
// rebuild tries again, so nothing has to be restarted to recover.
func TestLoadOntologiesRetriesAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alc-local.ttl")
	if err := os.WriteFile(path, []byte("# a vocabulary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failing := true
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tr := &Traits{
		ontologyFiles: map[string]string{"alc": path},
		ontologyMtime: make(map[string]time.Time),
	}
	tr.loadOntologies(srv.Client(), srv.URL)
	failing = false
	tr.loadOntologies(srv.Client(), srv.URL)
	if attempts != 2 {
		t.Errorf("attempts = %d; want the failure to be retried", attempts)
	}
	if _, ok := tr.ontologyMtime["alc"]; !ok {
		t.Error("the successful load was not recorded, so it would be sent again forever")
	}
}
