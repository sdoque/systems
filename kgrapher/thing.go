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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters
type Traits struct {
	SystemList     forms.SystemRecordList_v1 `json:"-"`
	TripleStoreURL string                    `json:"graphDBurl"`
	LOntologies    map[string]string         `json:"localOntologies"` // map of ontology names to their file paths
	owner          *components.System        `json:"-"`
	name           string                    `json:"-"`

	// graph is the assembled cloud, rebuilt when the registry reports a change
	// rather than when somebody asks for it. Written by the subscriber's
	// goroutine and read by every request handler, so it is swapped as a whole.
	graph atomic.Pointer[assembled]

	// silenceFor overrides how long the stream may say nothing before it is
	// treated as dead. Zero means silenceLimit. A field so a test can drive the
	// watchdog itself rather than the context, which would pass whether or not
	// the watchdog existed.
	silenceFor time.Duration

	// rebuilding is what to do when the registry settles. A field so a test can
	// count rebuilds without a registrar, a cloud, or a triple store.
	rebuilding func()

	// retryPending stops an incomplete assembly from scheduling more than one
	// retry at a time. Without it, each retry that is still incomplete would
	// schedule another, and the pass that finally succeeds would arrive with a
	// crowd of duplicates behind it.
	retryPending atomic.Bool
	// settlePending does the same for the follow-up pass after a change; see
	// settleLater.
	settlePending atomic.Bool
	// assembling is a test seam, so a scheduled pass can be driven without a
	// registry or a store behind it.
	assembling func() (string, int, error)

	// registry is where the subscription reads from, discovered like any other
	// consumed service so the stream carries a token in an authorized cloud.
	registry *components.Cervice
}

// assembled is one build of the graph and the moment it was made.
type assembled struct {
	turtle string
	at     time.Time
}

// store keeps a freshly assembled graph for readers.
// store publishes a newly assembled graph, refusing an empty one.
//
// Belt and braces beside the error returns in assembleOntologies: an empty
// graph is never a correct description of a cloud that has at least this
// grapher in it, and serving one is indistinguishable from serving a good one
// until somebody reads the triples. Whatever fails upstream, the graph already
// served is a better answer than nothing.
func (t *Traits) store(turtle string) {
	if strings.TrimSpace(turtle) == "" {
		log.Println("kgrapher: refusing to replace the graph with an empty one")
		return
	}
	t.graph.Store(&assembled{turtle: turtle, at: time.Now()})
}

// current returns the graph last assembled, and whether there is one.
func (t *Traits) current() (*assembled, bool) {
	a := t.graph.Load()
	return a, a != nil
}

// registryToken returns the token to present on the subscription, if the cloud
// issues one. The subscription holds a connection open and so cannot use
// SystemList, but it asks for the same token in the same way.
func (t *Traits) registryToken() (string, bool) {
	return usecases.RegistryToken(t.registry, t.owner)
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	cloudgraph := components.Service{
		Definition:  "cloudgraph",
		SubPath:     "cloudgraph",
		Details:     map[string][]string{"Format": {"Turtle"}},
		RegPeriod:   61,
		Description: "provides the knowledge graph of a local cloud (GET)",
	}

	localOntologies := components.Service{
		Definition: "localOntologies",
		SubPath:    "localontologies",
		// text/html because it is a page for a person: listOntologies writes
		// markup. Saying so keeps it out of analyses that ask what depends on
		// what — documentation has no failure mode worth reporting.
		Details:     map[string][]string{"Location": {"Files"}, "Forms": {"text/html"}},
		RegPeriod:   61,
		Description: "provides the list of local ontologies (GET)",
	}

	return &components.UnitAsset{
		Name:        "assembler",
		Mission:     components.MissionAggregation,
		Mobility:    components.MobilityMovable,
		Details:     map[string][]string{"Type": {"Interactive"}},
		ServicesMap: map[string]*components.Service{cloudgraph.SubPath: &cloudgraph, localOntologies.SubPath: &localOntologies},
		Traits: &Traits{
			TripleStoreURL: "http://localhost:7200/repositories/Arrowhead/statements",
			LOntologies: map[string]string{
				"alc": "alc-ontology-local.ttl",
			},
		},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
		name:  configuredAsset.Name,
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	// Ensure that you have a valid local ontology directory
	const dir = "./files"
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("could not create directory %q: %v", dir, err)
	}
	serverAddress := sys.Husk.Host.IPAddresses[0]
	ontologyURL := fmt.Sprintf("http://%s:20105/kgrapher/assembler/files/", serverAddress)
	resolveLocalOntologies(t.LOntologies, dir, ontologyURL)

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Mobility:    configuredAsset.Mobility,
		TetheredTo:  configuredAsset.TetheredTo,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// The registry is a consumed service like any other, so that in an
	// authorized cloud the subscription carries a token minted for reading it.
	t.registry = &components.Cervice{
		Definition: "syslist",
		Protos:     components.SProtocols(sys.Husk.ProtoPort),
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "get",
	}

	go t.follow(sys.Ctx)

	return ua, func() {
		log.Println("Disconnecting from GraphDB")
	}
}

// resolveLocalOntologies checks if the local ontology files exist in the specified directory.
func resolveLocalOntologies(localOntologies map[string]string, dir string, baseURL string) {
	for prefix, filename := range localOntologies {
		fullPath := filepath.Join(dir, filename)

		if _, err := os.Stat(fullPath); err == nil {
			localOntologies[prefix] = baseURL + filename
		} else {
			fmt.Printf("Warning: ontology file %s not found in %s. Removing prefix '%s'.\n", filename, dir, prefix)
			delete(localOntologies, prefix)
		}
	}
}

//-------------------------------------Service handlers

// aggregate writes out the knowledge graph of the local cloud and pushes it to GraphDB
func (t *Traits) aggregate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		// Served from the last build. The subscriber rebuilds when the registry
		// reports a change, so this is the cloud as of that change rather than
		// as of this request — and a reader costs nothing, where it used to cost
		// one call to the registrar, one to every system, and an upload to the
		// triple store.
		if a, ok := t.current(); ok {
			w.Header().Set("Content-Type", "text/turtle")
			w.Header().Set("Last-Modified", a.at.UTC().Format(http.TimeFormat))
			if _, err := w.Write([]byte(a.turtle)); err != nil {
				log.Printf("kgrapher: writing the graph: %v\n", err)
			}
			return
		}

		// Nothing built yet: the subscriber has not had its first event, or the
		// registry has never been reachable. Build once here rather than answer
		// with nothing.
		graph, _, err := t.assembleOntologies()
		if err != nil {
			log.Printf("kgrapher: %v\n", err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		t.store(graph)
		w.Header().Set("Content-Type", "text/turtle")
		if _, err := w.Write([]byte(graph)); err != nil {
			log.Printf("kgrapher: writing the graph: %v\n", err)
			return
		}
		t.publishToStore(graph)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// listOntologies writes out the HTML produced by localOntologies()
func (t *Traits) listOntologies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		p := r.Pattern
		html := t.localOntologies(p)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// -------------------------------------Unit asset's function methods

// assembleOntologies gets the list of systems from the lead registrar and then the ontology of each system
// assembleOntologies fetches every registered system's ontology and returns the
// assembled graph.
//
// It no longer writes the graph anywhere. Building it is expensive — one request
// to the registrar and one to each system in the cloud — and doing that inside
// the handler meant every reader paid for it, and every reader also triggered a
// fresh upload to the triple store. Separating the two lets the graph be built
// when the cloud changes and read as often as anyone likes.
func (t *Traits) assembleOntologies() (string, int, error) {
	var skipped int
	// One implementation of this, in the framework. The request was built here
	// by hand, and in modeler too, and neither carried an access token — so
	// declaring syslist as a service refused both in exactly the clouds the
	// declaration was for.
	systems, err := usecases.SystemList(t.registry, t.owner)
	if err != nil {
		return "", 0, err
	}

	prefixes := make(map[string]bool)
	processedBlocks := make(map[string]bool)
	var uniqueIndividuals []string

	for _, s := range systems {
		sysUrl := s + "/kgraph"
		fmt.Println(sysUrl)

		resp, err := http.Get(sysUrl)
		if err != nil {
			log.Printf("Unable to get ontology from %s: %s\n", s, err)
			continue
		}
		// A refusal is not an ontology.
		//
		// The body was parsed whatever the status said, which was harmless only
		// for as long as /kgraph answered every caller. It does not: it now
		// requires a caller this cloud enrolled, and a system registered under
		// its plain-HTTP URL — which every system is, in the seconds before its
		// certificate arrives — is refused. The refusal text was then spliced
		// into the Turtle, and the whole assembled graph was rejected by the
		// store with "MALFORMED DATA", so one unreachable system lost the entire
		// cloud's graph rather than its own contribution to it.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			log.Printf("Skipping ontology from %s: %s\n", s, resp.Status)
			skipped++
			continue
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Error reading ontology response from %s: %s\n", s, err)
			continue
		}

		text := strings.ReplaceAll(string(bodyBytes), "\r\n", "\n")
		blocks := strings.Split(text, "\n\n")

		for _, block := range blocks {
			normalizedBlock := strings.TrimSpace(block)
			if normalizedBlock == "" {
				continue
			}
			if processedBlocks[normalizedBlock] {
				continue
			}

			if strings.HasPrefix(normalizedBlock, "@prefix") {
				for _, line := range strings.Split(normalizedBlock, "\n") {
					if strings.HasPrefix(line, "@prefix") {
						prefixes[line] = true
					}
				}
				continue
			}

			processedBlocks[normalizedBlock] = true
			uniqueIndividuals = append(uniqueIndividuals, normalizedBlock)
		}
	}

	var cloudIRI string
	{
		seen := map[string]struct{}{}
		for _, blk := range uniqueIndividuals {
			if !isSystemBlock(blk) {
				continue
			}
			vals := extractContainedIns(blk)
			local := map[string]struct{}{}
			for _, v := range vals {
				local[v] = struct{}{}
			}
			if len(local) > 1 {
				return "", skipped, fmt.Errorf("%s", fmt.Sprintf("Bad Request: system %s has conflicting afo:isContainedIn values", extractSubject(blk)))
			}
			for k := range local {
				seen[k] = struct{}{}
			}
		}
		if len(seen) == 0 {
			return "", skipped, fmt.Errorf("%s", "Bad Request: no afo:isContainedIn found; please declare a LocalCloud in at least one system")
		}
		if len(seen) > 1 {
			var all []string
			for k := range seen {
				all = append(all, k)
			}
			sort.Strings(all)
			return "", skipped, fmt.Errorf("%s", fmt.Sprintf("Bad Request: multiple LocalClouds detected across systems: %v", all))
		}
		for k := range seen {
			cloudIRI = k
		}
	}

	for i, blk := range uniqueIndividuals {
		if isSystemBlock(blk) && len(extractContainedIns(blk)) == 0 {
			uniqueIndividuals[i] = injectContainedIn(blk, cloudIRI)
		}
	}

	uniqueIndividuals = addCloudPrefixToBlocks(uniqueIndividuals, cloudIRI)

	var graph string

	for prefix := range prefixes {
		graph += prefix + "\n"
	}

	ontoImport := "\nalc:ontology a owl:Ontology "
	for _, uri := range t.LOntologies {
		ontoImport += fmt.Sprintf(";\n    owl:imports <%s> ", uri)
	}
	ontoImport += ".\n"
	graph += ontoImport + "\n"

	for _, block := range uniqueIndividuals {
		graph += block + "\n\n"
	}
	return graph, skipped, nil
}

// publishToStore writes the assembled graph to the triple store, first as a
// timestamped snapshot and then as the current graph.
// Where the cloud's knowledge lives in the triple store.
//
//	urn:state:current     the cloud as it is now — the graph everything queries
//	urn:staging           scratch, overwritten on every rebuild, never queried
//	urn:changes           the index of changes, one description per event
//	urn:changes:<t>/added the triples that appeared at that moment
//	…/removed             the triples that went
const (
	currentGraph = "urn:state:current"
	stagingGraph = "urn:staging"
	changeIndex  = "urn:changes"
)

// publishToStore records the cloud in the triple store, and records a change
// only when there is one.
//
// It used to write a full timestamped snapshot on every rebuild. That is how
// the store came to hold 490 copies of a cloud that has one — and the copies
// were not history: two taken two minutes apart during a quiet night differed
// by zero triples in each direction. They recorded when kgrapher ran, not when
// anything changed.
//
// So: stage the new graph, ask the store whether it differs from what is
// current, and write nothing at all when it does not. When it does, record the
// difference — what appeared and what went — as an event with a timestamp. A
// quiet night now leaves no trace, and every entry that exists is something
// that happened.
//
// The comparison is made in the store rather than against the last graph this
// process assembled, for two reasons. It survives a restart, so coming back up
// does not look like a change. And it compares sets of triples rather than
// text, which matters because the assembler emits a system's services in map
// order — two runs over an unchanged cloud produce the same graph and not the
// same bytes.
func (t *Traits) publishToStore(graph string) {
	// As in store: an empty graph is not a description of anything, and putting
	// one over the current graph replaces the cloud's knowledge with nothing.
	if strings.TrimSpace(graph) == "" {
		log.Println("kgrapher: refusing to publish an empty graph to the triple store")
		return
	}

	statementsURL := t.TripleStoreURL
	repoBase := strings.TrimSuffix(t.TripleStoreURL, "/statements")
	client := &http.Client{Transport: http.DefaultClient.Transport, Timeout: 60 * time.Second}

	if err := putGraph(client, repoBase, stagingGraph, graph); err != nil {
		log.Printf("kgrapher: staging the graph: %v\n", err)
		return
	}

	changed, err := differs(client, repoBase, stagingGraph, currentGraph)
	if err != nil {
		log.Printf("kgrapher: comparing the new graph with the current one: %v\n", err)
		return
	}
	if !changed {
		// Nothing to say. The staging graph is left where it is; the next
		// rebuild overwrites it, and it is never queried.
		return
	}

	at := time.Now().UTC().Format(time.RFC3339)
	event := "urn:changes:" + at

	// One update, so the store never holds a change recorded against a current
	// graph that was not replaced, or the reverse.
	update := fmt.Sprintf(`
PREFIX alc:  <http://www.synecdoque.com/lcloud/>
PREFIX xsd:  <http://www.w3.org/2001/XMLSchema#>

INSERT { GRAPH <%[1]s/added> { ?s ?p ?o } }
WHERE  { GRAPH <%[2]s> { ?s ?p ?o } FILTER NOT EXISTS { GRAPH <%[3]s> { ?s ?p ?o } } };

INSERT { GRAPH <%[1]s/removed> { ?s ?p ?o } }
WHERE  { GRAPH <%[3]s> { ?s ?p ?o } FILTER NOT EXISTS { GRAPH <%[2]s> { ?s ?p ?o } } };

INSERT DATA { GRAPH <%[4]s> {
  <%[1]s> a alc:Change ;
          alc:at "%[5]s"^^xsd:dateTime ;
          alc:added   <%[1]s/added> ;
          alc:removed <%[1]s/removed> .
} };

CLEAR GRAPH <%[3]s>;
ADD GRAPH <%[2]s> TO <%[3]s>;
`, event, stagingGraph, currentGraph, changeIndex, at)

	if err := postUpdate(client, statementsURL, update); err != nil {
		log.Printf("kgrapher: recording the change: %v\n", err)
		return
	}
	log.Printf("kgrapher: the cloud changed; recorded at %s\n", event)
}

// putGraph replaces one named graph with the Turtle given, through the RDF
// Graph Store Protocol.
func putGraph(client *http.Client, repoBase, iri, turtle string) error {
	gspURL := repoBase + "/rdf-graphs/service?graph=" + url.QueryEscape(iri)
	req, err := http.NewRequest(http.MethodPut, gspURL, bytes.NewBufferString(turtle))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/turtle")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("PUT %s: %s: %s", iri, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// differs asks whether two named graphs hold different triples, in either
// direction.
//
// One ASK rather than two counts: the question is whether anything at all
// differs, and the store can stop at the first answer.
func differs(client *http.Client, repoBase, a, b string) (bool, error) {
	query := fmt.Sprintf(`ASK {
  { GRAPH <%[1]s> { ?s ?p ?o } FILTER NOT EXISTS { GRAPH <%[2]s> { ?s ?p ?o } } }
  UNION
  { GRAPH <%[2]s> { ?s ?p ?o } FILTER NOT EXISTS { GRAPH <%[1]s> { ?s ?p ?o } } }
}`, a, b)

	form := url.Values{"query": {query}}
	req, err := http.NewRequest(http.MethodPost, repoBase, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var answer struct {
		Boolean bool `json:"boolean"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return false, fmt.Errorf("reading the answer: %w", err)
	}
	return answer.Boolean, nil
}

// postUpdate runs a SPARQL update.
func postUpdate(client *http.Client, statementsURL, update string) error {
	form := url.Values{"update": {update}}
	req, err := http.NewRequest(http.MethodPost, statementsURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// updatePrefixes updates the prefixes in the RDF blocks with the new URIs from the local ontologies.
func updatePrefixes(prefixes map[string]bool, prefixUpdates map[string]string) {
	updated := make(map[string]bool)

	for line := range prefixes {
		if strings.HasPrefix(line, "@prefix") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				prefix := strings.TrimSuffix(parts[1], ":")
				if newURI, ok := prefixUpdates[prefix]; ok {
					line = fmt.Sprintf("@prefix %s: <%s#> .", prefix, newURI)
				}
			}
		}
		updated[line] = true
	}

	for k := range prefixes {
		delete(prefixes, k)
	}
	for k := range updated {
		prefixes[k] = true
	}
}

// addCloudPrefixToBlocks prefixes all alc: subjects with "<CloudName>_".
func addCloudPrefixToBlocks(blocks []string, cloudIRI string) []string {
	cloudIRI = ensurePrefixed(cloudIRI)
	cloudName := extractCloudName(cloudIRI)
	if cloudName == "" {
		return blocks
	}

	mapping := map[string]string{}
	for _, blk := range blocks {
		subj := extractSubject(blk)
		if !strings.HasPrefix(subj, "alc:") {
			continue
		}
		rest := strings.TrimPrefix(subj, "alc:")
		if rest == "" {
			continue
		}
		if rest == cloudName || rest == "ontology" {
			continue
		}
		if strings.HasPrefix(rest, cloudName+"_") {
			continue
		}
		newSubj := "alc:" + cloudName + "_" + rest
		mapping[subj] = newSubj
	}

	if len(mapping) == 0 {
		return blocks
	}

	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})

	out := make([]string, len(blocks))
	for i, blk := range blocks {
		txt := blk
		for _, old := range keys {
			txt = strings.ReplaceAll(txt, old, mapping[old])
		}
		out[i] = txt
	}
	return out
}

// extractCloudName gets the local name from a cloud IRI.
func extractCloudName(iri string) string {
	iri = strings.TrimSpace(iri)
	if iri == "" {
		return ""
	}

	if strings.HasPrefix(iri, "<") && strings.HasSuffix(iri, ">") {
		inner := iri[1 : len(iri)-1]
		idx := strings.LastIndexAny(inner, "#/")
		if idx >= 0 && idx+1 < len(inner) {
			return inner[idx+1:]
		}
		return inner
	}

	if strings.Contains(iri, ":") {
		parts := strings.SplitN(iri, ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}

	return iri
}

// ------------------------------------- Local cloud containing all systems

// ensurePrefixed returns v with "alc:" prefix unless it's already an IRI or prefixed.
func ensurePrefixed(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if (strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")) || strings.Contains(v, ":") {
		return v
	}
	return "alc:" + v
}

// isSystemBlock reports whether this TTL block defines an afo:System individual.
func isSystemBlock(block string) bool {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) == 0 {
		return false
	}
	first := lines[0]
	return strings.Contains(first, " a afo:System ")
}

// extractSubject returns the subject IRI (first token of the first line).
func extractSubject(block string) string {
	first := strings.Split(strings.TrimSpace(block), "\n")[0]
	parts := strings.Fields(first)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// extractContainedIns finds all afo:isContainedIn objects in a block.
func extractContainedIns(block string) []string {
	var found []string
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "afo:isContainedIn ") {
			after := strings.SplitN(line, "afo:isContainedIn", 2)[1]
			after = strings.TrimSpace(after)
			after = strings.TrimRight(after, " ;.")
			if after != "" {
				found = append(found, after)
			}
		}
	}
	return found
}

// injectContainedIn inserts "afo:isContainedIn <iri>" as one of the system's predicates.
func injectContainedIn(block, iri string) string {
	iri = ensurePrefixed(iri)

	if len(extractContainedIns(block)) > 0 {
		return block
	}

	trim := strings.TrimRight(block, " \t\r\n")
	if trim == "" {
		return block
	}

	if strings.HasSuffix(trim, ".") {
		core := strings.TrimSuffix(trim, ".")
		return core + " ;\n    afo:isContainedIn " + iri + " ."
	}

	subj := extractSubject(block)
	if subj == "" {
		return block
	}
	return trim + "\n" + fmt.Sprintf("%s afo:isContainedIn %s .", subj, iri)
}

// detectGlobalCloud validates there is at most one unique LocalCloud across all system blocks.
func detectGlobalCloud(blocks []string) (string, error) {
	set := map[string]struct{}{}
	for _, b := range blocks {
		if !isSystemBlock(b) {
			continue
		}
		vals := extractContainedIns(b)
		local := map[string]struct{}{}
		for _, v := range vals {
			local[ensurePrefixed(v)] = struct{}{}
		}
		if len(local) > 1 {
			var ls []string
			for k := range local {
				ls = append(ls, k)
			}
			sort.Strings(ls)
			return "", fmt.Errorf("a system block has conflicting afo:isContainedIn values: %v", ls)
		}
		for k := range local {
			set[k] = struct{}{}
		}
	}
	if len(set) <= 1 {
		for k := range set {
			return k, nil
		}
		return "", nil
	}
	var gs []string
	for k := range set {
		gs = append(gs, k)
	}
	sort.Strings(gs)
	return "", fmt.Errorf("multiple LocalClouds detected across systems: %v", gs)
}

// ----------- Local Ontologies Service -----------------------------------------------------------

// localOntologies reads the ./files directory and builds an HTML list
func (t *Traits) localOntologies(sp string) string {
	entries, err := os.ReadDir("./files")
	if err != nil {
		return fmt.Sprintf("<p><strong>Error:</strong> could not read files directory: %v</p>", err)
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html><head>
<meta charset="utf-8"><title>Available Ontologies</title>
</head><body>
<h1>Available Ontologies</h1>
<ul>`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		link := sp + t.name + "/files/" + name
		sb.WriteString(fmt.Sprintf(`<li><a href="%s">%s</a></li>`, link, name))
	}

	sb.WriteString(`</ul>
</body></html>`)
	return sb.String()
}
