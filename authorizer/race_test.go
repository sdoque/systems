package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

type stubRegistrar struct{}

func (stubRegistrar) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"version":"ServiceRecordList_v1","list":[]}`
	// GetRunningCoreSystemURL status-checks a registrar and skips any that does
	// not answer as the leader, so the cached URL is only ever written when this
	// answers correctly.
	if strings.HasSuffix(req.URL.Path, "/status") {
		body = components.ServiceRegistrarLeader + " now"
	}
	return &http.Response{
		Status: "200 OK", StatusCode: 200,
		Header:  http.Header{"Content-Type": []string{"application/json"}},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: req,
	}, nil
}

// TestConcurrentAdjudicationIsRaceFree exercises what net/http does in
// production: every quest runs in its own goroutine, and resolving a subject's
// attributes caches the registrar URL on the shared Traits.
//
// Run under -race. With leadingRegistrar as a plain string this reports a data
// race — t.mu guarded only the policies, never the cached URL.
func TestConcurrentAdjudicationIsRaceFree(t *testing.T) {
	http.DefaultClient.Transport = stubRegistrar{}

	sys := components.NewSystem("authorizer", t.Context())
	sys.Husk = &components.Husk{
		CoreS: []*components.CoreSystem{{
			Name: components.ServiceRegistrarName,
			Url:  "http://localhost:20102/serviceregistrar/registry",
		}},
	}
	tr := &Traits{owner: &sys}
	tr.attributesOf = tr.subjectAttributes

	var quest forms.AuthorizationQuest_v1
	quest.NewForm()
	quest.Subject = "thermostat"
	quest.Action = "read"

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Adjudicate(quest)
		}()
	}
	wg.Wait()
}
