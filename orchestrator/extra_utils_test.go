package main

import (
	"context"
	"fmt"
	"net/http"

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

func createSystemWithUnitAsset(url string) components.System {
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

func createUnitAsset(url string) *UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Location": {"LocalCloud"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	assetTraits := Traits{
		leadingRegistrar: "",
	}

	// create the unit asset template
	uat := &UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			squest.SubPath: &squest, // Inline assignment of the temperature service
		},
	}

	sys := createSystemWithUnitAsset(url)
	uat.Owner = &sys

	return uat
}

/*

// A mocked UnitAsset used for testing
type mockUnitAsset struct {
	Name        string              `json:"name"`    // Must be a unique name, ie. a sensor ID
	Owner       *components.System  `json:"-"`       // The parent system this UA is part of
	Details     map[string][]string `json:"details"` // Metadata or details about this UA
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
}

func (mua mockUnitAsset) GetName() string {
	return mua.Name
}

func (mua mockUnitAsset) GetServices() components.Services {
	return mua.ServicesMap
}

func (mua mockUnitAsset) GetCervices() components.Cervices {
	return mua.CervicesMap
}

func (mua mockUnitAsset) GetDetails() map[string][]string {
	return mua.Details
}

func (mua mockUnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {}

// A mocked form used for testing
type mockForm struct {
	XMLName xml.Name `json:"-" xml:"testName"`
	Value   any      `json:"value" xml:"value"`
	Unit    string   `json:"unit" xml:"unit"`
	Version string   `json:"version" xml:"version"`
}

// NewForm creates a new form
func (f mockForm) NewForm() forms.Form {
	f.Version = "testVersion"
	return f
}

// FormVersion returns the version of the form
func (f mockForm) FormVersion() string {
	return f.Version
}

*/

type errorReader struct{}

func (errorReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("forced read error")
}

type mockResponseWriter struct {
	headers    http.Header
	body       []byte
	status     int
	writeError bool
}

func (e *mockResponseWriter) Write(b []byte) (int, error) {
	if e.writeError {
		return 0, fmt.Errorf("Forced write error")
	}
	e.body = append(e.body, b...)
	return len(b), nil
}

func (e *mockResponseWriter) WriteHeader(statusCode int) {
	e.status = statusCode
}

func (e *mockResponseWriter) Header() http.Header {
	return e.headers
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{
		headers:    make(http.Header),
		status:     http.StatusOK,
		writeError: true,
	}
}

var brokenUrl = string(rune(0))

var errHTTP error = fmt.Errorf("bad http request")

// Create a error reader to break json.Unmarshal()
type errReader int

var errBodyRead error = fmt.Errorf("bad body read")

func (errReader) Read(p []byte) (n int, err error) {
	return 0, errBodyRead
}
func (errReader) Close() error {
	return nil
}

// Variables used in testing

// Help function to create a test system
func createTestSystem(broken bool) (sys components.System) {
	// instantiate the System
	ctx := context.Background()
	sys = components.NewSystem("testSystem", ctx)

	// Instantiate the Capsule
	sys.Husk = &components.Husk{
		Description: "A test system",
		Details:     map[string][]string{"Developer": {"Test dev"}},
		ProtoPort:   map[string]int{"https": 0, "http": 1234, "coap": 0},
		InfoLink:    "https://for.testing.purposes",
	}

	// create fake services and cervices for a mocked unit asset
	testCerv := &components.Cervice{
		Definition: "testCerv",
		Details:    map[string][]string{"Forms": {"SignalA_v1a"}},
		Nodes:      map[string][]string{},
	}

	CervicesMap := &components.Cervices{
		testCerv.Definition: testCerv,
	}
	setTest := &components.Service{
		ID:            1,
		Definition:    "squest",
		SubPath:       "squest",
		Details:       map[string][]string{"Forms": {"SignalA_v1a"}},
		Description:   "A test service",
		RegPeriod:     45,
		RegTimestamp:  "now",
		RegExpiration: "45",
	}
	ServicesMap := &components.Services{
		setTest.SubPath: setTest,
	}
	assetTraits := Traits{
		leadingRegistrar: "",
	}
	mua := &UnitAsset{
		Name:        "testUnitAsset",
		Details:     map[string][]string{"Test": {"Test"}},
		Traits:      assetTraits,
		ServicesMap: *ServicesMap,
		CervicesMap: *CervicesMap,
	}

	sys.UAssets = make(map[string]*components.UnitAsset)
	var muaInterface components.UnitAsset = mua
	sys.UAssets[mua.GetName()] = &muaInterface

	leadingRegistrar := &components.CoreSystem{
		Name: components.ServiceRegistrarName,
		Url:  "https://leadingregistrar",
	}
	test := &components.CoreSystem{
		Name: "test",
		Url:  "https://test",
	}
	if broken == false {
		orchestrator := &components.CoreSystem{
			Name: "orchestrator",
			Url:  "https://orchestator",
		}
		sys.CoreS = []*components.CoreSystem{
			leadingRegistrar,
			orchestrator,
			test,
		}
	} else {
		orchestrator := &components.CoreSystem{
			Name: "orchestrator",
			Url:  brokenUrl,
		}
		sys.CoreS = []*components.CoreSystem{
			leadingRegistrar,
			orchestrator,
			test,
		}
	}
	return
}
