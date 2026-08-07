package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// TestConcurrentOrchestrationIsRaceFree exercises what net/http does in
// production: every request runs in its own goroutine, and getServiceURL caches
// the registrar and authorizer URLs on the shared Traits.
//
// Run under -race. Without synchronisation this reports a data race on
// t.leadingRegistrar, t.leadingAuthorizer and t.uncheckedLogged — the three
// fields the orchestration path both reads and writes on the request path.
func TestConcurrentOrchestrationIsRaceFree(t *testing.T) {
	// Every call fails at the transport, which is what drives the reset paths
	// (leadingRegistrar = "" on error) as well as the assignment paths.
	newMockTransport(func() *http.Response {
		return &http.Response{
			Status: "200 OK", StatusCode: 200,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{"version":"ServiceRecordList_v1","list":[]}`)),
		}
	}, 0, nil)

	tr := createUnitAsset()

	var quest forms.ServiceQuest_v1
	quest.NewForm()
	quest.ServiceDefinition = "temperature"
	quest.Protocol = "http"

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tr.getServiceURL(quest, "consumer")
		}()
	}
	wg.Wait()
}
