/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"encoding/json"
	"fmt"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "Kitchen/temperature" {
		t.Errorf("expected Name %q, got %q", "Kitchen/temperature", ua.Name)
	}

	if _, ok := ua.ServicesMap["access"]; !ok {
		t.Error("expected ServicesMap to have an entry for \"access\"")
	}

	if ua.Traits == nil {
		t.Error("expected Traits to be non-nil")
	}
}

// TestPublishInfo_NoPendingDiscovery verifies that publishInfo writes a
// text/plain response with topic, broker, and period info when no sources
// are known yet.
func TestPublishInfo_NoPendingDiscovery(t *testing.T) {
	tr := &Traits{
		Topic:    "Room/temperature",
		Broker:   "tcp://localhost:1883",
		Period:   30,
		cervices: nil,
	}

	w := httptest.NewRecorder()
	tr.publishInfo(w)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"Room/temperature", "tcp://localhost:1883", "30"} {
		if !strings.Contains(body, want) {
			t.Errorf("publishInfo body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "pending discovery") {
		t.Errorf("publishInfo body should say 'pending discovery' when no sources known:\n%s", body)
	}
}

// TestServing_Publish_GET verifies that GET /publish calls publishInfo (200, text/plain).
func TestServing_Publish_GET(t *testing.T) {
	tr := &Traits{
		Topic:  "Room/temperature",
		Broker: "tcp://localhost:1883",
		Period: 30,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/publish", nil)
	serving(tr, w, r, "publish")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestServing_Publish_MethodNotAllowed verifies that DELETE /publish returns 405.
func TestServing_Publish_MethodNotAllowed(t *testing.T) {
	tr := &Traits{Topic: "Room/temperature"}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/telegrapher/Room_temperature/publish", nil)
	serving(tr, w, r, "publish")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestServing_InvalidPath verifies that an unknown service path returns 400.
func TestServing_InvalidPath(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/unknown", nil)
	serving(tr, w, r, "unknown")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestAccess_GET_EmptyMessage verifies that GET /access returns 400 when no
// MQTT message has been received yet.
func TestAccess_GET_EmptyMessage(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty message", w.Code)
	}
}

// TestAccess_GET_WithMessage verifies that GET /access returns 200 and the
// stored message bytes when a message has been received.
func TestAccess_GET_WithMessage(t *testing.T) {
	tr := &Traits{}
	tr.latest.Store(&reading{payload: []byte(`{"temp":21.5}`), received: time.Now()})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "21.5") {
		t.Errorf("body should contain message content, got: %s", w.Body.String())
	}
}

// TestAccess_Default verifies that unsupported methods return a non-2xx status.
func TestAccess_Default(t *testing.T) {
	tr := &Traits{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/telegrapher/Room_temperature/access", nil)
	tr.access(w, r, "access")

	if w.Code == http.StatusOK {
		t.Error("expected non-200 for DELETE /access")
	}
}

// The room is in the topic, and the topic is the asset's name. Filing it under
// the key the authorizer reads is what turns a naming convention into an
// attribute — and an asset with no FunctionalLocation is not merely unpaired,
// it is universally reachable, so the wrong key is the permissive answer.
func TestDetailsFromTopic(t *testing.T) {
	tests := []struct {
		name    string
		pattern []string
		asset   string
		want    map[string][]string
	}{
		{
			name:    "the room becomes the functional location",
			pattern: []string{"FunctionalLocation"},
			asset:   "Bathroom/temperature",
			want:    map[string][]string{"FunctionalLocation": {"Bathroom"}},
		},
		{
			name:    "a deeper topic fills as many keys as the pattern names",
			pattern: []string{"FunctionalLocation", "Quantity"},
			asset:   "Kitchen/temperature",
			want:    map[string][]string{"FunctionalLocation": {"Kitchen"}, "Quantity": {"temperature"}},
		},
		{
			name:    "a pattern longer than the topic takes what there is",
			pattern: []string{"FunctionalLocation", "Quantity", "Extra"},
			asset:   "Kitchen/temperature",
			want:    map[string][]string{"FunctionalLocation": {"Kitchen"}, "Quantity": {"temperature"}},
		},
		{
			name:    "a topic longer than the pattern leaves the rest alone",
			pattern: []string{"FunctionalLocation"},
			asset:   "Kitchen/temperature/outer",
			want:    map[string][]string{"FunctionalLocation": {"Kitchen"}},
		},
		{
			name:    "no pattern, no details",
			pattern: nil,
			asset:   "Kitchen/temperature",
			want:    map[string][]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detailsFromTopic(tc.pattern, tc.asset)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v; want %v", got, tc.want)
			}
			for key, values := range tc.want {
				if len(got[key]) != len(values) || (len(values) > 0 && got[key][0] != values[0]) {
					t.Errorf("%s = %v; want %v", key, got[key], values)
				}
			}
		})
	}
}

// The shipped template must name the key the authorizer reads, since it seeds
// the configuration a fresh deployment starts from.
func TestTemplatePatternNamesTheFunctionalLocation(t *testing.T) {
	ua := initTemplate()
	traits, ok := ua.GetTraits().(*Traits)
	if !ok {
		t.Fatalf("template traits are %T; want *Traits", ua.GetTraits())
	}
	if len(traits.Pattern) == 0 || traits.Pattern[0] != "FunctionalLocation" {
		t.Errorf("pattern = %v; the pairing rule and the knowledge graph both read FunctionalLocation", traits.Pattern)
	}
}

// Firmware authors publish a reading in whichever shape they preferred, and a
// plant contains both. Neither is wrong, so both are read.
func TestAnalogValue(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    float64
		wantErr bool
	}{
		{"a bare number", "21.5", 21.5, false},
		{"a bare number with whitespace", "  21.5\n", 21.5, false},
		{"a negative reading", "-3.25", -3.25, false},
		{"an integer", "22", 22, false},
		{"JSON naming the value", `{"value": 21.5}`, 21.5, false},
		{"JSON with the reading among other fields", `{"unit":"C","value":19.75,"rssi":-58}`, 19.75, false},
		{"JSON naming the service", `{"temperature": 18.5}`, 18.5, false},
		{"a number sent as a string", `{"value": "20.25"}`, 20.25, false},
		{"nothing numeric at all", `{"status":"ok"}`, 0, true},
		{"not a reading", "hello", 0, true},
		{"empty", "", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := analogValue([]byte(tc.payload), "temperature")
			if tc.wantErr != (err != nil) {
				t.Fatalf("analogValue(%q) error = %v; wantErr %v", tc.payload, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("analogValue(%q) = %v; want %v", tc.payload, got, tc.want)
			}
		})
	}
}

// Zero is a plausible temperature, so a payload with no number must fail rather
// than default — otherwise a fabricated reading reaches a control loop.
func TestAnalogValueDoesNotInventAZero(t *testing.T) {
	if v, err := analogValue([]byte(`{"status":"ok"}`), "temperature"); err == nil {
		t.Errorf("a payload with no reading produced %v", v)
	}
}

// The template describes the common case: an analog signal with a unit, in a
// form a consumer can unpack and convert.
func TestTemplateDescribesAnAnalogSignal(t *testing.T) {
	ua := initTemplate()

	var serv *components.Service
	for _, s := range ua.GetServices() {
		serv = s
		break
	}
	if serv == nil {
		t.Fatal("the template provides no service")
	}

	if got := serv.Details["Forms"]; len(got) != 1 || got[0] != "SignalA_v1a" {
		t.Errorf("Forms = %v; a consumer matches on the capitalised key and a registered form", got)
	}
	if got := serv.Details["Unit"]; len(got) != 1 {
		t.Errorf("Unit = %v; without one the topic is served raw", got)
	}
	if got := serv.Details["QuantityKind"]; len(got) != 1 {
		t.Errorf("QuantityKind = %v; without one no consumer asking for a temperature finds it", got)
	}
	if _, ok := serv.Details["forms"]; ok {
		t.Error(`the lowercase "forms" key is still present; nothing matches on it`)
	}
}

// TestAReadingIsServedWhole is the defect this test was written for: Message and
// received were separate fields written by the Paho callback and read separately
// by the HTTP handler, so access could pair the payload of message n with the
// timestamp of message n+1 and serve a signal whose value and timestamp never
// coexisted. Run under -race, the unsynchronized version also reports the race
// itself.
func TestAReadingIsServedWhole(t *testing.T) {
	tr := &Traits{unit: "<http://qudt.org/vocab/unit/DEG_C>", Topic: "room/temperature"}

	// The publisher: every message pairs a value with the instant it arrived.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			tr.latest.Store(&reading{
				payload:  []byte(fmt.Sprintf(`{"value":%d}`, i)),
				received: time.Unix(int64(i), 0),
			})
		}
	}()

	// The consumer: whatever it serves, the value and the timestamp must come
	// from one message.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/telegrapher/Room_temperature/access", nil)
				tr.access(w, r, "access")
				if w.Code != http.StatusOK {
					continue
				}
				var got forms.SignalA_v1a
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Errorf("unpacking the served signal: %v", err)
					return
				}
				// The publisher pairs value i with timestamp i, so any served
				// pair that disagrees was assembled from two messages.
				if int64(got.Value) != got.Timestamp.Unix() {
					t.Errorf("served value %v with timestamp %v — they are from different messages",
						got.Value, got.Timestamp.Unix())
					return
				}
			}
		}()
	}
	wg.Wait()
	<-done
}

// TestAnalogValueServesTheFieldTheTopicNames is follow-up finding N9.
//
// A fixed order of plausible names cannot know what was asked for. The one it
// used — value, temperature, temp, pressure, level — served a BME280's
// temperature as its pressure, and refused any payload keyed by something not on
// the list at all.
func TestAnalogValueServesTheFieldTheTopicNames(t *testing.T) {
	// One sensor, three readings, published to three topics.
	bme280 := []byte(`{"temperature":21.5,"humidity":45,"pressure":1013.2}`)

	for definition, want := range map[string]float64{
		"temperature": 21.5,
		"pressure":    1013.2,
		"humidity":    45,
	} {
		got, err := analogValue(bme280, definition)
		if err != nil {
			t.Errorf("a topic ending in %q could not be served: %v", definition, err)
			continue
		}
		if got != want {
			t.Errorf("a topic ending in %q served %v, want %v", definition, got, want)
		}
	}
}

// The quantities this repository's own meteorologue publishes were refused
// outright by the fixed list, so a working topic began returning 500.
func TestAnalogValueDoesNotRefuseQuantitiesItHasNoOpinionAbout(t *testing.T) {
	for _, quantity := range []string{"humidity", "co2", "noise", "rain", "wind_speed", "power"} {
		payload := []byte(`{"` + quantity + `":42.5}`)
		got, err := analogValue(payload, quantity)
		if err != nil {
			t.Errorf("a topic ending in %q was refused: %v", quantity, err)
			continue
		}
		if got != 42.5 {
			t.Errorf("%q served %v, want 42.5", quantity, got)
		}
	}
}

// "value" still means the reading whatever the reading is, and the same name in
// another casing is the same name rather than a guess.
func TestAnalogValueAcceptsValueAndAnyCasing(t *testing.T) {
	if got, err := analogValue([]byte(`{"value":7.5,"rssi":-58}`), "pressure"); err != nil || got != 7.5 {
		t.Errorf(`"value" was not read for a pressure topic: %v, %v`, got, err)
	}
	if got, err := analogValue([]byte(`{"Temperature":21.5}`), "temperature"); err != nil || got != 21.5 {
		t.Errorf("a capitalized field name was not matched: %v, %v", got, err)
	}
	// And it still refuses to invent one.
	if v, err := analogValue([]byte(`{"humidity":45}`), "temperature"); err == nil {
		t.Errorf("a temperature topic served the humidity %v", v)
	}
}
