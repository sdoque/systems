package main

import (
	"fmt"
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// ----------------------------------------------------- //
// Help functions and structs to tests initTemplate()
// ----------------------------------------------------- //

func TestInitTemplate(t *testing.T) {
	expectedServices := []string{"register", "query", "unregister", "status"}

	ua := initTemplate()
	services := ua.GetServices()

	// Check if expected name and services are present
	if ua.GetName() != "registry" {
		t.Errorf("Name mismatch expected 'registry', got: %s", ua.GetName())
	}

	for _, s := range expectedServices {
		if _, ok := services[s]; !ok {
			t.Errorf("Expected service '%s' to be present", s)
		}
	}
}

// --------------------------------------------- //
// Help functions and structs to test ***
// --------------------------------------------- //

// newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {...}
func TestNewResource(t *testing.T) {}

// UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {}
func TestUnmarshalTraits(t *testing.T) {}

// (ua *UnitAsset) serviceRegistryHandler() {}
func TestServiceRegistryHandler(t *testing.T) {}

// ------------------------------------------------------------------------ //
// Help functions and structs to test FilterByServiceDefinitionAndDetails()
// ------------------------------------------------------------------------ //

// Creates an asset multiple services in its registry
func createRegistryWithServices(broken bool) (ua *UnitAsset, err error) {
	initTemp := initTemplate()
	ua, ok := initTemp.(*UnitAsset)
	if !ok {
		return nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}
	var locations []string
	locations = []string{"Kitchen", "Bathroom", "Livingroom"}

	ua.serviceRegistry = make(map[int]forms.ServiceRecord_v1)
	for i, location := range locations {
		var form forms.ServiceRecord_v1
		form.ServiceDefinition = "testDef"
		form.SystemName = fmt.Sprintf("testSystem%d", i)
		form.ProtoPort = map[string]int{"http": i}
		form.IPAddresses = []string{fmt.Sprintf("999.999.%d.999", i)}
		form.EndOfValidity = "2026-01-02T15:04:05Z"
		form.Details = make(map[string][]string)
		if !broken {
			form.Details = map[string][]string{"Location": {location}}
		}
		ua.serviceRegistry[i] = form
	}
	return ua, nil
}

type filterByServDefAndDetailsParams struct {
	expectMatch bool
	setup       func() (*UnitAsset, error)
	testCase    string
}

func TestFilterByServiceDefAndDetails(t *testing.T) {
	params := []filterByServDefAndDetailsParams{
		{
			true,
			func() (ua *UnitAsset, err error) { return createRegistryWithServices(false) },
			"Best case",
		},
		{
			false,
			func() (ua *UnitAsset, err error) { return createRegistryWithServices(true) },
			"Bad case, key doesn't exist",
		},
	}

	for _, c := range params {
		ua, err := c.setup()
		if err != nil {
			t.Errorf("Failed during setup in '%s'", c.testCase)
		}
		checkLoc := map[string][]string{"Location": {"Livingroom"}}
		lst := ua.FilterByServiceDefinitionAndDetails("testDef", checkLoc)
		if (c.expectMatch == true) && (len(lst) < 1) {
			t.Errorf("Expected atleast 1 service")
		}
		if (c.expectMatch == false) && (len(lst) > 0) {
			t.Errorf("Expected no matches")
		}
	}
}

// ---------------------------------------------------- //
// Help functions and structs to test checkExpiration()
// ---------------------------------------------------- //

func createRegistryWithService(year any) (ua *UnitAsset, err error) {
	initTemp := initTemplate()
	ua, ok := initTemp.(*UnitAsset)
	if !ok {
		return nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"http": 1234}
	test.IPAddresses = []string{"999.999.999.999"}
	test.EndOfValidity = fmt.Sprintf("%v-01-02T15:04:05Z", year)
	ua.serviceRegistry = map[int]forms.ServiceRecord_v1{0: test}
	return ua, nil
}

type checkExpirationParams struct {
	servicePresent bool
	setup          func() (*UnitAsset, error)
	testCase       string
}

func TestCheckExpiration(t *testing.T) {
	params := []checkExpirationParams{
		{
			true,
			func() (ua *UnitAsset, err error) { return createRegistryWithService(2026) },
			"Best case, service not past expiration",
		},
		{
			false,
			func() (ua *UnitAsset, err error) { return createRegistryWithService(2006) },
			"Bad case, service past expiration",
		},
		{
			true,
			func() (ua *UnitAsset, err error) { return createRegistryWithService("faulty") },
			"Bad case, time parsing problem",
		},
	}
	for _, c := range params {
		ua, err := c.setup()
		if err != nil {
			t.Errorf("failed during setup: %v", err)
		}

		checkExpiration(ua, 0)
		if _, exists := ua.serviceRegistry[0]; (exists == false) && (c.servicePresent == true) {
			t.Errorf("expected the service to be present in '%s'", c.testCase)
		}
		if _, exists := ua.serviceRegistry[0]; (exists == true) && (c.servicePresent == false) {
			t.Errorf("expected service to be removed in '%s'", c.testCase)
		}
	}
}

// ----------------------------------------------------- //
// Help functions and structs to test getUniqueSystems()
// ----------------------------------------------------- //

func createServRegistryHttp() (ua *UnitAsset, err error) {
	initTemp := initTemplate()
	ua, ok := initTemp.(*UnitAsset)
	if !ok {
		return nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"http": 1234}
	test.IPAddresses = []string{"999.999.999.999"}
	ua.serviceRegistry = map[int]forms.ServiceRecord_v1{0: test}

	return ua, nil
}

func createServRegistryHttps() (ua *UnitAsset, err error) {
	initTemp := initTemplate()
	ua, ok := initTemp.(*UnitAsset)
	if !ok {
		return nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"https": 4321}
	test.IPAddresses = []string{"888.888.888.888"}
	ua.serviceRegistry = map[int]forms.ServiceRecord_v1{0: test}

	return ua, nil
}

func createBrokenServRegistry() (ua *UnitAsset, err error) {
	initTemp := initTemplate()
	ua, ok := initTemp.(*UnitAsset)
	if !ok {
		return nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"https": 0}
	test.IPAddresses = []string{"888.888.888.888"}
	ua.serviceRegistry = map[int]forms.ServiceRecord_v1{0: test}
	return ua, nil
}

type getUniqueSystemsParams struct {
	expectError bool
	setup       func() (ua *UnitAsset, err error)
	testCase    string
}

func TestGetUniqueSystems(t *testing.T) {
	params := []getUniqueSystemsParams{
		{
			false,
			func() (ua *UnitAsset, err error) { return createServRegistryHttp() },
			"Best case, http",
		},
		{
			false,
			func() (ua *UnitAsset, err error) { return createServRegistryHttps() },
			"Best case, https",
		},
		{
			false,
			func() (ua *UnitAsset, err error) { return createBrokenServRegistry() },
			"Bad case, http/https not found",
		},
	}

	for _, c := range params {
		ua, err := c.setup()
		if err != nil {
			t.Errorf("Failed during setup in '%s' with error: %v", c.testCase, err)
		}
		_, err = getUniqueSystems(ua)
		if c.expectError == false && err != nil {
			t.Errorf("Failed while getting unique systems in '%s': %v", c.testCase, err)
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}
	}
}
