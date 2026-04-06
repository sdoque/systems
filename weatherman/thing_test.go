package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// makeTestPacket builds a syntactically correct 99-byte Davis LOOP packet
// with the given field values and a valid CRC so parseLOOP accepts it.
func makeTestPacket(
	barRaw uint16, // 1000 × inHg
	inTempRaw uint16, // 10 × °F
	inHum uint8, // %
	outTempRaw uint16, // 10 × °F
	windMph uint8, // mph
	windDirRaw uint16, // degrees
	outHum uint8, // %
	rainRateRaw uint16, // 100 × in/h
	uvRaw uint8, // 10 × UV index (0xFF = no sensor)
	solarRaw uint16, // W/m² (0x7FFF = no sensor)
	dayRainRaw uint16, // 100 × in
) []byte {
	pkt := make([]byte, 99)
	pkt[0], pkt[1], pkt[2], pkt[3] = 'L', 'O', 'O', 'P'
	binary.LittleEndian.PutUint16(pkt[7:9], barRaw)
	binary.LittleEndian.PutUint16(pkt[9:11], inTempRaw)
	pkt[11] = inHum
	binary.LittleEndian.PutUint16(pkt[12:14], outTempRaw)
	pkt[14] = windMph
	binary.LittleEndian.PutUint16(pkt[16:18], windDirRaw)
	pkt[33] = outHum
	binary.LittleEndian.PutUint16(pkt[41:43], rainRateRaw)
	pkt[43] = uvRaw
	binary.LittleEndian.PutUint16(pkt[44:46], solarRaw)
	binary.LittleEndian.PutUint16(pkt[50:52], dayRainRaw)
	pkt[95] = 0x0A
	pkt[96] = 0x0D
	// CRC over bytes 0-96 stored big-endian at bytes 97-98.
	crcVal := crc16(pkt[:97])
	pkt[97] = byte(crcVal >> 8)
	pkt[98] = byte(crcVal & 0xFF)
	return pkt
}

// TestInitTemplate verifies the template has the expected name, mission, and defaults.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()
	if ua.Name != "VantagePro2" {
		t.Errorf("Name: got %q, want %q", ua.Name, "VantagePro2")
	}
	if ua.Mission != "provide_weather_data" {
		t.Errorf("Mission: got %q, want %q", ua.Mission, "provide_weather_data")
	}
	cfg, ok := ua.Traits.(*StationConfig)
	if !ok {
		t.Fatal("Traits should be *StationConfig")
	}
	if cfg.Port == "" {
		t.Error("Port should not be empty")
	}
	if cfg.BaudRate == 0 {
		t.Error("BaudRate should not be 0")
	}
	if cfg.Period == 0 {
		t.Error("Period should not be 0")
	}
}

// TestCRC16_ValidPacket verifies that crc16 of a packet with appended CRC equals 0.
func TestCRC16_ValidPacket(t *testing.T) {
	pkt := makeTestPacket(29920, 700, 45, 650, 8, 270, 72, 0, 0xFF, 0x7FFF, 0)
	if crc16(pkt) != 0 {
		t.Error("CRC of packet with appended CRC should be 0")
	}
}

// TestParseLOOP_Conversions verifies the unit conversions for a known packet.
func TestParseLOOP_Conversions(t *testing.T) {
	// barRaw=29920 → 29.920 inHg → ~1013.2 mbar
	// inTempRaw=720 → 72.0 °F → ~22.2 °C
	// outTempRaw=600 → 60.0 °F → ~15.6 °C
	// windMph=10 → ~16.1 km/h
	// rainRateRaw=100 → 1.00 in/h → ~25.4 mm/h
	// dayRainRaw=50 → 0.50 in → ~12.7 mm
	pkt := makeTestPacket(29920, 720, 55, 600, 10, 180, 65, 100, 0xFF, 0x7FFF, 50)
	r, err := parseLOOP(pkt)
	if err != nil {
		t.Fatalf("parseLOOP failed: %v", err)
	}

	if r.Barometer < 1010 || r.Barometer > 1016 {
		t.Errorf("Barometer: got %.1f mbar, expected ~1013.2", r.Barometer)
	}
	if r.InsideTemp < 22.0 || r.InsideTemp > 22.5 {
		t.Errorf("InsideTemp: got %.1f °C, expected ~22.2", r.InsideTemp)
	}
	if r.InsideHumidity != 55 {
		t.Errorf("InsideHumidity: got %.0f, want 55", r.InsideHumidity)
	}
	if r.OutsideTemp < 15.5 || r.OutsideTemp > 15.7 {
		t.Errorf("OutsideTemp: got %.1f °C, expected ~15.6", r.OutsideTemp)
	}
	if r.WindSpeed < 16.0 || r.WindSpeed > 16.2 {
		t.Errorf("WindSpeed: got %.1f km/h, expected ~16.1", r.WindSpeed)
	}
	if r.WindAngle != 180 {
		t.Errorf("WindAngle: got %.0f°, want 180", r.WindAngle)
	}
	if r.OutsideHumidity != 65 {
		t.Errorf("OutsideHumidity: got %.0f, want 65", r.OutsideHumidity)
	}
	if r.RainRate < 25.3 || r.RainRate > 25.5 {
		t.Errorf("RainRate: got %.1f mm/h, expected ~25.4", r.RainRate)
	}
	if r.DayRain < 12.6 || r.DayRain > 12.8 {
		t.Errorf("DayRain: got %.1f mm, expected ~12.7", r.DayRain)
	}
}

// TestParseLOOP_NoSensors verifies HasUV and HasSolar flags when sensors are absent.
func TestParseLOOP_NoSensors(t *testing.T) {
	pkt := makeTestPacket(29920, 720, 55, 600, 0, 0, 65, 0, 0xFF, 0x7FFF, 0)
	r, err := parseLOOP(pkt)
	if err != nil {
		t.Fatalf("parseLOOP failed: %v", err)
	}
	if r.HasUV {
		t.Error("HasUV should be false when uvRaw=0xFF")
	}
	if r.HasSolar {
		t.Error("HasSolar should be false when solarRaw=0x7FFF")
	}
}

// TestParseLOOP_WithSensors verifies HasUV and HasSolar flags when sensors are present.
func TestParseLOOP_WithSensors(t *testing.T) {
	pkt := makeTestPacket(29920, 720, 55, 600, 0, 0, 65, 0, 35, 850, 0)
	r, err := parseLOOP(pkt)
	if err != nil {
		t.Fatalf("parseLOOP failed: %v", err)
	}
	if !r.HasUV {
		t.Error("HasUV should be true when uvRaw=35")
	}
	if !r.HasSolar {
		t.Error("HasSolar should be true when solarRaw=850")
	}
	if r.UV != 3.5 {
		t.Errorf("UV: got %.1f, want 3.5", r.UV)
	}
	if r.SolarRadiation != 850 {
		t.Errorf("SolarRadiation: got %.0f, want 850", r.SolarRadiation)
	}
}

// TestParseLOOP_BadHeader verifies that an invalid header returns an error.
func TestParseLOOP_BadHeader(t *testing.T) {
	pkt := makeTestPacket(29920, 720, 55, 600, 0, 0, 65, 0, 0xFF, 0x7FFF, 0)
	pkt[0] = 'X' // corrupt header
	_, err := parseLOOP(pkt)
	if err == nil {
		t.Error("expected error for invalid header")
	}
}

// TestParseLOOP_BadCRC verifies that a CRC mismatch returns an error.
func TestParseLOOP_BadCRC(t *testing.T) {
	pkt := makeTestPacket(29920, 720, 55, 600, 0, 0, 65, 0, 0xFF, 0x7FFF, 0)
	pkt[97] ^= 0xFF // corrupt CRC
	_, err := parseLOOP(pkt)
	if err == nil {
		t.Error("expected CRC error")
	}
}

// TestExtractModuleMeasurements_Console verifies console module extraction.
func TestExtractModuleMeasurements_Console(t *testing.T) {
	r := &LOOPReading{Barometer: 1013.2, InsideTemp: 22.1, InsideHumidity: 48}
	m := extractModuleMeasurements("ConsoleModule", r)
	if m["barometer"] != 1013.2 {
		t.Errorf("barometer: got %.1f, want 1013.2", m["barometer"])
	}
	if m["inside_temperature"] != 22.1 {
		t.Errorf("inside_temperature: got %.1f, want 22.1", m["inside_temperature"])
	}
	if m["inside_humidity"] != 48 {
		t.Errorf("inside_humidity: got %.0f, want 48", m["inside_humidity"])
	}
}

// TestExtractModuleMeasurements_ISS verifies ISS module extraction.
func TestExtractModuleMeasurements_ISS(t *testing.T) {
	r := &LOOPReading{
		OutsideTemp: -3.5, OutsideHumidity: 88,
		WindSpeed: 14.4, WindAngle: 270,
		RainRate: 2.5, DayRain: 12.0,
	}
	m := extractModuleMeasurements("ISSModule", r)
	if m["temperature"] != -3.5 {
		t.Errorf("temperature: got %.1f, want -3.5", m["temperature"])
	}
	if m["wind_speed"] != 14.4 {
		t.Errorf("wind_speed: got %.1f, want 14.4", m["wind_speed"])
	}
	if m["rain_24h"] != 12.0 {
		t.Errorf("rain_24h: got %.1f, want 12.0", m["rain_24h"])
	}
}

// TestExtractModuleMeasurements_Solar verifies solar module extraction.
func TestExtractModuleMeasurements_Solar(t *testing.T) {
	r := &LOOPReading{UV: 4.2, SolarRadiation: 720}
	m := extractModuleMeasurements("SolarModule", r)
	if m["uv"] != 4.2 {
		t.Errorf("uv: got %.1f, want 4.2", m["uv"])
	}
	if m["solar_radiation"] != 720 {
		t.Errorf("solar_radiation: got %.0f, want 720", m["solar_radiation"])
	}
}

// TestExtractModuleMeasurements_Unknown verifies that an unknown asset name
// returns an empty map without panicking.
func TestExtractModuleMeasurements_Unknown(t *testing.T) {
	m := extractModuleMeasurements("UnknownModule", &LOOPReading{})
	if len(m) != 0 {
		t.Errorf("expected empty map for unknown asset, got %v", m)
	}
}

// TestStationCache verifies update and get including misses.
func TestStationCache(t *testing.T) {
	c := newStationCache()
	ts := time.Now()

	c.update("ConsoleModule", map[string]float64{"barometer": 1015.3}, ts)

	got := c.get("ConsoleModule", "barometer")
	if got == nil {
		t.Fatal("expected cached value, got nil")
	}
	if got.Value != 1015.3 {
		t.Errorf("barometer: got %.1f, want 1015.3", got.Value)
	}
	if !got.Timestamp.Equal(ts) {
		t.Error("timestamp mismatch")
	}

	if c.get("ConsoleModule", "nonexistent") != nil {
		t.Error("expected nil for nonexistent measurement")
	}
	if c.get("ISSModule", "barometer") != nil {
		t.Error("expected nil for wrong asset name")
	}
}

// TestServiceUnit verifies all known and unknown service subpaths.
func TestServiceUnit(t *testing.T) {
	cases := map[string]string{
		"barometer":          "mbar",
		"inside_temperature": "Celsius",
		"temperature":        "Celsius",
		"inside_humidity":    "%",
		"humidity":           "%",
		"wind_speed":         "km/h",
		"wind_angle":         "°",
		"rain_rate":          "mm/h",
		"rain_24h":           "mm",
		"uv":                 "UV index",
		"solar_radiation":    "W/m²",
		"unknown":            "",
	}
	for path, want := range cases {
		got := serviceUnit(path)
		if got != want {
			t.Errorf("serviceUnit(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestServing_GET verifies that a GET for a cached measurement returns 200 with body.
func TestServing_GET(t *testing.T) {
	c := newStationCache()
	c.update("ConsoleModule", map[string]float64{"barometer": 1013.0}, time.Now())
	tr := &Traits{assetName: "ConsoleModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/barometer", nil)
	serving(tr, w, r, "barometer")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty response body")
	}
}

// TestServing_NotYetAvailable verifies 503 when the cache has no data yet.
func TestServing_NotYetAvailable(t *testing.T) {
	c := newStationCache()
	tr := &Traits{assetName: "ISSModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/temperature", nil)
	serving(tr, w, r, "temperature")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

// TestServing_MethodNotAllowed verifies 405 for non-GET requests.
func TestServing_MethodNotAllowed(t *testing.T) {
	c := newStationCache()
	tr := &Traits{assetName: "ConsoleModule", cache: c}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/barometer", nil)
	serving(tr, w, r, "barometer")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}
