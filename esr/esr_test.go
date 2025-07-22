package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	uac := &UnitAsset{
		Name:        "testRegistrar",
		Details:     map[string][]string{"testDetail": {"detail1", "detail2"}},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:      true,
			leadingSince: time.Now(),
		},
	}
	return uac
}

func createNonLeadingRegistrar() *UnitAsset {
	uac := &UnitAsset{
		Name:        "testRegistrar",
		Details:     map[string][]string{"testDetail": {"detail1", "detail2"}},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:          false,
			leadingRegistrar: &components.CoreSystem{Name: "otherRegistrar", Url: "otherURL"},
		},
	}
	return uac
}

func createServiceUnavailableRegistrar() *UnitAsset {
	uac := &UnitAsset{
		Name:        "testRegistrar",
		Details:     map[string][]string{"testDetail": {"detail1", "detail2"}},
		ServicesMap: components.Services{},
		Traits: Traits{
			leading:          false,
			leadingRegistrar: nil,
		},
	}
	return uac
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
		ua.serviceRegistry[x] = forms.ServiceRecord_v1{Id: x, SystemName: fmt.Sprintf("testSys%d", x),
			IPAddresses: []string{"localhost"}, ProtoPort: map[string]int{"http": 1234}}
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

type updateDBParams struct {
	expectedStatuscode int
	setup              func(*UnitAsset) (*httptest.ResponseRecorder, *http.Request)
	testCase           string
}

func TestUpdateDB(t *testing.T) {
	params := []updateDBParams{
		{
			http.StatusServiceUnavailable,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = false
				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader("TestBody"))
				r = httptest.NewRequest(http.MethodPut, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}
				return w, r
			},
			"Bad case, not leading registrar",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true
				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader("TestBody"))
				r = httptest.NewRequest(http.MethodPut, "http://localhost/reg", body)
				return w, r
			},
			"Bad case, wrong content type in request",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true
				w = httptest.NewRecorder()
				body := io.NopCloser(errReader(0))
				r = httptest.NewRequest(http.MethodPut, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}
				return w, r
			},
			"Bad case, can't read body",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true
				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(""))
				r = httptest.NewRequest(http.MethodPut, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}
				return w, r
			},
			"Bad case, can't unpack body",
		},
		{
			http.StatusInternalServerError,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()

				// Record has to match the one sent by sendAddRequest(..)
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
				data, err := json.Marshal(rec)
				if err != nil {
					log.Printf("Error: %v", err)
				}
				body := io.NopCloser(bytes.NewReader(data))
				r = httptest.NewRequest(http.MethodPut, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}
				sendAddRequest(0, "test", "testPath", "", ua.requests)

				return w, r
			},
			"Bad case, request returns error",
		},
		{
			200,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				rec := &forms.ServiceRecord_v1{
					Id:      0,
					Version: "ServiceRecord_v1",
				}

				data, _ := json.Marshal(rec)
				w = httptest.NewRecorder()
				body := io.NopCloser(bytes.NewReader(data))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Good case, everything passes",
		},
		{
			200,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true
				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(""))
				r = httptest.NewRequest(http.MethodGet, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
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

		w, r := c.setup(ua)

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
	setup              func(*UnitAsset) (*httptest.ResponseRecorder, *http.Request)
	testCase           string
}

func TestQueryDB(t *testing.T) {
	params := []queryDBParams{
		{
			http.StatusOK,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader("{}"))
				r = httptest.NewRequest(http.MethodGet, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Good case GET, everything passes",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader("{}"))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{}

				return w, r
			},
			"Bad case POST, can't parse Content-Type from header",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(errReader(0))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case POST, error while reading body",
		},
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader("{}"))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case POST, error while unpacking body",
		},
		{
			http.StatusInternalServerError,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(`{"id": 0, "version":"SignalA_v1.0"}`))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case POST, request returns error",
		},
		{
			http.StatusOK,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`))
				r = httptest.NewRequest(http.MethodPost, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Good case POST, request returns a result",
		},
		{
			http.StatusMethodNotAllowed,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`))
				r = httptest.NewRequest(http.MethodDelete, "http://localhost/reg", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case default, unsupported http method",
		},
	}

	for _, c := range params {
		var ua *UnitAsset
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua = temp.(*UnitAsset)
		sendAddRequest(0, "test", "testPath", "", ua.requests)

		w, r := c.setup(ua)

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
	setup              func(*UnitAsset) (*httptest.ResponseRecorder, *http.Request)
	testCase           string
}

func TestCleanDB(t *testing.T) {
	params := []cleanDBParams{
		{
			http.StatusBadRequest,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`))
				r = httptest.NewRequest(http.MethodDelete, "http://localhost/reg/a", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case DELETE, couldn't convert id to int",
		},
		{
			200,
			func(ua *UnitAsset) (w *httptest.ResponseRecorder, r *http.Request) {
				ua.leading = true

				w = httptest.NewRecorder()
				body := io.NopCloser(strings.NewReader(`{"id": 0, "version":"ServiceQuest_v1"}`))
				r = httptest.NewRequest(http.MethodGet, "http://localhost/reg/a", body)
				r.Header = map[string][]string{"Content-Type": {"application/json"}}

				return w, r
			},
			"Bad case default, unsupported http method",
		},
	}

	for _, c := range params {
		var ua *UnitAsset
		sys := createTestSystem()
		confAsset := createConfAssetMultipleTraits()
		temp, shutdown := newResource(confAsset, &sys)
		ua = temp.(*UnitAsset)
		sendAddRequest(0, "test", "testPath", "", ua.requests)

		w, r := c.setup(ua)

		// Test and checks
		ua.cleanDB(w, r)

		if w.Result().StatusCode != c.expectedStatuscode {
			t.Errorf("Expected statuscode %d, got: %d in '%s'",
				c.expectedStatuscode, w.Result().StatusCode, c.testCase)
		}

		shutdown()
	}
}
