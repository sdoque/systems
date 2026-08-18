package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The page and the model are what an operator actually receives, so both are
// driven here rather than assumed from the fact that they compile.
func TestTheServicesAnswerWithAPageAndAPicture(t *testing.T) {
	traits := &Traits{}
	traits.picture.Store(build("AlphaCloud", map[string]string{
		"beekeeper":   beekeeperGraph,
		"ethermostat": ethermostatGraph,
	}))

	page := httptest.NewRecorder()
	traits.view(page, httptest.NewRequest(http.MethodGet, "/painter/canvas/view", nil))
	if page.Code != 200 {
		t.Fatalf("the page answered %d", page.Code)
	}
	body := page.Body.String()
	// Self-contained: a plant network reaches its own machines and often
	// nothing else, so anything fetched from elsewhere is a blank screen there.
	for _, offsite := range []string{"http://", "https://"} {
		for _, tag := range []string{"<script src=", "<link ", "@import"} {
			if strings.Contains(body, tag) && strings.Contains(body, offsite+"cdn") {
				t.Errorf("the page fetches something from off the machine (%s)", tag)
			}
		}
	}
	if !strings.Contains(body, "wheel") {
		t.Error("the page does not listen for the wheel, which is how it is meant to be used")
	}

	model := httptest.NewRecorder()
	traits.model(model, httptest.NewRequest(http.MethodGet, "/painter/canvas/model", nil))
	if model.Code != 200 {
		t.Fatalf("the model answered %d", model.Code)
	}
	var got Cloud
	if err := json.Unmarshal(model.Body.Bytes(), &got); err != nil {
		t.Fatalf("the model is not readable as JSON: %v", err)
	}
	if len(got.Hosts) != 1 || len(got.Links) != 1 {
		t.Errorf("the model carries %d hosts and %d links; the page draws from this alone",
			len(got.Hosts), len(got.Links))
	}
}

// Before the first walk finishes there is still a page to serve, and it says so
// rather than looking like an empty cloud.
func TestAnUnpaintedCloudSaysSo(t *testing.T) {
	traits := &Traits{}
	cloud := traits.current()
	if len(cloud.Notes) == 0 {
		t.Error("a painter that has not looked yet is indistinguishable from a cloud " +
			"with nothing in it")
	}
}
