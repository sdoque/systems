package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func createTestServiceQuest() forms.ServiceQuest_v1 {
	var ServiceQuest_v1_temperature forms.ServiceQuest_v1
	ServiceQuest_v1_temperature.NewForm()
	ServiceQuest_v1_temperature.ServiceDefinition = "temperature"
	ServiceQuest_v1_temperature.Details = map[string][]string{"Unit": {"Celsius"}}
	return ServiceQuest_v1_temperature
}

func (t *Traits) createDelayedBrokenURL(limit int) func() *http.Response {
	count := 0
	return func() *http.Response {
		resp := &http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       nil,
		}
		count++
		if count == limit {
			f := createTestServiceRecordListForm()
			t.leadingRegistrar = brokenUrl
			resp.Body = io.NopCloser(bytes.NewReader(f))
			return resp
		}
		resp.Body = io.NopCloser(strings.NewReader(string("lead Service Registrar since")))
		return resp
	}
}

var emptyServiceRecordListForm forms.ServiceRecordList_v1

func createEmptyServiceRecordListForm() []byte {
	emptyServiceRecordListForm.NewForm()
	fakebody, err := json.Marshal(emptyServiceRecordListForm)
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

type getServiceURLTestStruct struct {
	inputForm        forms.ServiceQuest_v1
	inputBody        string
	brokenUrl        bool
	writeError       bool
	mockTransportErr int
	errHTTP          error
	expectedOutput   string
	expectedErr      bool
	testName         string
}

var getServiceURLTestParams = []getServiceURLTestStruct{
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), false, false,
		0, nil, string(createTestServicePointForm()), false, "Good case, everything passes"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), false, false,
		2, errHTTP, "", true, "Bad case, DefaultClient.Do fails"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), false, true,
		0, nil, "", true, "Bad case, ReadAll fails"},
	{createTestServiceQuest(), "hej hej", false, false,
		0, nil, "", true, "Bad case, Unpack fails"},
	{createTestServiceQuest(), string(createTestServicePointForm()), false, false,
		0, nil, "", true, "Bad case, type assertion fails"},
	{createTestServiceQuest(), string(createEmptyServiceRecordListForm()), false, false,
		0, nil, "", true, "Bad case, the service record list is empty"},
}

func TestGetServiceURL(t *testing.T) {
	for _, testCase := range getServiceURLTestParams {
		mua := createUnitAsset()
		if mua == nil {
			t.Fatalf("UAssets[\"Orchestration\"] is nil")
		}
		if testCase.brokenUrl == true {
			newMockTransport(mua.createDelayedBrokenURL(2), testCase.mockTransportErr, testCase.errHTTP)
		} else {
			newMockTransport(createMultiHTTPResponse(2, testCase.writeError, testCase.inputBody),
				testCase.mockTransportErr, testCase.errHTTP)
		}
		servLoc, err := mua.getServiceURL(testCase.inputForm, "testconsumer")
		if string(servLoc) != testCase.expectedOutput || (err == nil && testCase.expectedErr == true) ||
			(err != nil && testCase.expectedErr == false) {
			t.Errorf("In test case: %s: Expected %s and error %t, got: %s and %v",
				testCase.testName, testCase.expectedOutput, testCase.expectedErr, string(servLoc), err)
		}
	}
}

func TestSelectService(t *testing.T) {
	serviceListbytes := createTestServiceRecordListForm()
	serviceListf, err := usecases.Unpack(serviceListbytes, "application/json")
	if err != nil {
		t.Fatalf("Error setting up test of SelectService function: %v", err)
	}
	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		t.Fatalf("Error in type assertion when setting up test of SelectService function")
	}

	expectedService := createTestServicePointForm()

	receivedServicef := selectService(*serviceList)

	receivedService, err := usecases.Pack(&receivedServicef, "application/json")
	if err != nil {
		t.Errorf("Expected the received service to be of type forms.ServicePoint_v1, got: %v", receivedService)
	}

	if string(expectedService) != string(receivedService) {
		t.Errorf("Expected: %v, got: %v", expectedService, receivedService)
	}
}

// testRecord builds a registration record for a single provider, as the service
// registrar would return it.
func testRecord(protoPort map[string]int) forms.ServiceRecordList_v1 {
	var rec forms.ServiceRecord_v1
	rec.NewForm()
	rec.SystemName = "ds18b20"
	rec.ServiceDefinition = "temperature"
	rec.SubPath = "temperature"
	rec.ServiceNode = "kitchen-pi"
	rec.IPAddresses = []string{"123.456.789"}
	rec.ProtoPort = protoPort
	rec.Details = map[string][]string{"FunctionalLocation": {"Kitchen"}}

	var list forms.ServiceRecordList_v1
	list.NewForm()
	list.List = []forms.ServiceRecord_v1{rec}
	return list
}

// A provider that has bound HTTPS must be handed out over HTTPS. The URL decides
// whether the consumer's request carries its client certificate, and therefore
// whether the provider can identify the caller at all — an orchestrator that
// answers with a plain-HTTP URL strips the identity of every consumer it serves,
// however well enrolled both ends are.
func TestSelectServiceProtocolPreference(t *testing.T) {
	tests := []struct {
		name      string
		protoPort map[string]int
		wantURL   string
	}{
		{
			name:      "https bound is preferred over http",
			protoPort: map[string]int{"http": 20152, "https": 30150},
			wantURL:   "https://123.456.789:30150/ds18b20/temperature",
		},
		{
			// The common case across the cloud today: https is present in the
			// configuration but set to 0, so no TLS listener is ever bound.
			name:      "https configured but not bound falls back to http",
			protoPort: map[string]int{"http": 20152, "https": 0},
			wantURL:   "http://123.456.789:20152/ds18b20/temperature",
		},
		{
			name:      "http only",
			protoPort: map[string]int{"http": 20152},
			wantURL:   "http://123.456.789:20152/ds18b20/temperature",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectService(testRecord(tc.protoPort)).ServLocation; got != tc.wantURL {
				t.Errorf("ServLocation = %q; want %q", got, tc.wantURL)
			}
		})
	}
}

// The record's identity and metadata must survive the conversion: the authorizer
// keys on these once policy filtering is wired in.
func TestSelectServiceCarriesRecordFields(t *testing.T) {
	sp := selectService(testRecord(map[string]int{"https": 30150}))

	if sp.ProviderName != "ds18b20" {
		t.Errorf("ProviderName = %q; want %q", sp.ProviderName, "ds18b20")
	}
	if sp.ServiceDefinition != "temperature" {
		t.Errorf("ServiceDefinition = %q; want %q", sp.ServiceDefinition, "temperature")
	}
	if sp.ServNode != "kitchen-pi" {
		t.Errorf("ServNode = %q; want %q", sp.ServNode, "kitchen-pi")
	}
	if got := sp.Details["FunctionalLocation"]; len(got) != 1 || got[0] != "Kitchen" {
		t.Errorf("Details[FunctionalLocation] = %v; want [Kitchen]", got)
	}
}

func createTestServiceRecordListFormWithSeveral() []byte {
	var serviceRecordFormTemperature forms.ServiceRecord_v1
	serviceRecordFormTemperature.NewForm()
	serviceRecordFormTemperature.IPAddresses = []string{"123.456.789"}
	serviceRecordFormTemperature.ProtoPort = map[string]int{"http": 123}
	serviceRecordFormTemperature.ServiceDefinition = "temperature"
	var serviceRecordFormRotation forms.ServiceRecord_v1
	serviceRecordFormRotation.NewForm()
	serviceRecordFormRotation.IPAddresses = []string{"123.456.789"}
	serviceRecordFormRotation.ProtoPort = map[string]int{"http": 123}
	serviceRecordFormRotation.ServiceDefinition = "rotation"
	var ServiceRecordListFormWithSeveral forms.ServiceRecordList_v1
	ServiceRecordListFormWithSeveral.NewForm()
	ServiceRecordListFormWithSeveral.List = []forms.ServiceRecord_v1{serviceRecordFormTemperature,
		serviceRecordFormRotation}
	fakebody, err := json.MarshalIndent(ServiceRecordListFormWithSeveral, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

func createTestServiceRecordListFormWithDefinition() []byte {
	var serviceRecordFormWithDefinition forms.ServiceRecord_v1
	serviceRecordFormWithDefinition.NewForm()
	serviceRecordFormWithDefinition.IPAddresses = []string{"123.456.789"}
	serviceRecordFormWithDefinition.ProtoPort = map[string]int{"http": 123}
	serviceRecordFormWithDefinition.ServiceDefinition = "temperature"
	var serviceRecordListFormWithDefinition forms.ServiceRecordList_v1
	serviceRecordListFormWithDefinition.NewForm()
	serviceRecordListFormWithDefinition.List = []forms.ServiceRecord_v1{serviceRecordFormWithDefinition}
	fakebody, err := json.MarshalIndent(serviceRecordListFormWithDefinition, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

func createTestServiceRecordListFormWithDetails() []byte {
	var serviceRecordFormWithDetails forms.ServiceRecord_v1
	serviceRecordFormWithDetails.NewForm()
	serviceRecordFormWithDetails.IPAddresses = []string{"123.456.789"}
	serviceRecordFormWithDetails.ProtoPort = map[string]int{"http": 123}
	serviceRecordFormWithDetails.Details = map[string][]string{"Location": {"Kitchen"}}
	var serviceRecordListFormWithDetails forms.ServiceRecordList_v1
	serviceRecordListFormWithDetails.NewForm()
	serviceRecordListFormWithDetails.List = []forms.ServiceRecord_v1{serviceRecordFormWithDetails}
	fakebody, err := json.MarshalIndent(serviceRecordListFormWithDetails, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

type getServicesURLTestStruct struct {
	inputForm        forms.ServiceQuest_v1
	inputBody        string
	brokenUrl        bool
	writeError       bool
	mockTransportErr int
	errHTTP          error
	expectedOutput   string
	expectedErr      bool
	testName         string
}

var getServicesURLTestParams = []getServicesURLTestStruct{
	{createTestServiceQuest(), string(createTestServiceRecordListFormWithSeveral()), false, false, 0, nil,
		string(createTestServiceRecordListFormWithSeveral()), false,
		"Good case, everything passes with several services"},
	{createTestServiceQuest(), string(createTestServiceRecordListFormWithDefinition()), false, false, 0, nil,
		string(createTestServiceRecordListFormWithDefinition()), false,
		"Good case, everything passes with one service definition"},
	{createTestServiceQuest(), string(createTestServiceRecordListFormWithDetails()), false, false, 0, nil,
		string(createTestServiceRecordListFormWithDetails()), false,
		"Good case, everything passes with one service details"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), false, false, 2, errHTTP,
		"", true,
		"Bad case, DefaultClient.Do fails"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), false, true, 0, nil,
		"", true,
		"Bad case, ReadAll fails"},
	{createTestServiceQuest(), "hej hej", false, false, 0, nil,
		"", true,
		"Bad case, Unpack fails"},
	{createTestServiceQuest(), string(createTestServicePointForm()), false, false, 0, nil,
		"", true,
		"Bad case, type assertion fails"},
	{createTestServiceQuest(), string(createEmptyServiceRecordListForm()), false, false, 0, nil,
		"", true,
		"Bad case, the service record list is empty"},
}

func TestGetServicesURL(t *testing.T) {
	for _, testCase := range getServicesURLTestParams {
		mua := createUnitAsset()
		if mua == nil {
			t.Fatalf("UAssets[\"Orchestration\"] is nil")
		}
		if testCase.brokenUrl == true {
			newMockTransport(mua.createDelayedBrokenURL(2), testCase.mockTransportErr, testCase.errHTTP)
		} else {
			newMockTransport(createMultiHTTPResponse(2, testCase.writeError, testCase.inputBody),
				testCase.mockTransportErr, testCase.errHTTP)
		}
		servLoc, err := mua.getServicesURL(testCase.inputForm)
		if string(servLoc) != testCase.expectedOutput || (err == nil && testCase.expectedErr == true) ||
			(err != nil && testCase.expectedErr == false) {
			t.Errorf("In test case: %s: Expected %s and error %t, got: %s and %v",
				testCase.testName, testCase.expectedOutput, testCase.expectedErr, string(servLoc), err)
		}
	}
}

// candidates builds a two-provider registrar answer.
func candidates() forms.ServiceRecordList_v1 {
	var kitchen, bathroom forms.ServiceRecord_v1
	kitchen.NewForm()
	kitchen.SystemName = "ds18b20"
	kitchen.ServiceNode = "pi_ds18b20_sensor_Id_temperature"
	kitchen.ServiceDefinition = "temperature"
	kitchen.Mission = "measurement"
	bathroom.NewForm()
	bathroom.SystemName = "telegrapher"
	bathroom.ServiceNode = "pi_telegrapher_Bathroom_temperature"
	bathroom.ServiceDefinition = "temperature"
	bathroom.Mission = "measurement"

	var list forms.ServiceRecordList_v1
	list.NewForm()
	list.List = []forms.ServiceRecord_v1{kitchen, bathroom}
	return list
}

func grantListResponse(t *testing.T, permitted forms.ServiceRecord_v1) func() *http.Response {
	t.Helper()
	var answer forms.AuthorizationGrantList_v1
	answer.NewForm()
	answer.Grants = []forms.AuthorizationGrant_v1{{Record: permitted, Token: "claims.signature", TTL: "5m", Reason: "policy 0 permits read"}}
	answer.Refusals = []forms.AuthorizationRefusal_v1{{
		ProviderName: "telegrapher",
		ServiceNode:  "pi_telegrapher_Bathroom_temperature",
		Reason:       "locations do not match",
	}}
	body, err := json.Marshal(&answer)
	if err != nil {
		t.Fatalf("marshalling the grant list: %v", err)
	}
	return func() *http.Response {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
	}
}

// A cloud that has not adopted authorization must keep orchestrating. Making the
// authorizer a hard dependency of every deployment would break clouds that
// predate it.
func TestAuthorizedPassesThroughWithoutAnAuthorizer(t *testing.T) {
	mua := createUnitAsset()
	all := candidates()

	got, _, err := mua.authorized("thermostat", "read", all)
	if err != nil {
		t.Fatalf("authorized: %v", err)
	}
	if len(got.List) != len(all.List) {
		t.Errorf("filtered %d of %d candidates with no authorizer configured", len(all.List)-len(got.List), len(all.List))
	}
}

// With an authorizer, only the granted candidates survive.
func TestAuthorizedKeepsOnlyGrantedCandidates(t *testing.T) {
	mua := createUnitAsset()
	mua.leadingAuthorizer = "http://localhost:20104/authorizer/authorization"
	all := candidates()
	newMockTransport(grantListResponse(t, all.List[0]), 0, nil)

	got, tokens, err := mua.authorized("thermostat", "read", all)
	if err != nil {
		t.Fatalf("authorized: %v", err)
	}
	if len(got.List) != 1 {
		t.Fatalf("kept %d candidates; want 1", len(got.List))
	}
	if got.List[0].SystemName != "ds18b20" {
		t.Errorf("kept %q; want ds18b20", got.List[0].SystemName)
	}
	// The token must come back keyed by service node, so the one attached to the
	// answer belongs to the provider actually chosen.
	if tokens[got.List[0].ServiceNode] == "" {
		t.Errorf("no token for the granted provider: %v", tokens)
	}
}

// Having named the gate, running without it is a fault rather than a fallback:
// an unreachable authorizer must not silently restore unfiltered orchestration.
func TestAuthorizedFailsClosedWhenTheAuthorizerIsUnreachable(t *testing.T) {
	mua := createUnitAsset()
	mua.leadingAuthorizer = "http://localhost:20104/authorizer/authorization"
	newMockTransport(func() *http.Response { return nil }, 1, errHTTP)

	got, _, err := mua.authorized("thermostat", "read", candidates())
	if err == nil {
		t.Fatal("an unreachable authorizer was treated as permission")
	}
	if len(got.List) != 0 {
		t.Errorf("returned %d candidates despite the failure", len(got.List))
	}
	if mua.leadingAuthorizer != "" {
		t.Error("the cached authorizer URL survived a failure; it must be looked up again")
	}
}

// An authorizer that permits nothing is a complete answer, not an error, but the
// consumer must be told rather than handed a provider it may not use.
func TestAuthorizedReturnsNothingWhenAllAreRefused(t *testing.T) {
	mua := createUnitAsset()
	mua.leadingAuthorizer = "http://localhost:20104/authorizer/authorization"

	var empty forms.AuthorizationGrantList_v1
	empty.NewForm()
	body, err := json.Marshal(&empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	newMockTransport(func() *http.Response {
		return &http.Response{
			Status: "200 OK", StatusCode: 200,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(bytes.NewReader(body)),
		}
	}, 0, nil)

	got, _, err := mua.authorized("thermostat", "read", candidates())
	if err != nil {
		t.Fatalf("an empty grant list was treated as an error: %v", err)
	}
	if len(got.List) != 0 {
		t.Errorf("kept %d candidates; want none", len(got.List))
	}
}
