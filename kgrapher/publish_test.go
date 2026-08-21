package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubStore stands in for GraphDB: it records what was asked of it and answers
// the difference question however the test wants.
type stubStore struct {
	server  *httptest.Server
	differs bool

	puts    []string // named graphs replaced through the Graph Store Protocol
	updates []string // SPARQL updates run
}

func newStubStore(t *testing.T, differs bool) *stubStore {
	t.Helper()
	s := &stubStore{differs: differs}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "rdf-graphs"):
			s.puts = append(s.puts, r.URL.Query().Get("graph"))
			w.WriteHeader(http.StatusNoContent)

		case strings.Contains(string(body), "update="):
			decoded, _ := io.ReadAll(strings.NewReader(string(body)))
			s.updates = append(s.updates, string(decoded))
			w.WriteHeader(http.StatusNoContent)

		default: // the ASK
			w.Header().Set("Content-Type", "application/sparql-results+json")
			if s.differs {
				_, _ = w.Write([]byte(`{"head":{},"boolean":true}`))
			} else {
				_, _ = w.Write([]byte(`{"head":{},"boolean":false}`))
			}
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

// A cloud that has not changed leaves no trace.
//
// This is the whole point. The store held 490 timestamped copies of a cloud
// that has one, written at roughly twenty-five an hour through a night in which
// nothing happened — two of them taken two minutes apart differed by zero
// triples in either direction. They recorded when kgrapher ran, not when
// anything changed.
func TestAnUnchangedCloudIsNotRecorded(t *testing.T) {
	store := newStubStore(t, false)
	tr := &Traits{TripleStoreURL: store.server.URL + "/statements"}

	tr.publishToStore("alc:x a alc:Thing .")

	if len(store.updates) != 0 {
		t.Errorf("an unchanged cloud produced %d update(s):\n%s",
			len(store.updates), strings.Join(store.updates, "\n"))
	}
	// Staging is still written — that is how the comparison is made — but it is
	// one graph that gets overwritten, not one per rebuild.
	if len(store.puts) != 1 || store.puts[0] != stagingGraph {
		t.Errorf("graphs written: %v; want only the staging graph", store.puts)
	}
}

// And a cloud that has changed records what changed, not another whole copy.
func TestAChangedCloudRecordsTheDifference(t *testing.T) {
	store := newStubStore(t, true)
	tr := &Traits{TripleStoreURL: store.server.URL + "/statements"}

	tr.publishToStore("alc:x a alc:Thing .")

	if len(store.updates) != 1 {
		t.Fatalf("%d updates; want one, so the store never holds a change recorded "+
			"against a current graph that was not replaced", len(store.updates))
	}
	update := store.updates[0]

	for _, want := range []string{
		"%2Fadded",   // the triples that appeared
		"%2Fremoved", // and the ones that went
		"alc%3AChange",
		"urn%3Achanges", // the index, so the events can be listed
		"CLEAR+GRAPH",   // and the current graph is replaced
	} {
		if !strings.Contains(update, want) {
			t.Errorf("the update does not contain %q", want)
		}
	}
	// No timestamped copy of the whole cloud.
	if strings.Contains(update, "urn%3Asnapshots") {
		t.Error("a full snapshot was still written")
	}
}

// An empty graph is not a description of anything, and must never replace one.
func TestAnEmptyGraphIsRefused(t *testing.T) {
	store := newStubStore(t, true)
	tr := &Traits{TripleStoreURL: store.server.URL + "/statements"}

	tr.publishToStore("   \n  ")

	if len(store.puts) != 0 || len(store.updates) != 0 {
		t.Error("an empty graph reached the store")
	}
}
