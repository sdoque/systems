package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func TestInitTemplate(t *testing.T) {
	expectedServices := []string{"squest"}

	ua := initTemplate()
	services := ua.GetServices()

	// Check if expected name and services are present
	if ua.GetName() != "orchestration" {
		t.Errorf("Name mismatch expected 'registry', got: %s", ua.GetName())
	}

	for _, s := range expectedServices {
		if _, ok := services[s]; !ok {
			t.Errorf("Expected service '%s' to be present", s)
		}
	}
}

func createTestServiceQuest() forms.ServiceQuest_v1 {
	var ServiceQuest_v1 forms.ServiceQuest_v1
	ServiceQuest_v1.NewForm()
	return ServiceQuest_v1
}

func (ua *UnitAsset) createDelayedBrokenURL(limit int) func() *http.Response {
	count := 0
	return func() *http.Response {
		count++
		if count == limit {
			f := createTestServiceRecordListForm()
			ua.leadingRegistrar.Url = brokenUrl
			return &http.Response{
				Status:     "200 OK",
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(f)),
			}
		}
		return &http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string("lead Service Registrar since"))),
		}
	}
}

var emptyServiceRecordListForm forms.ServiceRecordList_v1

func createEmptyServiceRecordListForm() []byte {
	emptyServiceRecordListForm.NewForm()
	fakebody, err := json.Marshal(emptyServiceRecordListForm)
	if err != nil {
		log.Fatalf("Fail marshal at start of test: %v", err)
	}
	return fakebody
}

type getServiceURLTestStruct struct {
	inputForm           forms.ServiceQuest_v1
	inputBody           string
	leadingRegistrarUrl string
	brokenUrl           bool
	writeError          bool
	mockTransportErr    int
	errHTTP             error
	expectedOutput      string
	expectedErr         bool
	testName            string
}

var getServiceURLTestParams = []getServiceURLTestStruct{
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), "https://leadingregistrar", false, false,
		0, nil, string(createTestServicePointForm()), false, "Good case, everything passes"},
	// {createTestServiceQuest(), string(createTestServiceRecordListForm()), "https://leadingregistrar", true, false,
	//	0, nil, "", true, "Bad case, creating a new http request fails"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), "https://leadingregistrar", false, false,
		2, errHTTP, "", true, "Bad case, DefaultClient.Do fails"},
	{createTestServiceQuest(), string(createTestServiceRecordListForm()), "https://leadingregistrar", false, true,
		0, nil, "", true, "Bad case, ReadAll fails"},
	{createTestServiceQuest(), "hej hej", "https://leadingregistrar", false, false,
		0, nil, "", true, "Bad case, Unpack fails"},
	{createTestServiceQuest(), string(createTestServicePointForm()), "https://leadingregistrar", false, false,
		0, nil, "", false, "Bad case, type assertion fails"},
	{createTestServiceQuest(), string(createEmptyServiceRecordListForm()), "https://leadingregistrar", false, false,
		0, nil, "", true, "Bad case, the service record list is empty"},
}

func TestGetServiceURL(t *testing.T) {
	for _, testCase := range getServiceURLTestParams {
		mua := createUnitAsset(testCase.leadingRegistrarUrl)
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
		log.Fatalf("Error setting up test of SelectService function: %v", err)
	}
	serviceList, ok := serviceListf.(*forms.ServiceRecordList_v1)
	if !ok {
		log.Fatalf("Error in type assertion when setting up test of SelectService function")
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
