package main

import (
	"encoding/binary"
	"fmt"
	"math"
)

const loopPacketSize = 99

// LOOPReading holds all parsed measurements from a Davis Vantage Pro2 LOOP packet.
// Temperatures are in Celsius, wind speed in km/h, pressure in mbar, rain in mm.
type LOOPReading struct {
	Barometer       float64 // mbar
	InsideTemp      float64 // Celsius
	InsideHumidity  float64 // %
	OutsideTemp     float64 // Celsius
	WindSpeed       float64 // km/h
	WindAngle       float64 // degrees (0 = calm, 1-360)
	OutsideHumidity float64 // %
	RainRate        float64 // mm/h
	DayRain         float64 // mm accumulated today
	UV              float64 // UV index
	SolarRadiation  float64 // W/m²
	HasUV           bool    // false when no UV sensor is installed
	HasSolar        bool    // false when no solar radiation sensor is installed
}

// parseLOOP parses a 99-byte Davis LOOP (type P) packet.
//
// Byte layout (little-endian multibyte values):
//
//	0-3   "LOOP" header
//	4-5   next archive record
//	6     barometer trend (signed byte)
//	7-8   barometer (1000 × inHg)
//	9-10  inside temperature (10 × °F)
//	11    inside humidity (%)
//	12-13 outside temperature (10 × °F)
//	14    wind speed (mph)
//	15    10-min avg wind speed (mph)
//	16-17 wind direction (degrees, 0 = calm)
//	18-32 extra/soil/leaf temperatures (7+4+4 bytes)
//	33    outside humidity (%)
//	34-40 extra humidities (7 bytes)
//	41-42 rain rate (100 × in/h)
//	43    UV index (10 × UV, 0xFF = no sensor)
//	44-45 solar radiation (W/m², 0x7FFF = no sensor)
//	46-49 storm rain / storm start
//	50-51 day rain (100 × in)
//	52-94 remaining fields (month/year rain, ET, alarms, …)
//	95    0x0A '\n'
//	96    0x0D '\r'
//	97-98 CRC-16 (big-endian, CCITT polynomial 0x1021)
func parseLOOP(data []byte) (*LOOPReading, error) {
	if len(data) < loopPacketSize {
		return nil, fmt.Errorf("LOOP packet too short: got %d bytes, want %d", len(data), loopPacketSize)
	}
	if data[0] != 'L' || data[1] != 'O' || data[2] != 'O' || data[3] != 'P' {
		return nil, fmt.Errorf("invalid LOOP header: %q", string(data[0:4]))
	}
	if crc16(data[:loopPacketSize]) != 0 {
		return nil, fmt.Errorf("LOOP packet CRC error")
	}

	round1 := func(v float64) float64 { return math.Round(v*10) / 10 }

	barRaw := binary.LittleEndian.Uint16(data[7:9])
	bar := round1(float64(barRaw) / 1000.0 * 33.8639) // thousandths inHg → mbar

	inTempRaw := binary.LittleEndian.Uint16(data[9:11])
	inTemp := round1((float64(inTempRaw)/10.0 - 32.0) * 5.0 / 9.0)

	inHum := float64(data[11])

	outTempRaw := binary.LittleEndian.Uint16(data[12:14])
	outTemp := round1((float64(outTempRaw)/10.0 - 32.0) * 5.0 / 9.0)

	windKmh := round1(float64(data[14]) * 1.60934) // mph → km/h

	windDir := float64(binary.LittleEndian.Uint16(data[16:18]))

	outHum := float64(data[33])

	rainRateRaw := binary.LittleEndian.Uint16(data[41:43])
	rainRate := round1(float64(rainRateRaw) / 100.0 * 25.4) // hundredths in/h → mm/h

	uvRaw := data[43]
	uv := round1(float64(uvRaw) / 10.0)
	hasUV := uvRaw != 0xFF

	solarRaw := binary.LittleEndian.Uint16(data[44:46])
	hasSolar := solarRaw != 0x7FFF

	dayRainRaw := binary.LittleEndian.Uint16(data[50:52])
	dayRain := round1(float64(dayRainRaw) / 100.0 * 25.4) // hundredths in → mm

	return &LOOPReading{
		Barometer:       bar,
		InsideTemp:      inTemp,
		InsideHumidity:  inHum,
		OutsideTemp:     outTemp,
		WindSpeed:       windKmh,
		WindAngle:       windDir,
		OutsideHumidity: outHum,
		RainRate:        rainRate,
		DayRain:         dayRain,
		UV:              uv,
		SolarRadiation:  float64(solarRaw),
		HasUV:           hasUV,
		HasSolar:        hasSolar,
	}, nil
}

// crc16 computes the Davis CRC-16 (CCITT polynomial 0x1021).
// Running this over the full 99-byte packet (data + CRC bytes) returns 0 when valid.
func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// ---------------------------------------------------------------------------
// Asset definitions — fixed for the Davis Vantage Pro2 hardware.

// serviceSpec describes one service offered by a unit asset.
type serviceSpec struct {
	definition  string
	subPath     string
	unit        string
	description string
}

// moduleInfo defines a unit asset name and its set of services.
type moduleInfo struct {
	assetName string
	services  []serviceSpec
}

var consoleModuleInfo = moduleInfo{
	assetName: "ConsoleModule",
	services: []serviceSpec{
		{"barometer", "barometer", "mbar", "atmospheric pressure from the indoor console (GET)"},
		{"inside_temperature", "inside_temperature", "Celsius", "temperature inside at the console (GET)"},
		{"inside_humidity", "inside_humidity", "%", "relative humidity inside at the console (GET)"},
	},
}

var issModuleInfo = moduleInfo{
	assetName: "ISSModule",
	services: []serviceSpec{
		{"temperature", "temperature", "Celsius", "outdoor temperature from the ISS (GET)"},
		{"humidity", "humidity", "%", "outdoor relative humidity from the ISS (GET)"},
		{"wind_speed", "wind_speed", "km/h", "wind speed (GET)"},
		{"wind_angle", "wind_angle", "°", "wind direction in degrees (GET)"},
		{"rain_rate", "rain_rate", "mm/h", "current rain rate (GET)"},
		{"rain_24h", "rain_24h", "mm", "day rain accumulation (GET)"},
	},
}

var solarModuleInfo = moduleInfo{
	assetName: "SolarModule",
	services: []serviceSpec{
		{"uv", "uv", "UV index", "ultraviolet index (GET)"},
		{"solar_radiation", "solar_radiation", "W/m²", "solar radiation (GET)"},
	},
}

// extractModuleMeasurements pulls the measurements for a given asset from a LOOPReading.
func extractModuleMeasurements(assetName string, r *LOOPReading) map[string]float64 {
	m := make(map[string]float64)
	switch assetName {
	case "ConsoleModule":
		m["barometer"] = r.Barometer
		m["inside_temperature"] = r.InsideTemp
		m["inside_humidity"] = r.InsideHumidity
	case "ISSModule":
		m["temperature"] = r.OutsideTemp
		m["humidity"] = r.OutsideHumidity
		m["wind_speed"] = r.WindSpeed
		m["wind_angle"] = r.WindAngle
		m["rain_rate"] = r.RainRate
		m["rain_24h"] = r.DayRain
	case "SolarModule":
		m["uv"] = r.UV
		m["solar_radiation"] = r.SolarRadiation
	}
	return m
}
