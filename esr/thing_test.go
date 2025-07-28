package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------------------ //
// Help functions and other goodies for testing
// ------------------------------------------------ //

// Create a error reader to break json.Unmarshal()
type errReader int

var errBodyRead error = fmt.Errorf("bad body read")

func (errReader) Read(p []byte) (n int, err error) {
	return 0, errBodyRead
}
func (errReader) Close() error {
	return nil
}

func createConfAssetMultipleTraits() usecases.ConfigurableAsset {
	uac := usecases.ConfigurableAsset{
		Name:     "testRegistrar",
		Details:  map[string][]string{"testDetail": {"detail1", "detail2"}},
		Services: []components.Service{},
		Traits:   []json.RawMessage{json.RawMessage(`{"recCount": 0}`), json.RawMessage(`{"leading": false}`)},
	}
	return uac
}

func createTestSystem() components.System {
	ctx := context.Background()
	sys := components.NewSystem("testsys", ctx)
	sys.Husk = &components.Husk{
		Description: " is for testing purposes",
		Certificate: "ABCD",
		Details:     map[string][]string{"Developer": {"Arrowhead"}},
		ProtoPort:   map[string]int{"https": 0, "http": 8870, "coap": 0},
		InfoLink:    "https://for.testing.purposes",
	}
	leadingRegistrar := &components.CoreSystem{
		Name: components.ServiceRegistrarName,
		Url:  "https://leadingregistrar:1234",
	}
	orchestrator := &components.CoreSystem{
		Name: "orchestrator",
		Url:  "https://orchestator:1234",
	}
	sys.CoreS = []*components.CoreSystem{
		leadingRegistrar,
		orchestrator,
	}
	return sys
}

// --------------------------------------------------------------------------- //
// Help functions and structs to test the add part of serviceRegistryHandler()
// --------------------------------------------------------------------------- //

func createNewSys() components.System {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be cancelled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("serviceregistrar", ctx)

	// Instantiate the Capsule
	sys.Husk = &components.Husk{
		Description: "is an Arrowhead mandatory core system that keeps track of the currently available services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20102, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/esr",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	assetName := assetTemplate.GetName()
	sys.UAssets[assetName] = &assetTemplate
	return sys
}

func sendAddRequest(id int64, def string, subPath string, created string, ch chan ServiceRegistryRequest) error {
	rec := &forms.ServiceRecord_v1{
		Id:                int(id),
		ServiceDefinition: def,
		SystemName:        "System",
		ServiceNode:       "node",
		IPAddresses:       []string{"123.456.789.012"},
		ProtoPort:         map[string]int{"http": 1234},
		Details:           map[string][]string{"details": {}},
		Certificate:       "ABCD",
		SubPath:           subPath,
		RegLife:           25,
		Version:           "SignalA_v1a",
		Created:           created,
		Updated:           time.Now().String(),
		EndOfValidity:     time.Now().Add(25 * time.Second).String(),
		SubscribeAble:     false,
		ACost:             float64(id),
		CUnit:             "",
	}

	req := ServiceRegistryRequest{
		Action: "add",
		Record: rec,
		Error:  make(chan error),
	}

	ch <- req

	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

func sendBrokenAddRequest(num int64, ch chan ServiceRegistryRequest) error {
	rec := &forms.SignalA_v1a{}
	req := ServiceRegistryRequest{
		Action: "add",
		Record: rec,
		Id:     num,
		Error:  make(chan error),
	}
	ch <- req

	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

type serviceRegistryHandlerParams struct {
	expectError bool
	request     func(*UnitAsset) error
	testCase    string
}

func TestServiceRegistryHandlerAdd(t *testing.T) {
	params := []serviceRegistryHandlerParams{
		{
			false,
			func(ua *UnitAsset) error {
				return sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
			},
			"Best case, successful request",
		},
		{
			true,
			func(ua *UnitAsset) error { return sendBrokenAddRequest(0, ua.requests) },
			"Bad case, unable to convert to correct form",
		},
		{
			true,
			func(ua *UnitAsset) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef2", "subP", time.Now().Format(time.RFC3339), ua.requests)
				return err
			},
			"Bad case, exists with different service definition",
		},
		{
			true,
			func(ua *UnitAsset) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subPa", time.Now().Format(time.RFC3339), ua.requests)
				return err
			},
			"Bad case, exists with different subpath",
		},
		{
			true,
			func(ua *UnitAsset) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", "", ua.requests)
				return err
			},
			"Bad case, exists different creation time in updated record",
		},
		{
			true,
			func(ua *UnitAsset) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", time.Now().Add(1*time.Hour).Format(time.RFC3339), ch)
				return err
			},
			"Bad case, mismatch between db- and received created field",
		},
		{
			false,
			func(ua *UnitAsset) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				return err
			},
			"Good case, recCount has looped back to 0",
		},
		{
			false,
			func(ua *UnitAsset) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				return err
			},
			"Good case, updated db record",
		},
	}

	for _, c := range params {
		// Setup
		temp := createConfAssetMultipleTraits()
		sys := createNewSys()
		res, shutdown := newResource(temp, &sys)
		ua, _ := res.(*UnitAsset)

		// Test and check
		err := c.request(ua)

		if c.expectError == false && err != nil {
			t.Errorf("Expected no errors in '%s': %v", c.testCase, err)
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}
		shutdown()
	}
}

// --------------------------------------------------------------------------- //
// Help functions and structs to test the read part of serviceRegistryHandler()
// --------------------------------------------------------------------------- //

func sendAddRequestWithDetails(id int64, def string, subPath string, created string, ch chan ServiceRegistryRequest) error {
	rec := &forms.ServiceRecord_v1{
		Id:                int(id),
		ServiceDefinition: def,
		SystemName:        "System",
		ServiceNode:       "node",
		IPAddresses:       []string{"123.456.789.012"},
		ProtoPort:         map[string]int{"http": 1234},
		Details:           map[string][]string{"details": {}},
		Certificate:       "ABCD",
		SubPath:           subPath,
		RegLife:           25,
		Version:           "SignalA_v1a",
		Created:           created,
		Updated:           time.Now().String(),
		EndOfValidity:     time.Now().Add(25 * time.Second).String(),
		SubscribeAble:     false,
		ACost:             float64(id),
		CUnit:             "",
	}

	for x := range id {
		rec.Details["details"] = append(rec.Details["details"], fmt.Sprintf("detail%d", x+1))
	}

	req := ServiceRegistryRequest{
		Action: "add",
		Id:     0,
		Record: rec,
		Error:  make(chan error),
	}

	ch <- req
	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

// id 0 will return all items in service registry, any other will return items depending on details & definition
func sendReadRequest(id int64, def string, details []string, ch chan ServiceRegistryRequest) ([]forms.ServiceRecord_v1, error) {
	rec := &forms.ServiceQuest_v1{
		SysId:             999,
		RequesterName:     "requester",
		ServiceDefinition: def,
		Protocol:          "",
		Details:           map[string][]string{"details": details},
		Version:           "",
	}
	var req ServiceRegistryRequest
	if id == 0 {
		// Returns a specific
		req = ServiceRegistryRequest{
			Action: "read",
			Record: nil,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
	} else {
		// Returns full list of services
		req = ServiceRegistryRequest{
			Action: "read",
			Record: rec,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
	}

	ch <- req
	select {
	case err := <-req.Error:
		return nil, err
	case lst := <-req.Result:
		return lst, nil
	}
}

func sendBrokenReadRequest(ch chan ServiceRegistryRequest) ([]forms.ServiceRecord_v1, error) {
	rec := &forms.SignalA_v1a{}

	var req = ServiceRegistryRequest{
		Action: "read",
		Record: rec,
		Result: make(chan []forms.ServiceRecord_v1),
		Error:  make(chan error),
	}

	ch <- req
	select {
	case err := <-req.Error:
		return nil, err
	case lst := <-req.Result:
		return lst, nil
	}
}

type serviceRegistryHandlerReadParams struct {
	expectError bool
	expectedLen int
	request     func(ua *UnitAsset) ([]forms.ServiceRecord_v1, error)
	testCase    string
}

func TestServiceRegistryHandlerRead(t *testing.T) {
	params := []serviceRegistryHandlerReadParams{
		{
			false,
			1,
			func(ua *UnitAsset) ([]forms.ServiceRecord_v1, error) {
				return sendReadRequest(0, "", []string{""}, ua.requests)
			},
			"Best case, successful read request returning all items",
		},
		{
			false,
			1,
			func(ua *UnitAsset) ([]forms.ServiceRecord_v1, error) {
				return sendReadRequest(1, "test", []string{"detail6"}, ua.requests)
			},
			"Best case, successful read request returning specific items",
		},
		{
			true,
			0,
			func(ua *UnitAsset) ([]forms.ServiceRecord_v1, error) {
				return sendBrokenReadRequest(ua.requests)
			},
			"Bad case, wrong form",
		},
	}

	for _, c := range params {
		// Setup
		temp := createConfAssetMultipleTraits()
		sys := createNewSys()
		res, shutdown := newResource(temp, &sys)
		ua, _ := res.(*UnitAsset)
		time.Sleep(25 * time.Millisecond)
		// Add some services to the serviceregistrar with details: detail1 detail2 ... detailN
		sendAddRequestWithDetails(1, "test", "sub1", time.Now().Format(time.RFC3339), ua.requests)
		sendAddRequestWithDetails(4, "test", "sub2", time.Now().Format(time.RFC3339), ua.requests)
		sendAddRequestWithDetails(8, "test", "sub3", time.Now().Format(time.RFC3339), ua.requests)

		lst, err := c.request(ua)

		if c.expectError == false && err != nil && len(lst) != c.expectedLen {
			t.Errorf("Expected no errors in '%s', got: %v, with length of list: %d got %d",
				c.testCase, err, c.expectedLen, len(lst))
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}

		shutdown()
	}
}

// ------------------------------------------------------------------------ //
// Help functions and structs to test delete in serviceRegistryHandler()
// ------------------------------------------------------------------------ //

func sendDeleteRequest(id int, ch chan ServiceRegistryRequest) {
	ch <- ServiceRegistryRequest{
		Action: "delete",
		Id:     int64(id),
	}
}

func TestServiceRegistryHandlerDelete(t *testing.T) {
	// Setup
	temp := createConfAssetMultipleTraits()
	sys := createNewSys()
	res, shutdown := newResource(temp, &sys)
	ua, _ := res.(*UnitAsset)
	time.Sleep(25 * time.Millisecond)
	// Add a services to the serviceregistrar
	sendAddRequestWithDetails(1, "test", "sub1", time.Now().Format(time.RFC3339), ua.requests)

	sendDeleteRequest(0, ua.requests)

	shutdown()
}

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

	var locations = []string{"Kitchen", "Bathroom", "Livingroom"}

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

func createRegistryWithService(year any) (ua *UnitAsset, cancel func(), err error) {
	sys := createNewSys()
	temp, cancel := newResource(createConfAssetMultipleTraits(), &sys)
	ua, ok := temp.(*UnitAsset)
	if !ok {
		return nil, nil, fmt.Errorf("Failed while typecasting to local UnitAsset")
	}

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"http": 1234}
	test.IPAddresses = []string{"999.999.999.999"}
	test.EndOfValidity = fmt.Sprintf("%v-01-02T15:04:05Z", year)
	ua.serviceRegistry = map[int]forms.ServiceRecord_v1{0: test}
	return ua, cancel, err
}

type checkExpirationParams struct {
	servicePresent bool
	setup          func() (*UnitAsset, func(), error)
	testCase       string
}

func TestCheckExpiration(t *testing.T) {
	params := []checkExpirationParams{
		{
			true,
			func() (ua *UnitAsset, cancel func(), err error) { return createRegistryWithService(2026) },
			"Best case, service not past expiration",
		},
		{
			false,
			func() (ua *UnitAsset, cancel func(), err error) { return createRegistryWithService(2006) },
			"Bad case, service past expiration",
		},
		{
			true,
			func() (ua *UnitAsset, cancel func(), err error) { return createRegistryWithService("faulty") },
			"Bad case, time parsing problem",
		},
	}
	for _, c := range params {
		ua, cancel, err := c.setup()
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

		cancel()
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
