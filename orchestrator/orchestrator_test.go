package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

/*
type servingTestStruct struct {
	servicePath string
	testName    string
}

var servingTestParams = []servingTestStruct{
	{"squest", "Good case, the service path is squest"},
	{"", "Bad case, the service path is not squest"},
}

func TestServing(t *testing.T) {

}
*/

func createUnitAsset() *UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	squest := components.Service{
		Definition:  "squest",
		SubPath:     "squest",
		Details:     map[string][]string{"DefaultForm": {"ServiceRecord_v1"}, "Location": {"LocalCloud"}},
		Description: "looks for the desired service described in a quest form (POST)",
	}

	assetTraits := Traits{
		leadingRegistrar: &components.CoreSystem{
			Name: components.ServiceRegistrarName,
			Url:  "https://leadingregistrar",
		},
	}

	// create the unit asset template
	uat := &UnitAsset{
		Name:    "orchestration",
		Details: map[string][]string{"Platform": {"Independent"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			squest.SubPath: &squest, // Inline assignment of the temperature service
		},
	}
	return uat
}

func createMultiHTTPResponse() func() *http.Response {
	count := 0
	return func() *http.Response {
		count++
		if count == 2 {
			f := createTestServiceRecordListForm()
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

var serviceRecordListForm forms.ServiceRecordList_v1

func createTestServiceRecordListForm() []byte {
	serviceRecordForm.NewForm()
	serviceRecordForm.IPAddresses = []string{"123.456.789"}
	serviceRecordForm.ProtoPort = map[string]int{"http": 123}
	serviceRecordListForm.NewForm()
	serviceRecordListForm.List = []forms.ServiceRecord_v1{serviceRecordForm}
	fakebody, err := json.Marshal(serviceRecordListForm)
	if err != nil {
		log.Fatalf("Fail marshal at start of test: %v", err)
	}
	return fakebody
}

type orchestrateTestStruct struct {
	inputW         http.ResponseWriter
	inputBody      string
	expectedErr    bool
	expectedOutput string
	testName       string
}

var orchestrateTestParams = []orchestrateTestStruct{
	{httptest.NewRecorder(), string(createTestServiceQuestForm()),
		false, string(createTestServicePointForm()), "Best case, everything passes"},
}

func TestOrchestrate(t *testing.T) {
	for _, testCase := range orchestrateTestParams {
		inputR := httptest.NewRequest(http.MethodPost, "/test123", io.NopCloser(strings.NewReader(testCase.inputBody)))
		inputR.Header.Set("Content-Type", "application/json")
		mua := createUnitAsset()
		newMockTransport(createMultiHTTPResponse(), 3, nil)
		mua.orchestrate(testCase.inputW, inputR)

		recorder, ok := testCase.inputW.(*httptest.ResponseRecorder)
		if ok {
			if recorder.Body.String() != testCase.expectedOutput {
				t.Errorf("In test case: %s: Expected %s, got: %s",
					testCase.testName, testCase.expectedOutput, recorder.Body.String())
			}
		}
	}
}
