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

import (
	"encoding/json"
	"fmt"

	"github.com/sdoque/mbaigo/components"
)

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

// serviceMissions gives each ZigBee service its mission.
//
// The gateway is an interface, not a thing, and a single physical device is
// often both: a smart plug appears in deCONZ as a light with an on_off service
// and as sensors reporting power and energy. Classifying the whole asset as
// actuation would make its metering readable only to whoever may switch it,
// while classifying it as measurement would hide that it can be switched at all.
// So the mission belongs to the service.
//
// A service absent from this map is a programming error rather than a
// configuration one — it means a device type was given a service that was never
// classified — and missionForService reports it so startup fails loudly.
var serviceMissions = map[string]components.Mission{
	// Drivable.
	"on_off":     components.MissionActuation,
	"brightness": components.MissionActuation,

	// Sampled quantities.
	"temperature": components.MissionMeasurement,
	"humidity":    components.MissionMeasurement,
	"pressure":    components.MissionMeasurement,
	"power":       components.MissionMeasurement,
	"energy":      components.MissionMeasurement,
	"light_level": components.MissionMeasurement,

	// Transitions rather than quantities: they report that something happened.
	"button_event": components.MissionEvent,
	"open":         components.MissionEvent,
	"presence":     components.MissionEvent,
	"vibration":    components.MissionEvent,
}

// missionForService returns the mission of a ZigBee service subpath.
func missionForService(subPath string) (components.Mission, error) {
	if mission, ok := serviceMissions[subPath]; ok {
		return mission, nil
	}
	return components.Mission{}, fmt.Errorf("service %q has no mission: add it to serviceMissions", subPath)
}

// methodsFor says which HTTP methods a service subpath answers.
//
// Only on_off can be driven — serving() refuses a PUT to anything else — so
// only on_off says it accepts one. Deriving it here rather than listing it
// beside each spec keeps the two statements in one place: the handler and the
// registration would otherwise be free to disagree about which lights can be
// switched.
func methodsFor(subPath string) []string {
	if subPath == "on_off" {
		return components.HTTPMethods("GET", "PUT")
	}
	return components.HTTPMethods("GET")
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
	// Units are QUDT IRIs, not symbols. A bare "W" is not a unit anyone else
	// can resolve: written into the graph it became a locally minted term that
	// looked like a unit and meant nothing, and under the QUDT alignment AFO
	// 2.1.0 adds it would be asserted to be a qudt:Unit, which is false.
	// An empty unit stays empty and the triple is left out — a boolean has no
	// unit, and saying so with an empty string is not the same as not saying it.
	"on_off":       {"OnOff", "", "device on/off state (SignalB_v1a)"},
	"brightness":   {"Brightness", "<http://qudt.org/vocab/unit/PERCENT>", "brightness level 0–100%"},
	"temperature":  {"Temperature", "<http://qudt.org/vocab/unit/DEG_C>", "temperature"},
	"humidity":     {"Humidity", "<http://qudt.org/vocab/unit/PERCENT>", "relative humidity"},
	"pressure":     {"Pressure", "<http://qudt.org/vocab/unit/HectoPA>", "atmospheric pressure"},
	"power":        {"Power", "<http://qudt.org/vocab/unit/W>", "instantaneous power consumption"},
	"energy":       {"Energy", "<http://qudt.org/vocab/unit/W-HR>", "cumulative energy consumption"},
	"presence":     {"Presence", "", "motion or presence detected (SignalB_v1a)"},
	"open":         {"OpenClose", "", "contact state, true = open (SignalB_v1a)"},
	"button_event": {"ButtonEvent", "", "button event code"},
	"light_level":  {"LightLevel", "<http://qudt.org/vocab/unit/LUX>", "ambient light level"},
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
