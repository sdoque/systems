package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// ----------------------------------------------- //
// Help functions and structs to test roleStatus()
// ----------------------------------------------- //

func createLeadingRegistrar() *Traits {
	t := &Traits{}
	t.role.Store(&registrarRole{leading: true, since: time.Now()})
	return t
}

func createNonLeadingRegistrar() *Traits {
	t := &Traits{}
	t.role.Store(&registrarRole{registrar: &components.CoreSystem{Name: "otherRegistrar", Url: "otherURL"}})
	return t
}

func createServiceUnavailableRegistrar() *Traits {
	t := &Traits{}
	t.role.Store(&registrarRole{})
	return t
}

type roleStatusParams struct {
	expectedStatuscode int
	setup              func() *Traits
	request            *http.Request
	testCase           string
}

func TestRoleStatus(t *testing.T) {
	params := []roleStatusParams{
		{
			200,
			func() *Traits { return createLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() *Traits { return createNonLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() *Traits { return createServiceUnavailableRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Bad case, service unavailable",
		},
		{
			200,
			func() *Traits { return &Traits{} },
			httptest.NewRequest(http.MethodPost, "http://localhost/test", nil),
			"Bad case, unsupported http method",
		},
	}
	for _, c := range params {
		tr := c.setup()
		w := httptest.NewRecorder()
		r := c.request

		tr.roleStatus(w, r)
		statusCode := w.Result().StatusCode
		if statusCode != c.expectedStatuscode {
			t.Errorf("Failed '%s', expected statuscode %d got: %d", c.testCase, c.expectedStatuscode, statusCode)
		}
	}
}

// ---------------------------------------------- //
// Help functions and structs to test peersList()
// ---------------------------------------------- //

func createTestSysMultipleRegistrars(port string) components.System {
	sys := createTestSystem()
	sys.Husk.CoreS = []*components.CoreSystem{}
	for num := range 5 {
		reg := &components.CoreSystem{
			Name: "serviceregistrar",
			Url:  fmt.Sprintf("http://localhost:%s/%d", port, num),
		}
		sys.Husk.CoreS = append(sys.Husk.CoreS, reg)
	}
	return sys
}

func createTestSysBrokenRegistrarURL() components.System {
	sys := createTestSystem()
	sys.Husk.CoreS = []*components.CoreSystem{}

	reg := &components.CoreSystem{
		Name: "serviceregistrar",
		Url:  string(rune(0)),
	}
	sys.Husk.CoreS = append(sys.Husk.CoreS, reg)

	return sys
}

type peersListParams struct {
	expectError bool
	setup       func() components.System
	testCase    string
}

func TestPeersList(t *testing.T) {
	params := []peersListParams{
		{
			false,
			func() (sys components.System) { return createTestSystem() },
			"Good case, one registrar",
		},
		{
			false,
			func() (sys components.System) { return createTestSysMultipleRegistrars("1234") },
			"Good case, multiple registrars",
		},
		{
			false,
			func() (sys components.System) { return createTestSysMultipleRegistrars("") },
			"Bad case, port missing",
		},
		{
			false,
			func() (sys components.System) { return createTestSysMultipleRegistrars("8870") },
			"Bad case, port same as http in husk",
		},
		{
			true,
			func() (sys components.System) { return createTestSysBrokenRegistrarURL() },
			"Bad case, can't parse url",
		},
	}

	for _, c := range params {
		sys := c.setup()
		_, err := peersList(&sys)
		if (c.expectError == false) && (err != nil) {
			t.Errorf("Expected no errors in '%s', got: %v", c.testCase, err)
		}
		if (c.expectError == true) && (err == nil) {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}
	}
}

// ----------------------------------------------- //
// Help functions and structs to test systemList()
// ----------------------------------------------- //

func createFilledRegistrar() *Traits {
	ua := createLeadingRegistrar()
	ua.serviceRegistry = make(map[int]forms.ServiceRecord_v1)
	for x := range 5 {
		ua.serviceRegistry[x] = forms.ServiceRecord_v1{
			Id:          x,
			SystemName:  fmt.Sprintf("testSys%d", x),
			IPAddresses: []string{"localhost"},
			ProtoPort:   map[string]int{"http": 1234},
		}
	}
	return ua
}

type expectedBody struct {
	List    []string `json:"systemurl"`
	Version string   `json:"version"`
}

type systemListParams struct {
	expectedStatuscode int
	setup              func() *Traits
	request            *http.Request
	testCase           string
}

func TestSystemList(t *testing.T) {
	params := []systemListParams{
		{
			200,
			func() *Traits { return createFilledRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost", nil),
			"Best case",
		},
		{
			405,
			func() *Traits { return createFilledRegistrar() },
			httptest.NewRequest(http.MethodPost, "http://localhost", nil),
			"Bad case, unsupported http method",
		},
	}

	for _, c := range params {
		tr := c.setup()
		w := httptest.NewRecorder()
		r := c.request

		tr.systemList(w, r)
		res := w.Result()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Errorf("Failed while reading response body")
		}

		var jsonData expectedBody
		// Only unmarshal the data if it's a successful request
		if res.StatusCode == 200 {
			err = json.Unmarshal(data, &jsonData)
			if err != nil {
				t.Errorf("Failed while unmarshalling data")
			}
		}

		if (res.StatusCode == 200) && (len(jsonData.List) != 5) {
			t.Errorf("Expected status code '%d' and length of list '%d' got: '%d' and '%d'",
				c.expectedStatuscode, 5, res.StatusCode, len(jsonData.List))
		}

		if c.expectedStatuscode == 405 && res.Status != "405 Method Not Allowed" {
			t.Errorf("Expected '405 Method Not Allowed' as Status, got: %v", res.Status)
		}
	}
}

// ----------------------------------------------- //
// Help functions and structs to test updateDB()
// ----------------------------------------------- //

func createSpecialRequest(statusCode int, method string) *http.Request {
	if statusCode == 200 {
		rec := &forms.ServiceRecord_v1{
			Id:      0,
			Version: "ServiceRecord_v1",
		}

		data, _ := json.Marshal(rec)
		body := io.NopCloser(bytes.NewReader(data))
		return httptest.NewRequest(method, "http://localhost/reg", body)
	} else {
		rec := &forms.ServiceRecord_v1{
			Id:                int(0),
			ServiceDefinition: "test",
			SystemName:        "System",
			ServiceNode:       "node",
			IPAddresses:       []string{"123.456.789.012"},
			ProtoPort:         map[string]int{"http": 1234},
			Details:           map[string][]string{"details": {}},
			Certificate:       "ABCD",
			SubPath:           "testPath",
			RegLife:           25,
			Version:           "SignalA_v1.0",
			Created:           "",
			Updated:           time.Now().String(),
			EndOfValidity:     time.Now().Add(25 * time.Second).String(),
			SubscribeAble:     false,
			ACost:             float64(0),
			CUnit:             "",
		}
		data, _ := json.Marshal(rec)
		body := io.NopCloser(bytes.NewReader(data))
		return httptest.NewRequest(method, "http://localhost/reg", body)
	}
}

type updateDBParams struct {
	expectedStatuscode int
	leading            bool
	body               io.ReadCloser
	method             string
	testCase           string
}

func TestUpdateDB(t *testing.T) {
	params := []updateDBParams{
		{
			http.StatusServiceUnavailable,
			false,
			io.NopCloser(strings.NewReader("TestBody")),
			http.MethodPut,
			"Bad case, not leading registrar",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(strings.NewReader("TestBody")),
			http.MethodPut,
			"Bad case, wrong content type in request",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(errReader(0)),
			http.MethodPut,
			"Bad case, can't read body",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(strings.NewReader("")),
			http.MethodPut,
			"Bad case, can't unpack body",
		},
		{
			http.StatusInternalServerError,
			true,
			nil,
			http.MethodPut,
			"Bad case, request returns error",
		},
		{
			200,
			true,
			nil,
			http.MethodPost,
			"Good case, everything passes",
		},
		{
			200,
			true,
			io.NopCloser(strings.NewReader("")),
			http.MethodGet,
			"Good case, default case",
		},
	}

	for _, c := range params {
		// Setup
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua := temp.Traits.(*Traits)
		ua.role.Store(&registrarRole{leading: c.leading})
		w := httptest.NewRecorder()
		var r *http.Request
		if c.body == nil {
			r = createSpecialRequest(c.expectedStatuscode, c.method)
		} else {
			r = httptest.NewRequest(c.method, "http://localhost/reg", c.body)
		}

		r.Header = map[string][]string{"Content-Type": {"application/json"}}

		// Test and checks
		ua.updateDB(w, r)

		if w.Result().StatusCode != c.expectedStatuscode {
			t.Errorf("Expected statuscode %d, got: %d in '%s'",
				c.expectedStatuscode, w.Result().StatusCode, c.testCase)
		}

		shutdown()
	}
}

// ----------------------------------------------- //
// Help functions and structs to test queryDB()
// ----------------------------------------------- //

type queryDBParams struct {
	expectedStatuscode int
	leading            bool
	body               io.ReadCloser
	method             string
	header             map[string][]string
	testCase           string
}

func TestQueryDB(t *testing.T) {
	params := []queryDBParams{
		{
			http.StatusOK,
			true,
			io.NopCloser(strings.NewReader("{}")),
			http.MethodGet,
			map[string][]string{"Content-Type": {"application/json"}},
			"Good case GET, everything passes",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(strings.NewReader("{}")),
			http.MethodPost,
			map[string][]string{},
			"Bad case POST, can't parse Content-Type from header",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(errReader(0)),
			http.MethodPost,
			map[string][]string{"Content-Type": {"application/json"}},
			"Bad case POST, error while reading body",
		},
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(strings.NewReader("{}")),
			http.MethodPost,
			map[string][]string{"Content-Type": {"application/json"}},
			"Bad case POST, error while unpacking body",
		},
		{
			http.StatusInternalServerError,
			true,
			io.NopCloser(strings.NewReader(`{"id": 0, "version":"SignalA_v1.0"}`)),
			http.MethodPost,
			map[string][]string{"Content-Type": {"application/json"}},
			"Bad case POST, request returns error",
		},
		{
			http.StatusOK,
			true,
			io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`)),
			http.MethodPost,
			map[string][]string{"Content-Type": {"application/json"}},
			"Good case POST, request returns a result",
		},
		{
			http.StatusMethodNotAllowed,
			true,
			io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`)),
			http.MethodDelete,
			map[string][]string{"Content-Type": {"application/json"}},
			"Bad case default, unsupported http method",
		},
	}

	for _, c := range params {
		// Setup
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua := temp.Traits.(*Traits)
		ua.role.Store(&registrarRole{leading: c.leading})
		w := httptest.NewRecorder()
		r := httptest.NewRequest(c.method, "http://localhost/reg", c.body)
		r.Header = c.header

		sendAddRequest(0, "test", "testPath", "", ua.requests)

		// Test and checks
		ua.queryDB(w, r)

		if w.Result().StatusCode != c.expectedStatuscode {
			t.Errorf("Expected statuscode %d, got: %d in '%s'",
				c.expectedStatuscode, w.Result().StatusCode, c.testCase)
		}

		shutdown()
	}
}

// ----------------------------------------------- //
// Tests for renderListItems()
// ----------------------------------------------- //

func TestRenderListItems(t *testing.T) {
	services := []forms.ServiceRecord_v1{
		{Id: 3, SystemName: "sysC", SubPath: "svcC/pathC", IPAddresses: []string{"10.0.0.3"}, ProtoPort: map[string]int{"http": 8083}},
		{Id: 1, SystemName: "sysA", SubPath: "svcA/pathA", IPAddresses: []string{"10.0.0.1"}, ProtoPort: map[string]int{"http": 8081}},
		{Id: 2, SystemName: "sysB", SubPath: "svcB/pathB", IPAddresses: []string{"10.0.0.2"}, ProtoPort: map[string]int{"http": 8082}},
	}

	result := renderListItems(services)

	if !strings.Contains(result, "Service ID: 1") || !strings.Contains(result, "Service ID: 2") || !strings.Contains(result, "Service ID: 3") {
		t.Error("Expected all three Service IDs in result")
	}

	pos1 := strings.Index(result, "Service ID: 1")
	pos2 := strings.Index(result, "Service ID: 2")
	pos3 := strings.Index(result, "Service ID: 3")
	if !(pos1 < pos2 && pos2 < pos3) {
		t.Errorf("Expected services sorted by ID (1 < 2 < 3), got positions: %d, %d, %d", pos1, pos2, pos3)
	}

	if !strings.HasPrefix(result, "<li>") {
		t.Error("Expected result to start with <li>")
	}
}

// renderListItems must show every configured protocol per service. HTTP is
// rendered as a clickable link (browsers can hit it); HTTPS is rendered as a
// non-clickable label because the framework's mTLS requirement excludes
// browser-only clients. Ports of 0 must be skipped entirely.
func TestRenderListItemsProtocols(t *testing.T) {
	services := []forms.ServiceRecord_v1{
		{
			Id: 1, SystemName: "ca", SubPath: "certification/certify",
			IPAddresses:       []string{"10.0.0.33"},
			ProtoPort:         map[string]int{"http": 20100, "https": 30100, "coap": 0},
			ServiceDefinition: "certify",
		},
		{
			Id: 2, SystemName: "secret", SubPath: "vault/get",
			IPAddresses:       []string{"10.0.0.99"},
			ProtoPort:         map[string]int{"http": 0, "https": 30200},
			ServiceDefinition: "fetch",
		},
	}

	result := renderListItems(services)

	// Service 1 is reachable on both HTTP and HTTPS.
	if !strings.Contains(result, `href="http://10.0.0.33:20100/ca/certification/certify"`) {
		t.Error("HTTP endpoint for service 1 missing or malformed")
	}
	if !strings.Contains(result, `https://10.0.0.33:30100/ca/certification/certify`) {
		t.Error("HTTPS endpoint for service 1 missing")
	}
	// HTTPS must NOT be inside an <a href="..."> — clicking it would fail mTLS.
	if strings.Contains(result, `href="https://`) {
		t.Error("HTTPS endpoints must not be rendered as clickable <a> links")
	}

	// Service 2 is HTTPS-only; no HTTP link should be rendered.
	if strings.Contains(result, "http://10.0.0.99:0") {
		t.Error("Port-0 HTTP must be skipped, not rendered as :0")
	}
	if !strings.Contains(result, "https://10.0.0.99:30200/secret/vault/get") {
		t.Error("HTTPS endpoint for service 2 missing")
	}
}

// ----------------------------------------------- //
// Tests for notify()
// ----------------------------------------------- //

func TestNotify(t *testing.T) {
	record := func() forms.ServiceRecord_v1 {
		var rec forms.ServiceRecord_v1
		rec.NewForm()
		rec.SystemName = "ds18b20"
		rec.ServiceDefinition = "temperature"
		return rec
	}

	t.Run("delivers the event", func(t *testing.T) {
		tr := &Traits{subscribers: make(map[int]*subscriber)}
		sub := &subscriber{events: make(chan forms.RegistryEvent_v1, 1)}
		tr.subscribers[1] = sub

		tr.notify(forms.RegistryRegistered, record())

		select {
		case got := <-sub.events:
			if got.Change != forms.RegistryRegistered {
				t.Errorf("change = %q, want %q", got.Change, forms.RegistryRegistered)
			}
			if got.Record.SystemName != "ds18b20" {
				t.Errorf("the event does not say what changed: %+v", got.Record)
			}
			if got.Timestamp == "" {
				t.Error("the event carries no timestamp")
			}
		default:
			t.Error("the subscriber was not told")
		}
	})

	t.Run("no subscribers is a no-op", func(t *testing.T) {
		tr := &Traits{subscribers: make(map[int]*subscriber)}
		tr.notify(forms.RegistryRegistered, record()) // must not panic
	})

	t.Run("a full subscriber is told to resync rather than blocking the registry", func(t *testing.T) {
		tr := &Traits{subscribers: make(map[int]*subscriber)}
		sub := &subscriber{events: make(chan forms.RegistryEvent_v1, 1)}
		sub.events <- forms.RegistryEvent_v1{} // pre-fill
		tr.subscribers[1] = sub

		done := make(chan struct{})
		go func() { tr.notify(forms.RegistryRegistered, record()); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("notify blocked on a full subscriber; one slow consumer would stall every registration")
		}

		// The event could not be delivered, so it must not be silently lost:
		// the subscriber is told its stream no longer describes the registry.
		if !sub.resync.Load() {
			t.Error("an undeliverable event was dropped without asking the subscriber to re-read")
		}
	})
}

// ----------------------------------------------- //
// Tests for SSE path in queryDB()
// ----------------------------------------------- //

func TestQueryDBGetSSE(t *testing.T) {
	sys := createTestSystem()
	confAsset := createConfAssetMultipleTraits()
	temp, shutdown := newResource(confAsset, &sys)
	defer shutdown()
	ua := temp.Traits.(*Traits)
	ua.role.Store(&registrarRole{leading: true})

	ctx, cancel := context.WithCancel(context.Background())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://localhost/query", nil).WithContext(ctx)
	r.Header = map[string][]string{"Accept": {"text/event-stream"}}

	done := make(chan struct{})
	go func() {
		ua.queryDB(w, r)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not exit after context cancellation")
	}

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got: %s", ct)
	}
	if !strings.Contains(w.Body.String(), "data:") {
		t.Errorf("Expected 'data:' in SSE response body, got: %s", w.Body.String())
	}
}

// ----------------------------------------------- //
// Help functions and structs to test cleanDB()
// ----------------------------------------------- //

type cleanDBParams struct {
	expectedStatuscode int
	leading            bool
	body               io.ReadCloser
	method             string
	testCase           string
}

func TestCleanDB(t *testing.T) {
	params := []cleanDBParams{
		{
			http.StatusBadRequest,
			true,
			io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`)),
			http.MethodDelete,
			"Bad case DELETE, couldn't convert id to int",
		},
		{
			200,
			true,
			io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`)),
			http.MethodGet,
			"Bad case default, unsupported http method",
		},
	}

	for _, c := range params {
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua := temp.Traits.(*Traits)
		ua.role.Store(&registrarRole{leading: c.leading})

		w := httptest.NewRecorder()
		r := httptest.NewRequest(c.method, "http://localhost/reg/a", c.body)
		r.Header = map[string][]string{"Content-Type": {"application/json"}}
		sendAddRequest(0, "test", "testPath", "", ua.requests)

		// Test and checks
		ua.cleanDB(w, r)

		if w.Result().StatusCode != c.expectedStatuscode {
			t.Errorf("Expected statuscode %d, got: %d in '%s'",
				c.expectedStatuscode, w.Result().StatusCode, c.testCase)
		}

		shutdown()
	}
}

// TestTheRegistryPageShowsAQudtUnit is the bug an operator sees as missing data.
//
// A QUDT identifier is written <http://qudt.org/vocab/unit/DEG_C>. Interpolated
// into HTML unescaped, a browser reads it as an unknown tag and renders nothing,
// so the page showed "Unit: []" and "QuantityKind: []" on every service in the
// cloud — not missing values, swallowed ones. The registry held them all along,
// which is what made it look like a registration fault rather than a display
// one.
func TestTheRegistryPageShowsAQudtUnit(t *testing.T) {
	services := []forms.ServiceRecord_v1{{
		Id: 1, SystemName: "thermostat", SubPath: "controller_1/setpoint",
		ServiceDefinition: "setpoint",
		IPAddresses:       []string{"10.0.0.1"}, ProtoPort: map[string]int{"http": 8081},
		Details: map[string][]string{
			"Unit":         {"<http://qudt.org/vocab/unit/DEG_C>"},
			"QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"},
		},
	}}

	page := renderListItems(services)

	// Escaped, so the browser prints the identifier instead of hunting for a
	// closing tag.
	if !strings.Contains(page, "&lt;http://qudt.org/vocab/unit/DEG_C&gt;") {
		t.Errorf("the unit is not rendered where a browser will show it:\n%s", page)
	}
	if !strings.Contains(page, "&lt;http://qudt.org/vocab/quantitykind/ThermodynamicTemperature&gt;") {
		t.Errorf("the quantity kind is not rendered where a browser will show it:\n%s", page)
	}
	// The raw form is what disappeared.
	if strings.Contains(page, "<http://qudt.org") {
		t.Error("an identifier is still written as a tag, so the browser will eat it")
	}
}

// A detail is whatever some system registered, and this page is opened by the
// person commissioning the cloud. Anything registered must be shown, not run.
func TestTheRegistryPageDoesNotRunWhatWasRegistered(t *testing.T) {
	services := []forms.ServiceRecord_v1{{
		Id: 1, SystemName: `sys"><script>alert(1)</script>`, SubPath: "asset/svc",
		ServiceDefinition: "definition",
		IPAddresses:       []string{`10.0.0.1"><script>alert(2)</script>`},
		ProtoPort:         map[string]int{"http": 8081},
		Details:           map[string][]string{"Note": {"<script>alert(3)</script>"}},
	}}

	page := renderListItems(services)

	if strings.Contains(page, "<script>") {
		t.Errorf("a registered value reached the page as markup, so it runs in the "+
			"browser of whoever opens the registry:\n%s", page)
	}
	// The values are still shown — escaping displays them, it does not drop them.
	if !strings.Contains(page, "&lt;script&gt;alert(3)&lt;/script&gt;") {
		t.Errorf("the detail was not displayed at all:\n%s", page)
	}
}
