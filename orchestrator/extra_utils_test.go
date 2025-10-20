package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/sdoque/mbaigo/components"
)

// mockTransport is used for replacing the default network Transport (used by
// http.DefaultClient) and it will intercept network requests.
type mockTransport struct {
	respFunc func() *http.Response
	hits     int
	err      error
}

func newMockTransport(respFunc func() *http.Response, v int, err error) *mockTransport {
	t := &mockTransport{
		respFunc: respFunc,
		hits:     v,
		err:      err,
	}
	// Hijack the default http client so no actual http requests are sent over the network
	http.DefaultClient.Transport = t
	return t
}

// RoundTrip method is required to fulfil the RoundTripper interface (as required by the DefaultClient).
// It prevents the request from being sent over the network, and count how many times
// a http request was sent
func (t *mockTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	t.hits -= 1
	if t.hits == 0 {
		return resp, t.err
	}
	resp = t.respFunc()
	resp.Request = req
	return resp, nil
}

func createSystemWithUnitAsset() components.System {
	ctx := context.Background()
	sys := components.NewSystem("testSystem", ctx)

	leadingRegistrar := &components.CoreSystem{
		Name: components.ServiceRegistrarName,
		Url:  "http://localhost:20102/serviceregistrar/registry",
	}
	sys.CoreS = []*components.CoreSystem{
		leadingRegistrar,
	}
	return sys
}

func createUnitAsset() *UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Location": {"LocalCloud"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	// create the unit asset template
	uat := &UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		ServicesMap: components.Services{
			squest.SubPath: &squest, // Inline assignment of the temperature service
		},
		Traits: Traits{
			leadingRegistrar: "", // Initialize the leading registrar to nil
		},
	}

	sys := createSystemWithUnitAsset()
	uat.Owner = &sys

	return uat
}

type errorReader struct{}

func (errorReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("forced read error")
}

type mockResponseWriter struct {
	*httptest.ResponseRecorder
	writeError bool
}

func (e *mockResponseWriter) Write(b []byte) (int, error) {
	if e.writeError {
		return 0, fmt.Errorf("Forced write error")
	}
	return e.ResponseRecorder.Write(b)
}

func (e *mockResponseWriter) WriteHeader(statusCode int) {
	e.ResponseRecorder.Code = statusCode
}

func (e *mockResponseWriter) Header() http.Header {
	return e.ResponseRecorder.Header()
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		writeError:       true,
	}
}

var brokenUrl = string(rune(0))

var errHTTP error = fmt.Errorf("bad http request")
