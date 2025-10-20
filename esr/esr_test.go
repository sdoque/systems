package main

import (
	"bytes"
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

func createLeadingRegistrar() *UnitAsset {
	return &UnitAsset{
		Name: "testRegistrar",
		Details: map[string][]string{
			"testDetail": []string{"detail1", "detail2"},
		},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:      true,
			leadingSince: time.Now(),
		},
	}
}

func createNonLeadingRegistrar() *UnitAsset {
	return &UnitAsset{
		Name: "testRegistrar",
		Details: map[string][]string{
			"testDetail": []string{"detail1", "detail2"},
		},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:          false,
			leadingRegistrar: &components.CoreSystem{Name: "otherRegistrar", Url: "otherURL"}, // or URL if your field is URL
		},
	}
}

func createServiceUnavailableRegistrar() *UnitAsset {
	return &UnitAsset{
		Name: "testRegistrar",
		Details: map[string][]string{
			"testDetail": []string{"detail1", "detail2"},
		},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:          false,
			leadingRegistrar: nil,
		},
	}
}

type roleStatusParams struct {
	expectedStatuscode int
	setup              func() *UnitAsset
	request            *http.Request
	testCase           string
}

func TestRoleStatus(t *testing.T) {
	params := []roleStatusParams{
		{
			200,
			func() *UnitAsset { return createLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() *UnitAsset { return createNonLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() *UnitAsset { return createServiceUnavailableRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Bad case, service unavailable",
		},
		{
			200,
			func() *UnitAsset { return &UnitAsset{} },
			httptest.NewRequest(http.MethodPost, "http://localhost/test", nil),
			"Bad case, unsupported http method",
		},
	}
	for _, c := range params {
		ua := c.setup()
		w := httptest.NewRecorder()
		r := c.request

		ua.roleStatus(w, r)
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
	sys.CoreS = []*components.CoreSystem{}
	for num := range 5 {
		reg := &components.CoreSystem{
			Name: "serviceregistrar",
			Url:  fmt.Sprintf("http://localhost:%s/%d", port, num),
		}
		sys.CoreS = append(sys.CoreS, reg)
	}
	return sys
}

func createTestSysBrokenRegistrarURL() components.System {
	sys := createTestSystem()
	sys.CoreS = []*components.CoreSystem{}

	reg := &components.CoreSystem{
		Name: "serviceregistrar",
		Url:  string(rune(0)),
	}
	sys.CoreS = append(sys.CoreS, reg)

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

func createFilledRegistrar() *UnitAsset {
	ua := createLeadingRegistrar()
	ua.serviceRegistry = make(map[int]forms.ServiceRecord_v1)
	var serviceAmount int
	for x := range 5 {
		serviceAmount++
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
	setup              func() *UnitAsset
	request            *http.Request
	testCase           string
}

func TestSystemList(t *testing.T) {
	params := []systemListParams{
		{
			200,
			func() *UnitAsset { return createFilledRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost", nil),
			"Best case",
		},
		{
			405,
			func() *UnitAsset { return createFilledRegistrar() },
			httptest.NewRequest(http.MethodPost, "http://localhost", nil),
			"Bad case, unsupported http method",
		},
	}

	for _, c := range params {
		ua := c.setup()
		w := httptest.NewRecorder()
		r := c.request

		ua.systemList(w, r)
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
		var ua *UnitAsset
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua = temp.(*UnitAsset)
		ua.leading = c.leading
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
		var ua *UnitAsset
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua = temp.(*UnitAsset)
		ua.leading = c.leading
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
		var ua *UnitAsset
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua = temp.(*UnitAsset)
		ua.leading = c.leading

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
