package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInitTemplate verifies the template has the expected name, mission, and defaults.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()
	if ua.Name != "BeehiveDashboard" {
		t.Errorf("Name: got %q, want %q", ua.Name, "BeehiveDashboard")
	}
	if ua.Mission != "web_dashboard" {
		t.Errorf("Mission: got %q, want %q", ua.Mission, "web_dashboard")
	}
	tr, ok := ua.Traits.(*Traits)
	if !ok {
		t.Fatal("Traits should be *Traits")
	}
	if tr.Period <= 0 {
		t.Errorf("Period: got %d, want > 0", tr.Period)
	}
	if _, ok := ua.ServicesMap["dashboard"]; !ok {
		t.Error("ServicesMap should contain a 'dashboard' service")
	}
}

// TestNameFromURL verifies that asset names are correctly extracted from service URLs.
func TestNameFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"http://192.168.1.105:20185/beekeeper/Kitchen_Heater/on_off", "Kitchen Heater"},
		{"http://192.168.1.105:20185/beekeeper/Bathroom_Heater/on_off", "Bathroom Heater"},
		{"http://192.168.1.105:20185/beekeeper/Living_Room_Light/on_off", "Living Room Light"},
		{"http://host:port/sys/Single/svc", "Single"},
	}
	for _, tc := range cases {
		got := nameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("nameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestNameFromURL_Fallback verifies that a bare or malformed URL returns the raw URL.
func TestNameFromURL_Fallback(t *testing.T) {
	raw := "not-a-url"
	got := nameFromURL(raw)
	if got != raw {
		t.Errorf("nameFromURL(%q) = %q, want original URL as fallback", raw, got)
	}
}

// TestServing_Dashboard_GET verifies 200 and HTML content-type for a GET on the dashboard service.
func TestServing_Dashboard_GET(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	serving(tr, w, r, "dashboard")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), "Beehive") {
		t.Error("response body should contain 'Beehive'")
	}
}

// TestServing_Dashboard_MethodNotAllowed verifies 405 for non-GET requests to the dashboard.
func TestServing_Dashboard_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/dashboard", nil)
	serving(tr, w, r, "dashboard")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestServing_UnknownService verifies 404 for an unrecognised service subpath.
func TestServing_UnknownService(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestStateHandler_Empty verifies that the state endpoint returns an empty JSON array when
// no switches have been discovered yet.
func TestStateHandler_Empty(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/beehive/api/state", nil)
	stateHandler(tr, w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var got []SwitchInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}
}

// TestStateHandler_WithSwitches verifies that all known switches are returned correctly.
// Sorting happens at discovery time in discoverAndPoll; stateHandler returns the list as stored.
func TestStateHandler_WithSwitches(t *testing.T) {
	// Store already sorted (as discoverAndPoll would leave them).
	tr := &Traits{
		switches: []SwitchInfo{
			{Name: "Bathroom Heater", URL: "http://host/beekeeper/Bathroom_Heater/on_off", State: false, Online: true},
			{Name: "Kitchen Heater", URL: "http://host/beekeeper/Kitchen_Heater/on_off", State: true, Online: true},
		},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/beehive/api/state", nil)
	stateHandler(tr, w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var got []SwitchInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 switches, got %d", len(got))
	}
	if got[0].Name != "Bathroom Heater" || got[1].Name != "Kitchen Heater" {
		t.Errorf("unexpected order: %v", got)
	}
}

// TestStateHandler_MethodNotAllowed verifies 405 for non-GET requests.
func TestStateHandler_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/beehive/api/state", nil)
	stateHandler(tr, w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestToggleHandler_UnknownDevice verifies 404 when the named device is not in the switch list.
func TestToggleHandler_UnknownDevice(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/beehive/api/toggle?name=NoSuchDevice&value=true", nil)
	toggleHandler(tr, w, r, nil)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestToggleHandler_MethodNotAllowed verifies 405 for non-PUT requests.
func TestToggleHandler_MethodNotAllowed(t *testing.T) {
	tr := &Traits{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/beehive/api/toggle?name=X&value=true", nil)
	toggleHandler(tr, w, r, nil)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestToggleHandler_ProxiesToUpstream verifies that the toggle handler sends a PUT to the
// upstream beekeeper URL and then confirms the new state with a GET.
func TestToggleHandler_ProxiesToUpstream(t *testing.T) {
	// Stand up a fake beekeeper that accepts PUT and returns the new state on GET.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"version":   "SignalB_v1.0",
				"value":     true,
				"timestamp": time.Now(),
			})
		default:
			t.Errorf("upstream: unexpected method %s", r.Method)
		}
	}))
	defer upstream.Close()

	tr := &Traits{
		switches: []SwitchInfo{
			{Name: "Kitchen Heater", URL: upstream.URL + "/beekeeper/Kitchen_Heater/on_off", State: false, Online: true},
		},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/beehive/api/toggle?name=Kitchen+Heater&value=true", nil)
	toggleHandler(tr, w, r, nil)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}

	// Local state should reflect what beekeeper confirmed via the GET.
	tr.mu.RLock()
	state := tr.switches[0].State
	tr.mu.RUnlock()
	if !state {
		t.Error("local switch state should be true after toggle confirmed by upstream GET")
	}
}

// TestSwitchSortOrder verifies that stateHandler preserves the stored order (sorting is done at
// discovery time, not at serve time). Switches must be stored sorted by discoverAndPoll.
func TestSwitchSortOrder(t *testing.T) {
	// Simulate what discoverAndPoll produces: alphabetical order.
	tr := &Traits{
		switches: []SwitchInfo{
			{Name: "A Device"},
			{Name: "M Device"},
			{Name: "Z Device"},
		},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/beehive/api/state", nil)
	stateHandler(tr, w, r)

	var got []SwitchInfo
	json.Unmarshal(w.Body.Bytes(), &got)

	want := []string{"A Device", "M Device", "Z Device"}
	for i, sw := range got {
		if sw.Name != want[i] {
			t.Errorf("index %d: got %q, want %q", i, sw.Name, want[i])
		}
	}
}

// TestDashboardHTML verifies the embedded HTML contains expected landmarks.
func TestDashboardHTML(t *testing.T) {
	for _, want := range []string{"Beehive", "/beehive/api/state", "/beehive/api/toggle", "fetchState"} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboardHTML missing %q", want)
		}
	}
}

// TestFetchOnOffState_Offline verifies that an unreachable URL returns (false, false).
func TestFetchOnOffState_Offline(t *testing.T) {
	state, online := fetchOnOffState("http://127.0.0.1:1/nonexistent")
	if online {
		t.Error("expected online=false for unreachable URL")
	}
	if state {
		t.Error("expected state=false for unreachable URL")
	}
}

// TestFetchOnOffState_Online verifies that a well-formed SignalB_v1a response is parsed correctly.
func TestFetchOnOffState_Online(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"version":   "SignalB_v1.0",
			"value":     true,
			"timestamp": time.Now(),
		})
	}))
	defer srv.Close()

	state, online := fetchOnOffState(srv.URL)
	if !online {
		t.Error("expected online=true")
	}
	if !state {
		t.Error("expected state=true")
	}
}
