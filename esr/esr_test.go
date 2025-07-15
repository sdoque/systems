package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// ----------------------------------------------- //
// Help functions and structs to test roleStatus()
// ----------------------------------------------- //

func createLeadingRegistrar() UnitAsset {
	uac := UnitAsset{
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

func createNonLeadingRegistrar() UnitAsset {
	uac := UnitAsset{
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

func createServiceUnavailableRegistrar() UnitAsset {
	uac := UnitAsset{
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
	setup              func() UnitAsset
	request            *http.Request
	testCase           string
}

func TestRoleStatus(t *testing.T) {
	params := []roleStatusParams{
		{
			200,
			func() UnitAsset { return createLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() UnitAsset { return createNonLeadingRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Good case, leading registrar",
		},
		{
			503,
			func() UnitAsset { return createServiceUnavailableRegistrar() },
			httptest.NewRequest(http.MethodGet, "http://localhost/test", nil),
			"Bad case, service unavailable",
		},
		{
			200,
			func() UnitAsset { return UnitAsset{} },
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

// func (ua *UnitAsset) systemList(w http.ResponseWriter, r *http.Request) {
