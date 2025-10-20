package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

func TestServing(t *testing.T) {
	inputW := httptest.NewRecorder()
	inputR := httptest.NewRequest(http.MethodPost, "/test123",
		io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))))
	inputR.Header.Set("Content-Type", "application/json")
	newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())), 0, nil)
	mua := createUnitAsset()
	mua.Serving(inputW, inputR, "squest")

	var expectedOutput = string(createTestServicePointForm())

	if inputW.Body.String() != expectedOutput || inputW.Code != 200 {
		t.Errorf("Expected %s and code %d, got: %s and code %d",
			expectedOutput, 200, inputW.Body.String(), inputW.Code)
	}

	inputW = httptest.NewRecorder()
	inputR = httptest.NewRequest(http.MethodPost, "/test123",
		io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))))
	inputR.Header.Set("Content-Type", "application/json")
	newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())), 0, nil)
	mua = createUnitAsset()
	mua.Serving(inputW, inputR, "squests")

	expectedOutput = string(createTestServiceRecordListForm())

	if inputW.Body.String() != expectedOutput || inputW.Code != 200 {
		t.Errorf("Expected %s and code %d, got: %s and code %d",
			expectedOutput, 200, inputW.Body.String(), inputW.Code)
	}

	inputW = httptest.NewRecorder()
	inputR = httptest.NewRequest(http.MethodPost, "/test123", io.NopCloser(strings.NewReader("")))
	inputR.Header.Set("Content-Type", "application/json")
	newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())), 0, nil)
	mua = createUnitAsset()
	mua.Serving(inputW, inputR, "wrong")

	if inputW.Code == 200 {
		t.Errorf("Expected the error code to not be 200 when having servicePath not be squest")
	}
}

func createMultiHTTPResponse(limit int, writeError bool, body string) func() *http.Response {
	count := 0
	return func() *http.Response {
		resp := &http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       nil,
		}
		count++
		if count == limit && writeError == true {
			resp.Body = io.NopCloser(errorReader{})
			return resp
		}
		if count == limit {
			resp.Body = io.NopCloser(strings.NewReader(body))
			return resp
		}
		resp.Body = io.NopCloser(strings.NewReader(string("lead Service Registrar since")))
		return resp
	}
}

var serviceQuestForm forms.ServiceQuest_v1

func createTestServiceQuestForm() []byte {
	serviceQuestForm.NewForm()
	fakebody, err := json.Marshal(serviceQuestForm)
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

var servicePointForm forms.ServicePoint_v1

func createTestServicePointForm() []byte {
	servicePointForm.NewForm()
	servicePointForm.ServLocation = "http://123.456.789:123//"
	fakebody, err := json.MarshalIndent(servicePointForm, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

var serviceRecordForm forms.ServiceRecord_v1

var serviceRecord2Form forms.ServiceRecord_v1

var serviceRecordListForm forms.ServiceRecordList_v1

func createTestServiceRecordListForm() []byte {
	serviceRecordForm.NewForm()
	serviceRecordForm.IPAddresses = []string{"123.456.789"}
	serviceRecordForm.ProtoPort = map[string]int{"http": 123}
	serviceRecord2Form.NewForm()
	serviceRecord2Form.IPAddresses = []string{"123.456.789"}
	serviceRecord2Form.ProtoPort = map[string]int{"http": 123}
	serviceRecordListForm.NewForm()
	serviceRecordListForm.List = []forms.ServiceRecord_v1{serviceRecordForm, serviceRecord2Form}
	fakebody, err := json.MarshalIndent(serviceRecordListForm, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Fail marshal at start of test: %v", err))
	}
	return fakebody
}

var getServiceURLErrorMessage = "core system 'serviceregistrar' not found: verifying registrar: Get " +
	"\"http://localhost:20102/serviceregistrar/registry/status\": http: RoundTripper implementation " +
	"(*main.mockTransport) returned a nil *Response with a nil error\n"

type orchestrateTestStruct struct {
	inputBody        io.ReadCloser
	httpMethod       string
	contentType      string
	mockTransportErr int
	expectedCode     int
	expectedOutput   string
	testName         string
}

var orchestrateTestParams = []orchestrateTestStruct{
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 200, string(createTestServicePointForm()), "Best case, everything passes"},
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"", 3, 200, "", "Bad case, header content type is wrong"},
	{io.NopCloser(errorReader{}), "POST",
		"application/json", 3, 200, "", "Bad case, ReadAll on header body fails"},
	{io.NopCloser(strings.NewReader(string("hej hej"))), "POST",
		"text/plain", 3, 200, "", "Bad case, Unpack and type assertion to ServiceQuest_v1 fails"},
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 1, 503, getServiceURLErrorMessage, "Bad case, getServiceURL fails"},
	{io.NopCloser(strings.NewReader(string(""))), "PUT",
		"", 0, 404, "Method is not supported.\n", "Bad case, wrong http method"},
}

func TestOrchestrate(t *testing.T) {
	for _, testCase := range orchestrateTestParams {
		inputR := httptest.NewRequest(testCase.httpMethod, "/test123", testCase.inputBody)
		inputR.Header.Set("Content-Type", testCase.contentType)
		mua := createUnitAsset()
		newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())),
			testCase.mockTransportErr, nil)

		inputW := httptest.NewRecorder()
		inputW.Header()
		mua.orchestrate(inputW, inputR)

		if inputW.Body.String() != testCase.expectedOutput || inputW.Result().StatusCode != testCase.expectedCode {
			t.Errorf("In test case: %s: Expected %s, got: %s",
				testCase.testName, testCase.expectedOutput, inputW.Body.String())
		}
	}

	// Special case
	inputR := httptest.NewRequest(http.MethodPost, "/test123",
		io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))))
	inputR.Header.Set("content-Type", "application/json")
	mua := createUnitAsset()
	newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())), 3, nil)
	inputW := newMockResponseWriter()
	mua.orchestrate(inputW, inputR)

	if inputW.ResponseRecorder.Body.String() != "" || inputW.ResponseRecorder.Code != 500 {
		t.Errorf("In test case: Bad case, write fails: Expected: , and: 500, got: %s, and: %d",
			inputW.ResponseRecorder.Body.String(), inputW.ResponseRecorder.Code)
	}
}

type orchestrateMultipleTestStruct struct {
	inputBody        io.ReadCloser
	httpMethod       string
	contentType      string
	mockTransportErr int
	expectedCode     int
	expectedOutput   string
	testName         string
}

var orchestrateMultipleTestParams = []orchestrateMultipleTestStruct{
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 200, string(createTestServiceRecordListForm()), "Best case, everything passes"},
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"", 3, 200, "", "Bad case, header content type is wrong"},
	{io.NopCloser(errorReader{}), "POST",
		"application/json", 3, 200, "", "Bad case, ReadAll on header body fails"},
	{io.NopCloser(strings.NewReader(string("hej hej"))), "POST",
		"text/plain", 3, 200, "", "Bad case, Unpack and type assertion to ServiceQuest_v1 fails"},
	{io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 1, 503, getServiceURLErrorMessage, "Bad case, getServiceURL fails"},
	{io.NopCloser(strings.NewReader(string(""))), "PUT",
		"", 0, 404, "Method is not supported.\n", "Bad case, wrong http method"},
}

func TestOrchestrateMultiple(t *testing.T) {
	for _, testCase := range orchestrateMultipleTestParams {
		inputR := httptest.NewRequest(testCase.httpMethod, "/test123", testCase.inputBody)
		inputR.Header.Set("Content-Type", testCase.contentType)
		mua := createUnitAsset()
		newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())),
			testCase.mockTransportErr, nil)
		inputW := httptest.NewRecorder()
		mua.orchestrateMultiple(inputW, inputR)

		if inputW.Body.String() != testCase.expectedOutput || inputW.Code != testCase.expectedCode {
			t.Errorf("In test case: %s: Expected %s, got: %s",
				testCase.testName, testCase.expectedOutput, inputW.Body.String())
		}
	}

	// Special case, write fails

	inputR := httptest.NewRequest(http.MethodPost, "/test123",
		io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))))
	inputR.Header.Set("content-Type", "application/json")
	mua := createUnitAsset()
	newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())), 3, nil)
	inputW := newMockResponseWriter()
	mua.orchestrateMultiple(inputW, inputR)

	if inputW.ResponseRecorder.Body.String() != "" || inputW.ResponseRecorder.Code != 500 {
		t.Errorf("In test case: Bad case, write fails: Expected: , and: 500, got: %s, and: %d",
			inputW.ResponseRecorder.Body.String(), inputW.ResponseRecorder.Code)
	}
}
