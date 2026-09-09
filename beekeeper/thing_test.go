package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// TestInitTemplate verifies the template has the expected name, mission, and defaults.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()
	if ua.Name != "BeekeeperGateway" {
		t.Errorf("Name: got %q, want %q", ua.Name, "BeekeeperGateway")
	}
	// Checked against the taxonomy rather than against a literal. This assertion
	// used to name "expose_zigbee_devices" and so agreed with the template that
	// no mission at all had been declared — it described the system in a field
	// meant to classify it, and the test froze that in place.
	if err := components.ValidateMission(ua.Name, ua.Mission); err != nil {
		t.Errorf("the template seeds a configuration the framework refuses: %v", err)
	}
	cfg, ok := ua.Traits.(*DeconzConfig)
	if !ok {
		t.Fatal("Traits should be *DeconzConfig")
	}
	if cfg.Host == "" {
		t.Error("Host should not be empty")
	}
	if cfg.APIPort == 0 {
		t.Error("APIPort should not be 0")
	}
	if cfg.WSPort == 0 {
		t.Error("WSPort should not be 0")
	}
	if cfg.APIKey == "" {
		t.Error("APIKey should not be empty")
	}
}

// TestNormalizeName verifies safe conversion of device names to asset names.
func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Living Room", "Living_Room"},
		{"Kitchen-Light", "Kitchen_Light"},
		{"Sensor #3", "Sensor__3"},
		{"ValidName123", "ValidName123"},
		{"  spaces  ", "spaces"},
	}
	for _, tc := range cases {
		got := normalizeName(tc.in)
		if got != tc.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExtractLightMeasurements_On verifies on/off and brightness conversion when the light is on.
func TestExtractLightMeasurements_On(t *testing.T) {
	light := DeconzLight{
		State: LightState{On: true, Bri: 127},
	}
	m := extractLightMeasurements(light)
	if m["on_off"] != 1.0 {
		t.Errorf("on_off: got %.1f, want 1.0", m["on_off"])
	}
	// 127/254 × 100 ≈ 50.0
	if m["brightness"] < 49.5 || m["brightness"] > 50.5 {
		t.Errorf("brightness: got %.2f, want ~50.0", m["brightness"])
	}
}

// TestExtractLightMeasurements_Off verifies on_off = 0 when the light is off.
func TestExtractLightMeasurements_Off(t *testing.T) {
	light := DeconzLight{
		State: LightState{On: false, Bri: 0},
	}
	m := extractLightMeasurements(light)
	if m["on_off"] != 0.0 {
		t.Errorf("on_off: got %.1f, want 0.0", m["on_off"])
	}
}

// TestExtractSensorMeasurements_Temperature verifies temperature conversion (× 100 → °C).
func TestExtractSensorMeasurements_Temperature(t *testing.T) {
	raw := 2150 // 21.50 °C
	sensor := DeconzSensor{
		Type:  "ZHATemperature",
		State: SensorState{Temperature: &raw},
	}
	m := extractSensorMeasurements(sensor)
	if m["temperature"] != 21.5 {
		t.Errorf("temperature: got %.2f, want 21.50", m["temperature"])
	}
}

// TestExtractSensorMeasurements_Humidity verifies humidity conversion (× 100 → %).
func TestExtractSensorMeasurements_Humidity(t *testing.T) {
	raw := 6520 // 65.20 %
	sensor := DeconzSensor{
		State: SensorState{Humidity: &raw},
	}
	m := extractSensorMeasurements(sensor)
	if m["humidity"] != 65.2 {
		t.Errorf("humidity: got %.2f, want 65.20", m["humidity"])
	}
}

// TestExtractSensorMeasurements_Power verifies power conversion (deciwatts ÷ 10 → W).
func TestExtractSensorMeasurements_Power(t *testing.T) {
	raw := 235 // 23.5 W
	sensor := DeconzSensor{
		State: SensorState{Power: &raw},
	}
	m := extractSensorMeasurements(sensor)
	if m["power"] != 23.5 {
		t.Errorf("power: got %.1f, want 23.5", m["power"])
	}
}

// TestExtractSensorMeasurements_Presence verifies presence is passed through as float64
// (the cache layer, not the extract layer, is responsible for converting to bool).
func TestExtractSensorMeasurements_Presence(t *testing.T) {
	yes := true
	no := false

	mYes := extractSensorMeasurements(DeconzSensor{State: SensorState{Presence: &yes}})
	if mYes["presence"] != 1.0 {
		t.Errorf("presence (true): got %.1f, want 1.0", mYes["presence"])
	}

	mNo := extractSensorMeasurements(DeconzSensor{State: SensorState{Presence: &no}})
	if mNo["presence"] != 0.0 {
		t.Errorf("presence (false): got %.1f, want 0.0", mNo["presence"])
	}
}

// TestExtractSensorMeasurements_ButtonEvent verifies button event code passthrough.
func TestExtractSensorMeasurements_ButtonEvent(t *testing.T) {
	code := 1002 // Aqara single press
	sensor := DeconzSensor{
		State: SensorState{ButtonEvent: &code},
	}
	m := extractSensorMeasurements(sensor)
	if m["button_event"] != 1002.0 {
		t.Errorf("button_event: got %.0f, want 1002", m["button_event"])
	}
}

// TestExtractSensorMeasurements_NilFields verifies that absent sensor fields produce no map entry.
func TestExtractSensorMeasurements_NilFields(t *testing.T) {
	m := extractSensorMeasurements(DeconzSensor{State: SensorState{}})
	if len(m) != 0 {
		t.Errorf("expected empty map for all-nil state, got %v", m)
	}
}

// TestSensorStateToMap_OpenClose verifies open/close bool → float64 conversion.
func TestSensorStateToMap_OpenClose(t *testing.T) {
	open := true
	m := sensorStateToMap(SensorState{Open: &open})
	if m["open"] != 1.0 {
		t.Errorf("open: got %.1f, want 1.0", m["open"])
	}
}

// TestDeviceCache verifies update and get, including cache misses.
func TestDeviceCache(t *testing.T) {
	c := newDeviceCache()
	ts := time.Now()

	c.update("Living_Room", map[string]float64{"temperature": 21.5, "humidity": 63.0}, ts)

	got := c.get("Living_Room", "temperature")
	if got == nil {
		t.Fatal("expected cached value, got nil")
	}
	if got.IsBool {
		t.Error("temperature should not be a bool service")
	}
	if got.Value != 21.5 {
		t.Errorf("temperature: got %.1f, want 21.5", got.Value)
	}
	if !got.Timestamp.Equal(ts) {
		t.Error("timestamp mismatch")
	}

	if c.get("Living_Room", "nonexistent") != nil {
		t.Error("expected nil for nonexistent service")
	}
	if c.get("Unknown_Asset", "temperature") != nil {
		t.Error("expected nil for unknown asset")
	}
}

// TestDeviceCache_BinaryService verifies that on_off is stored as IsBool=true.
func TestDeviceCache_BinaryService(t *testing.T) {
	c := newDeviceCache()
	ts := time.Now()

	c.update("Hall_Light", map[string]float64{"on_off": 1.0}, ts)
	got := c.get("Hall_Light", "on_off")
	if got == nil {
		t.Fatal("expected cached value, got nil")
	}
	if !got.IsBool {
		t.Error("on_off should be a bool service")
	}
	if !got.BoolValue {
		t.Error("on_off BoolValue: got false, want true")
	}

	c.update("Hall_Light", map[string]float64{"on_off": 0.0}, ts)
	got = c.get("Hall_Light", "on_off")
	if got.BoolValue {
		t.Error("on_off BoolValue: got true, want false")
	}
}

// TestServiceUnit verifies the unit string for all known and unknown subpaths.
func TestServiceUnit(t *testing.T) {
	cases := map[string]string{
		// QUDT IRIs, not symbols. A consumer that reads "W" has to already know
		// what a watt is; one that reads the IRI can look it up, and a reasoner
		// can check it. The booleans keep no unit at all, which is different
		// from an empty one.
		"on_off":       "",
		"brightness":   "<http://qudt.org/vocab/unit/PERCENT>",
		"temperature":  "<http://qudt.org/vocab/unit/DEG_C>",
		"humidity":     "<http://qudt.org/vocab/unit/PERCENT>",
		"pressure":     "<http://qudt.org/vocab/unit/HectoPA>",
		"power":        "<http://qudt.org/vocab/unit/W>",
		"energy":       "<http://qudt.org/vocab/unit/W-HR>",
		"presence":     "",
		"open":         "",
		"button_event": "",
		"light_level":  "<http://qudt.org/vocab/unit/LUX>",
		"vibration":    "",
		"unknown":      "",
	}
	for path, want := range cases {
		got := serviceUnit(path)
		if got != want {
			t.Errorf("serviceUnit(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestServing_GET verifies 200 with body for a cached measurement.
func TestServing_GET(t *testing.T) {
	c := newDeviceCache()
	c.update("Kitchen_Plug", map[string]float64{"power": 42.0}, time.Now())
	tr := &Traits{assetName: "Kitchen_Plug", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/power", nil)
	serving(tr, w, r, "power")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty response body")
	}
}

// TestServing_NotYetAvailable verifies 503 when no data has been cached yet.
func TestServing_NotYetAvailable(t *testing.T) {
	c := newDeviceCache()
	tr := &Traits{assetName: "Bedroom_Sensor", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/temperature", nil)
	serving(tr, w, r, "temperature")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestServing_MethodNotAllowed verifies 405 for non-GET requests.
func TestServing_MethodNotAllowed(t *testing.T) {
	c := newDeviceCache()
	tr := &Traits{assetName: "Hall_Light", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/on_off", nil)
	serving(tr, w, r, "on_off")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// TestLightServices verifies that all expected deCONZ light types are mapped.
func TestLightServices(t *testing.T) {
	mustHave := []string{
		"Extended color light",
		"Color temperature light",
		"On/Off plug-in unit",
		"Dimmable light",
	}
	for _, typ := range mustHave {
		if _, ok := lightServices[typ]; !ok {
			t.Errorf("lightServices missing entry for %q", typ)
		}
	}
}

// TestSensorServices verifies that all expected ZHA sensor types are mapped.
func TestSensorServices(t *testing.T) {
	mustHave := []string{
		"ZHATemperature", "ZHAHumidity", "ZHAPressure",
		"ZHASwitch", "ZHAPower", "ZHAConsumption",
		"ZHAPresence", "ZHAOpenClose", "ZHAVibration",
	}
	for _, typ := range mustHave {
		if _, ok := sensorServices[typ]; !ok {
			t.Errorf("sensorServices missing entry for %q", typ)
		}
	}
}

// Every service any supported device type can expose must have a mission.
// A gap here is a programming error, not a configuration one: the device would
// register a service the authorizer cannot classify.
func TestEveryDeviceServiceHasAMission(t *testing.T) {
	for typ, svcs := range lightServices {
		for _, svc := range svcs {
			if _, err := missionForService(svc); err != nil {
				t.Errorf("light type %q: %v", typ, err)
			}
		}
	}
	for typ, svcs := range sensorServices {
		for _, svc := range svcs {
			if _, err := missionForService(svc); err != nil {
				t.Errorf("sensor type %q: %v", typ, err)
			}
		}
	}
	if _, err := missionForService("no_such_service"); err == nil {
		t.Error("missionForService accepted an unknown service; it must fail loudly")
	}
}

// A smart plug is one physical device that both switches and meters. Its on_off
// service must be actuation while its power and energy services stay
// measurement, or metering becomes readable only by whoever may switch the plug.
func TestSmartPlugSplitsActuationFromMetering(t *testing.T) {
	want := map[string]string{
		"on_off":     "actuation",
		"power":      "measurement",
		"energy":     "measurement",
		"brightness": "actuation",
	}
	for svc, mission := range want {
		got, err := missionForService(svc)
		if err != nil {
			t.Errorf("missionForService(%q): %v", svc, err)
			continue
		}
		if got.String() != mission {
			t.Errorf("missionForService(%q) = %q; want %q", svc, got, mission)
		}
	}
}

// TestFetchFunctionalLocations uses the payload the lab gateway actually returns
// (deCONZ 2.33.2, RaspBee II): two Aqara plugs, one room each, plus Phoscon's own
// empty group. The reserved 0xFFF0 group is added here even though the REST API
// omits it, because the exclusion must be tested rather than trusted.
func TestFetchFunctionalLocations(t *testing.T) {
	const payload = `{
		"1": {"name": "Phoscon_All_Off", "lights": []},
		"2": {"name": "Kitchen", "lights": ["2"]},
		"3": {"name": "Living room", "lights": ["1"]},
		"4": {"name": "Attic", "hidden": true, "lights": ["1"]},
		"65520": {"name": "All", "lights": ["1", "2"]}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/testkey/groups" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		fmt.Fprint(w, payload)
	}))
	defer srv.Close()

	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	apiPort, _ := strconv.Atoi(port)
	cfg := DeconzConfig{Host: host, APIPort: apiPort, APIKey: "testkey"}

	got, err := fetchFunctionalLocations(cfg)
	if err != nil {
		t.Fatalf("fetchFunctionalLocations: %v", err)
	}
	// "Living room" must arrive as LivingRoom: the graph mints alc:LivingRoom
	// from it, and afo:hasFunctionalLocation takes an IRI, not a literal.
	want := map[string][]string{"1": {"LivingRoom"}, "2": {"Kitchen"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A device the gateway places nowhere must carry no FunctionalLocation at all,
// rather than an empty one: an empty detail is a term in the graph that says
// nothing.
func TestNewDeviceAsset_FunctionalLocation(t *testing.T) {
	sys := &components.System{Name: "beekeeper"}
	cache := newDeviceCache()

	placed := newDeviceAsset("bench1", "Bench1", []string{"on_off"}, []string{"LivingRoom"}, "1", DeconzConfig{}, sys, cache)
	if got := placed.Details["FunctionalLocation"]; !reflect.DeepEqual(got, []string{"LivingRoom"}) {
		t.Errorf("placed asset: got %v, want [LivingRoom]", got)
	}

	loose := newDeviceAsset("desk", "Desk", []string{"on_off"}, nil, "2", DeconzConfig{}, sys, cache)
	if _, ok := loose.Details["FunctionalLocation"]; ok {
		t.Errorf("unplaced asset carries FunctionalLocation %v, want it absent", loose.Details["FunctionalLocation"])
	}
}

func TestPlaceName(t *testing.T) {
	cases := map[string]string{
		"Living room":  "LivingRoom",
		"Kitchen":      "Kitchen",
		"LivingRoom":   "LivingRoom", // already one word: left alone, not re-split
		"dining room":  "DiningRoom",
		"Bedroom 2":    "Bedroom2",
		"Jan's office": "JansOffice",
		"  ":           "", // nothing usable: the caller drops it
	}
	for in, want := range cases {
		if got := placeName(in); got != want {
			t.Errorf("placeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The gateway's answer to a bad or missing key, captured from deCONZ 2.33.2:
// HTTP 403 with an array of error objects, where a success is always an object
// keyed by device id.
const deconzUnauthorizedBody = `[{"error":{"address":"/lights","description":"unauthorized user","type":1}}]`

// A freshly generated systemconfig.json has an empty apiKey, so this is the
// state every new deployment starts in. It used to surface as "json: cannot
// unmarshal array into Go value of type map[string]main.DeconzLight", which
// names the shape of the failure and not its cause.
func TestARefusedKeyIsReportedAsARefusedKey(t *testing.T) {
	err := gatewayRefusal(http.StatusForbidden, []byte(deconzUnauthorizedBody))
	if err == nil {
		t.Fatal("a 403 with an unauthorized-user body was treated as success")
	}
	msg := err.Error()
	for _, want := range []string{"refused the API key", "unauthorized user", "apiKey"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "Phoscon") {
		t.Errorf("the message does not say where to get a key: %s", msg)
	}
}

// An error message is the one place a credential must not end up, and the URL
// this system fetches carries the API key in its path.
func TestTheRefusalNeverCarriesTheKey(t *testing.T) {
	err := gatewayRefusal(http.StatusForbidden, []byte(deconzUnauthorizedBody))
	if strings.Contains(err.Error(), "/api/") || strings.Contains(err.Error(), "http://") {
		t.Errorf("the message contains a URL, which carries the key: %s", err.Error())
	}
}

// deCONZ answers some refusals with 200 and an error body, so the status alone
// is not enough to tell.
func TestAnErrorBodyWithA200IsStillARefusal(t *testing.T) {
	if err := gatewayRefusal(http.StatusOK, []byte(deconzUnauthorizedBody)); err == nil {
		t.Error("an error body with a 200 was treated as success")
	}
}

// A refusal that is not about the key keeps the gateway's own words rather than
// being flattened into a guess about credentials.
func TestANonKeyRefusalIsReportedAsItself(t *testing.T) {
	body := `[{"error":{"address":"/lights/9","description":"resource, /lights/9, not available","type":3}}]`
	err := gatewayRefusal(http.StatusNotFound, []byte(body))
	if err == nil {
		t.Fatal("a 404 refusal was treated as success")
	}
	if strings.Contains(err.Error(), "API key") {
		t.Errorf("a missing resource was blamed on the key: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("the gateway's own words were dropped: %s", err.Error())
	}
}

// The normal path must stay quiet.
func TestAGoodAnswerIsNotARefusal(t *testing.T) {
	if err := gatewayRefusal(http.StatusOK, []byte(`{"1":{"name":"BenchHeater"}}`)); err != nil {
		t.Errorf("a normal reply was reported as a refusal: %v", err)
	}
}

// A non-2xx with a body this system cannot parse is still a failure, and must
// not fall through to a type-mismatch error.
func TestAnUnparseableFailureStillFails(t *testing.T) {
	if err := gatewayRefusal(http.StatusBadGateway, []byte("<html>proxy error</html>")); err == nil {
		t.Error("a 502 with an HTML body was treated as success")
	}
}
