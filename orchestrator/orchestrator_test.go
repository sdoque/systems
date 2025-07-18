package main

import (
	"encoding/json"
	"io"
	"log"
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
		count++
		if count == limit && writeError == true {
			return &http.Response{
				Status:     "200 OK",
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(errorReader{}),
			}
		}
		if count == limit {
			return &http.Response{
				Status:     "200 OK",
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
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

var serviceQuestForm forms.ServiceQuest_v1

func createTestServiceQuestForm() []byte {
	serviceQuestForm.NewForm()
	fakebody, err := json.Marshal(serviceQuestForm)
	if err != nil {
		log.Fatalf("Fail marshal at start of test: %v", err)
	}
	return fakebody
}

var servicePointForm forms.ServicePoint_v1

func createTestServicePointForm() []byte {
	servicePointForm.NewForm()
	servicePointForm.ServLocation = "http://123.456.789:123//"
	fakebody, err := json.MarshalIndent(servicePointForm, "", "  ")
	if err != nil {
		log.Fatalf("Fail marshal at start of test: %v", err)
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
		log.Fatalf("Fail marshal at start of test: %v", err)
	}
	return fakebody
}

var getServiceURLErrorMessage = "core system 'serviceregistrar' not found: verifying registrar: Get " +
	"\"http://localhost:20102/serviceregistrar/registry/status\": http: RoundTripper implementation " +
	"(*main.mockTransport) returned a nil *Response with a nil error\n"

type orchestrateTestStruct struct {
	inputW           http.ResponseWriter
	inputBody        io.ReadCloser
	httpMethod       string
	contentType      string
	mockTransportErr int
	expectedCode     int
	expectedOutput   string
	testName         string
}

var orchestrateTestParams = []orchestrateTestStruct{
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 200, string(createTestServicePointForm()), "Best case, everything passes"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"", 3, 200, "", "Bad case, header content type is wrong"},
	{httptest.NewRecorder(), io.NopCloser(errorReader{}), "POST",
		"application/json", 3, 200, "", "Bad case, ReadAll on header body fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string("hej hej"))), "POST",
		"text/plain", 3, 200, "", "Bad case, Unpack and type assertion to ServiceQuest_v1 fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 1, 503, getServiceURLErrorMessage, "Bad case, getServiceURL fails"},
	{newMockResponseWriter(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 500, "", "Bad case, write fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(""))), "PUT",
		"", 0, 404, "Method is not supported.\n", "Bad case, wrong http method"},
}

func TestOrchestrate(t *testing.T) {
	for _, testCase := range orchestrateTestParams {
		inputR := httptest.NewRequest(testCase.httpMethod, "/test123", testCase.inputBody)
		inputR.Header.Set("Content-Type", testCase.contentType)
		mua := createUnitAsset()
		newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())),
			testCase.mockTransportErr, nil)
		testCase.inputW.Header()
		mua.orchestrate(testCase.inputW, inputR)

		recorder, ok := testCase.inputW.(*httptest.ResponseRecorder)
		if ok {
			if recorder.Body.String() != testCase.expectedOutput || recorder.Code != testCase.expectedCode {
				t.Errorf("In test case: %s: Expected %s, got: %s",
					testCase.testName, testCase.expectedOutput, recorder.Body.String())
			}
		} else {
			if recorder, ok := testCase.inputW.(*mockResponseWriter); ok {
				if recorder.status != testCase.expectedCode {
					t.Errorf("Expected status %d, got %d", testCase.expectedCode, recorder.status)
				}
			} else {
				t.Errorf("Expected inputW to be of type mockResponseWriter")
			}
		}
	}
}

type orchestrateMultipleTestStruct struct {
	inputW           http.ResponseWriter
	inputBody        io.ReadCloser
	httpMethod       string
	contentType      string
	mockTransportErr int
	expectedCode     int
	expectedOutput   string
	testName         string
}

var orchestrateMultipleTestParams = []orchestrateMultipleTestStruct{
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 200, string(createTestServiceRecordListForm()), "Best case, everything passes"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"", 3, 200, "", "Bad case, header content type is wrong"},
	{httptest.NewRecorder(), io.NopCloser(errorReader{}), "POST",
		"application/json", 3, 200, "", "Bad case, ReadAll on header body fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string("hej hej"))), "POST",
		"text/plain", 3, 200, "", "Bad case, Unpack and type assertion to ServiceQuest_v1 fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 1, 503, getServiceURLErrorMessage, "Bad case, getServiceURL fails"},
	{newMockResponseWriter(), io.NopCloser(strings.NewReader(string(createTestServiceQuestForm()))), "POST",
		"application/json", 3, 0, "", "Bad case, write fails"},
	{httptest.NewRecorder(), io.NopCloser(strings.NewReader(string(""))), "PUT",
		"", 0, 404, "Method is not supported.\n", "Bad case, wrong http method"},
}

func TestOrchestrateMultiple(t *testing.T) {
	for _, testCase := range orchestrateMultipleTestParams {
		inputR := httptest.NewRequest(testCase.httpMethod, "/test123", testCase.inputBody)
		inputR.Header.Set("Content-Type", testCase.contentType)
		mua := createUnitAsset()
		newMockTransport(createMultiHTTPResponse(2, false, string(createTestServiceRecordListForm())),
			testCase.mockTransportErr, nil)
		mua.orchestrateMultiple(testCase.inputW, inputR)

		recorder, ok := testCase.inputW.(*httptest.ResponseRecorder)
		if ok {
			if recorder.Body.String() != testCase.expectedOutput || recorder.Code != testCase.expectedCode {
				t.Errorf("In test case: %s: Expected %s, got: %s",
					testCase.testName, testCase.expectedOutput, recorder.Body.String())
			}
		} else {
			if _, ok := testCase.inputW.(*mockResponseWriter); !ok {
				t.Errorf("Expected inputW to be of type mockResponseWriter")
			}
		}
	}
}
