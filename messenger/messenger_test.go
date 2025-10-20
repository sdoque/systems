package main

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdoque/mbaigo/components"
)

type errorReader struct{}

func (er *errorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read error")
}

func (er *errorReader) Close() error {
	return fmt.Errorf("close error")
}

func TestHandleNewMessage(t *testing.T) {
	table := []struct {
		expectedStatus int
		method         string
		content        string
		body           io.ReadCloser
	}{
		// Method not post
		{http.StatusMethodNotAllowed, http.MethodGet, "", nil},
		// Read body error
		{http.StatusInternalServerError, http.MethodPost, "", &errorReader{}},
		// Unpack error
		{http.StatusBadRequest, http.MethodPost, "bad type", nil},
		// Wrong form
		{http.StatusBadRequest, http.MethodPost, "application/json",
			io.NopCloser(strings.NewReader(`{"version":"MessengerRegistration_v1"}`)),
		},
		// All ok
		{http.StatusOK, http.MethodPost, "application/json",
			io.NopCloser(strings.NewReader(`{"version":"SystemMessage_v1","system":"test"}`)),
		},
	}

	ua := &UnitAsset{
		messages: make(map[string][]message),
	}
	for _, test := range table {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(test.method, "/message", test.body)
		req.Header.Set("Content-Type", test.content)
		ua.handleNewMessage(rec, req)

		res := rec.Result()
		if got, want := res.StatusCode, test.expectedStatus; got != want {
			t.Errorf("expected status %d, got %d", want, got)
		}
	}
}

func TestHandleDashboard(t *testing.T) {
	table := []struct {
		expectedStatus int
		method         string
		badMockBuff    bool
	}{
		// Method not GET
		{http.StatusMethodNotAllowed, http.MethodPost, false},
		// Template fails executing
		{http.StatusInternalServerError, http.MethodGet, true},
		// All ok
		{http.StatusOK, http.MethodGet, false},
	}

	tmpl, err := template.New("dashboard").Parse(tmplDashboard)
	if err != nil {
		t.Fatalf("expected no error from template.Parse, got %v", err)
	}
	sys := components.NewSystem("test sys", context.Background())
	ua := &UnitAsset{
		Owner:         &sys,
		messages:      make(map[string][]message),
		tmplDashboard: tmpl,
	}
	for _, test := range table {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(test.method, "/message", nil)
		if test.badMockBuff {
			// Triggers the mock buffer error
			req.Header.Set(testBufferHeader, "true")
		}
		ua.handleDashboard(rec, req)

		res := rec.Result()
		if got, want := res.StatusCode, test.expectedStatus; got != want {
			t.Errorf("expected status %d, got %d", want, got)
		}
	}
}
