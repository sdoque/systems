package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

func TestNewRegMsg(t *testing.T) {
	table := []struct {
		scheme    string
		expectErr bool
	}{
		// Missing both ports
		{"", true},
		// Having http port
		{"http", false},
		// Having https port
		{"https", false},
	}

	sys := components.NewSystem("test sys", context.Background())
	sys.Husk = &components.Husk{
		ProtoPort: map[string]int{"https": 0, "http": 0},
	}
	for _, test := range table {
		sys.Husk.ProtoPort[test.scheme] = 8080
		body, err := newRegMsg(&sys)

		if got, want := err != nil, test.expectErr; got != want {
			t.Errorf("expected error %v, got: %v", want, err)
		}

		if got, want := len(body) < 1, test.expectErr; got != want {
			t.Errorf("expected body %v, got: %s", want, string(body))
		}
		if got, want := bytes.Contains(body, []byte(test.scheme)), true; got != want {
			t.Errorf("expected URL scheme %v in body, but it's missing", test.scheme)
		}
	}
}

type transSendRequest struct {
	errResponse error
	status      int
	body        io.Reader
}

func newTransSendRequest() *transSendRequest {
	lt := &transSendRequest{}
	http.DefaultClient.Transport = lt
	return lt
}

// This mock transport also verifies that the system message forms are valid.
func (mock *transSendRequest) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	if mock.errResponse != nil {
		return nil, mock.errResponse
	}
	rec := httptest.NewRecorder()
	rec.WriteHeader(mock.status)
	res := rec.Result()
	res.Body = io.NopCloser(mock.body)
	return res, nil
}

var errMock = fmt.Errorf("mock error")
var bodyMock = "body ok"

func TestSendRequest(t *testing.T) {
	table := []struct {
		method    string
		err       error
		status    int
		body      io.Reader
		expectErr bool
	}{
		// Bad method
		{"no method", nil, 0, nil, true},
		// Error from defaultclient
		{http.MethodGet, errMock, 0, nil, true},
		// Bad status code
		{http.MethodGet, nil, http.StatusInternalServerError, nil, true},
		// Error from body
		{http.MethodGet, nil, http.StatusOK, &errorReader{}, true},
		// All ok
		{http.MethodGet, nil, http.StatusOK, strings.NewReader(bodyMock), false},
	}

	mock := newTransSendRequest()
	for _, test := range table {
		mock.errResponse = test.err
		mock.status = test.status
		mock.body = test.body
		body, err := sendRequest(test.method, "/test/url", nil)

		if got, want := err != nil, test.expectErr; got != want {
			t.Errorf("expected error %v, got: %v", want, err)
		}
		if !test.expectErr && string(body) != bodyMock {
			t.Errorf("expected body '%s', got '%s'", bodyMock, string(body))
		}
	}
}

type transFetchSystems struct {
	t          *testing.T
	coreStatus int
	reqStatus  int
	body       string
}

func newTransFetchSystems(t *testing.T) *transFetchSystems {
	lt := &transFetchSystems{t: t}
	http.DefaultClient.Transport = lt
	return lt
}

// This mock transport also verifies that the system message forms are valid.
func (mock *transFetchSystems) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	rec := httptest.NewRecorder()
	switch req.URL.Path {
	case "/status":
		rec.WriteHeader(mock.coreStatus)
		if mock.coreStatus == http.StatusOK {
			fmt.Fprint(rec.Body, components.ServiceRegistrarLeader)
		}
	case "/syslist":
		rec.WriteHeader(mock.reqStatus)
		fmt.Fprint(rec.Body, mock.body)
	default:
		mock.t.Errorf("unexpected path: %s", req.URL.Path)
		rec.WriteHeader(http.StatusInternalServerError)
	}
	return rec.Result(), nil
}

func TestFetchSystems(t *testing.T) {
	table := []struct {
		coreStatus int
		reqStatus  int
		expectErr  bool
		body       string
	}{
		// Error from GetRunningCoreSystemURL
		{http.StatusInternalServerError, 0, true, ""},
		// Error from sendRequest
		{http.StatusOK, http.StatusInternalServerError, true, ""},
		// Error from Unpack
		{http.StatusOK, http.StatusOK, true, ""},
		// Error from form
		{http.StatusOK, http.StatusOK, true, `{"version":"MessengerRegistration_v1"}`},
		// All ok
		{http.StatusOK, http.StatusOK, false,
			`{"version":"SystemRecordList_v1", "systemurl":["http://test"]}`,
		},
	}

	sys := components.NewSystem("test sys", context.Background())
	sys.CoreS = []*components.CoreSystem{
		{Name: "serviceregistrar", Url: "http://fake"},
	}
	ua := &UnitAsset{
		Owner: &sys,
	}
	mock := newTransFetchSystems(t)

	for _, test := range table {
		mock.coreStatus = test.coreStatus
		mock.reqStatus = test.reqStatus
		mock.body = test.body
		list, err := ua.fetchSystems()

		if got, want := err != nil, test.expectErr; got != want {
			t.Errorf("expected error %v, got: %v", want, err)
		}
		if err != nil {
			continue
		}
		if got, want := len(list), 1; got != want {
			t.Errorf("expected %d urls in list, got %d", want, got)
		}
	}
}

func TestNotifySystems(t *testing.T) {
	name := "test messenger"
	urls := []string{
		"\x00bad",  // Bad URLs
		"/" + name, // Skip itself
		"/good",    // All ok
	}
	sys := components.NewSystem(name, context.Background())
	ua := &UnitAsset{
		Owner: &sys,
	}
	mock := newTransSendRequest()
	mock.status = http.StatusOK
	mock.body = strings.NewReader("ok") // Required for sendRequest()
	for _, test := range urls {
		ua.notifySystems([]string{test})
	}
}

func TestAddMessage(t *testing.T) {
	sys := "test"
	ua := &UnitAsset{
		messages: make(map[string][]message),
	}
	for i := range maxMessages * 2 {
		msg := forms.SystemMessage_v1{
			Level:  forms.LevelDebug,
			System: sys,
			Body:   fmt.Sprintf("%d", i),
		}
		ua.addMessage(msg)
	}

	size := len(ua.messages[sys])
	if got, want := size, maxMessages; got > want {
		t.Errorf("expected max messages %d, got %d", want, got)
	}
	oldest := ua.messages[sys][0]
	if got, want := oldest.body, fmt.Sprintf("%d", maxMessages); got != want {
		t.Errorf("expected oldest msg '%s', got '%s'", want, got)
	}
	newest := ua.messages[sys][size-1]
	if got, want := newest.body, fmt.Sprintf("%d", maxMessages*2-1); got != want {
		t.Errorf("expected newest msg '%s', got '%s'", want, got)
	}
}
