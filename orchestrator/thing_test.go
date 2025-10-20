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

func (ua *UnitAsset) createDelayedBrokenURL(limit int) func() *http.Response {
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
			ua.leadingRegistrar = brokenUrl
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
		servLoc, err := mua.getServiceURL(testCase.inputForm)
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
