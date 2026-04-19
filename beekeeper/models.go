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
 ***************************************************************************SDG*/

package main

import "encoding/json"

// DeconzLight is the REST API representation of a light or plug device.
type DeconzLight struct {
	Name             string     `json:"name"`
	Type             string     `json:"type"`
	ModelID          string     `json:"modelid"`
	UniqueID         string     `json:"uniqueid"`
	ManufacturerName string     `json:"manufacturername"`
	State            LightState `json:"state"`
}

// LightState is the current state of a light or plug.
type LightState struct {
	On        bool `json:"on"`
	Bri       int  `json:"bri"`
	Reachable bool `json:"reachable"`
}

// wsLightState is used for partial WebSocket state updates where only
// changed fields are present — pointer fields distinguish absent from false/zero.
type wsLightState struct {
	On  *bool `json:"on"`
	Bri *int  `json:"bri"`
}

// DeconzSensor is the REST API representation of a ZHA sensor.
type DeconzSensor struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	ModelID  string      `json:"modelid"`
	UniqueID string      `json:"uniqueid"`
	State    SensorState `json:"state"`
}

// SensorState holds readings from a ZHA sensor. All fields are pointers because
// deCONZ only populates the ones relevant to each sensor type; the same struct is
// reused for partial WebSocket updates.
type SensorState struct {
	Temperature *int     `json:"temperature"` // °C × 100
	Humidity    *int     `json:"humidity"`    // % × 100
	Pressure    *int     `json:"pressure"`    // hPa
	Power       *int     `json:"power"`       // deciwatts (÷10 → W)
	Consumption *float64 `json:"consumption"` // Wh
	On          *bool    `json:"on"`
	Open        *bool    `json:"open"`
	Presence    *bool    `json:"presence"`
	Vibration   *bool    `json:"vibration"`
	ButtonEvent *int     `json:"buttonevent"`
	LightLevel  *int     `json:"lightlevel"`
}

// WSEvent is a deCONZ WebSocket push notification.
type WSEvent struct {
	Event    string          `json:"e"`  // "changed", "added", "deleted"
	Resource string          `json:"r"`  // "lights" or "sensors"
	ID       string          `json:"id"` // deCONZ numeric string ID
	UniqueID string          `json:"uniqueid"`
	State    json.RawMessage `json:"state"`
}

// lightServices maps deCONZ light/plug types to the Arrowhead services they expose.
var lightServices = map[string][]string{
	"Extended color light":    {"on_off", "brightness"},
	"Color temperature light": {"on_off", "brightness"},
	"Color light":             {"on_off", "brightness"},
	"Dimmable light":          {"on_off", "brightness"},
	"Dimmable plug-in unit":   {"on_off", "brightness"},
	"On/Off plug-in unit":     {"on_off"},
	"Smart plug":              {"on_off"},
}

// sensorServices maps deCONZ ZHA sensor types to the Arrowhead services they expose.
var sensorServices = map[string][]string{
	"ZHATemperature": {"temperature"},
	"ZHAHumidity":    {"humidity"},
	"ZHAPressure":    {"pressure"},
	"ZHASwitch":      {"button_event"},
	"ZHAPower":       {"power"},
	"ZHAConsumption": {"energy"},
	"ZHAPresence":    {"presence"},
	"ZHAOpenClose":   {"open"},
	"ZHALightLevel":  {"light_level"},
	"ZHAVibration":   {"vibration"},
}

// serviceSpec defines the Arrowhead service metadata for each service subpath.
type serviceSpec struct {
	definition  string
	unit        string
	description string
}

// binaryService is the set of service subpaths that carry a boolean value
// and should be served as SignalB_v1a rather than SignalA_v1a.
var binaryService = map[string]bool{
	"on_off":    true,
	"presence":  true,
	"open":      true,
	"vibration": true,
}

// serviceSpecs maps service subpath names to their Arrowhead metadata.
var serviceSpecs = map[string]serviceSpec{
	"on_off":       {"OnOff", "", "device on/off state (SignalB_v1a)"},
	"brightness":   {"Brightness", "%", "brightness level 0–100%"},
	"temperature":  {"Temperature", "Celsius", "temperature"},
	"humidity":     {"Humidity", "%", "relative humidity"},
	"pressure":     {"Pressure", "hPa", "atmospheric pressure"},
	"power":        {"Power", "W", "instantaneous power consumption"},
	"energy":       {"Energy", "Wh", "cumulative energy consumption"},
	"presence":     {"Presence", "", "motion or presence detected (SignalB_v1a)"},
	"open":         {"OpenClose", "", "contact state, true = open (SignalB_v1a)"},
	"button_event": {"ButtonEvent", "", "button event code"},
	"light_level":  {"LightLevel", "lux", "ambient light level"},
	"vibration":    {"Vibration", "", "vibration detected (SignalB_v1a)"},
}

// extractLightMeasurements converts a DeconzLight state to a float64 measurement map.
func extractLightMeasurements(light DeconzLight) map[string]float64 {
	m := make(map[string]float64)
	if light.State.On {
		m["on_off"] = 1.0
	} else {
		m["on_off"] = 0.0
	}
	m["brightness"] = float64(light.State.Bri) / 254.0 * 100.0
	return m
}

// extractSensorMeasurements converts a DeconzSensor state to a float64 measurement map.
func extractSensorMeasurements(sensor DeconzSensor) map[string]float64 {
	return sensorStateToMap(sensor.State)
}

// sensorStateToMap converts a SensorState to a float64 measurement map.
// It is called both for full REST responses and partial WebSocket updates.
func sensorStateToMap(s SensorState) map[string]float64 {
	m := make(map[string]float64)
	if s.Temperature != nil {
		m["temperature"] = float64(*s.Temperature) / 100.0
	}
	if s.Humidity != nil {
		m["humidity"] = float64(*s.Humidity) / 100.0
	}
	if s.Pressure != nil {
		m["pressure"] = float64(*s.Pressure)
	}
	if s.Power != nil {
		m["power"] = float64(*s.Power) / 10.0
	}
	if s.Consumption != nil {
		m["energy"] = *s.Consumption
	}
	if s.On != nil {
		v := 0.0
		if *s.On {
			v = 1.0
		}
		m["on_off"] = v
	}
	if s.Open != nil {
		v := 0.0
		if *s.Open {
			v = 1.0
		}
		m["open"] = v
	}
	if s.Presence != nil {
		v := 0.0
		if *s.Presence {
			v = 1.0
		}
		m["presence"] = v
	}
	if s.Vibration != nil {
		v := 0.0
		if *s.Vibration {
			v = 1.0
		}
		m["vibration"] = v
	}
	if s.ButtonEvent != nil {
		m["button_event"] = float64(*s.ButtonEvent)
	}
	if s.LightLevel != nil {
		m["light_level"] = float64(*s.LightLevel)
	}
	return m
}
