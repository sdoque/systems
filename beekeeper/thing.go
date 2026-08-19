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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// DeconzConfig holds the connection parameters for the deCONZ gateway.
type DeconzConfig struct {
	Host    string        `json:"host"`
	APIPort int           `json:"apiPort"`
	WSPort  int           `json:"wsPort"`
	APIKey  string        `json:"apiKey"`
	Period  time.Duration `json:"period"` // REST poll interval in seconds
}

func (c DeconzConfig) apiBase() string {
	return fmt.Sprintf("http://%s:%d/api/%s", c.Host, c.APIPort, c.APIKey)
}

func (c DeconzConfig) wsURL() string {
	return fmt.Sprintf("ws://%s:%d", c.Host, c.WSPort)
}

// DeviceCache is a thread-safe measurement store keyed by asset name and service subpath.
type DeviceCache struct {
	mu   sync.RWMutex
	data map[string]map[string]CachedMeasurement // assetName → subPath → value
}

// CachedMeasurement holds one measurement value and its timestamp.
// Binary services (on_off, presence, open, vibration) use BoolValue; all others use Value.
type CachedMeasurement struct {
	Value     float64
	BoolValue bool
	IsBool    bool
	Timestamp time.Time
}

func newDeviceCache() *DeviceCache {
	return &DeviceCache{data: make(map[string]map[string]CachedMeasurement)}
}

func (c *DeviceCache) update(assetName string, measurements map[string]float64, ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data[assetName] == nil {
		c.data[assetName] = make(map[string]CachedMeasurement)
	}
	for k, v := range measurements {
		if binaryService[k] {
			c.data[assetName][k] = CachedMeasurement{IsBool: true, BoolValue: v != 0, Timestamp: ts}
		} else {
			c.data[assetName][k] = CachedMeasurement{Value: v, Timestamp: ts}
		}
	}
}

func (c *DeviceCache) get(assetName, service string) *CachedMeasurement {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.data[assetName]; ok {
		if v, ok := m[service]; ok {
			return &v
		}
	}
	return nil
}

// Traits is the runtime state for one ZigBee device unit asset.
type Traits struct {
	assetName string
	lightID   string       // deCONZ light ID, non-empty only for devices with an on_off service
	cfg       DeconzConfig // gateway connection parameters, used to forward PUT commands
	cache     *DeviceCache
}

// assetEntry maps a deCONZ resource+id pair back to an asset name for WebSocket routing.
type assetEntry struct {
	resource string // "lights" or "sensors"
	id       string // deCONZ numeric string ID
}

// initTemplate returns a template UnitAsset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	return &components.UnitAsset{
		Name: "BeekeeperGateway",
		// A mission from the taxonomy rather than a description of the system.
		// "expose_zigbee_devices" is neither, and this template is what seeds a
		// systemconfig.json on a first run — so a fresh installation was written
		// a value the framework would refuse. Actuation because driving a plug
		// or a light is what distinguishes this gateway from a sensor; the
		// assets it builds from device discovery decide for themselves, and a
		// device it can only read declares measurement there.
		Mission:     components.MissionActuation,
		Details:     map[string][]string{},
		ServicesMap: components.Services{},
		Traits: &DeconzConfig{
			Host:    "localhost",
			APIPort: 80,
			WSPort:  80,
			APIKey:  "your_deconz_api_key",
			Period:  30,
		},
	}
}

// normalizeName converts a deCONZ device name to a valid Arrowhead asset name.
// Spaces, dashes, and non-alphanumeric characters are replaced with underscores.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// macPrefix extracts the IEEE 802.15.4 MAC address from a deCONZ uniqueid.
// deCONZ uniqueids have the form "AA:BB:CC:DD:EE:FF:GG:HH-endpoint-cluster".
// The MAC is the first 8 colon-separated octets, shared by every endpoint of
// the same physical device (e.g. an Aqara plug's switch and power meter).
func macPrefix(uniqueID string) string {
	parts := strings.SplitN(uniqueID, "-", 2)
	return parts[0]
}

// newResources discovers all ZigBee devices from deCONZ, builds one UnitAsset per
// physical device (merging services from lights and sensors that share the same MAC
// address — typical for Aqara smart plugs), starts the WebSocket listener and
// REST-poll goroutines, and returns the assets.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	var cfg DeconzConfig
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("beekeeper: unmarshal config: %v\n", err)
		}
	}
	if cfg.Period == 0 {
		cfg.Period = 30
	}

	lights, sensors, err := fetchAllDevices(cfg)
	if err != nil {
		log.Fatalf("beekeeper: device discovery failed: %v\n", err)
	}
	log.Printf("beekeeper: discovered %d light(s), %d sensor(s)\n", len(lights), len(sensors))

	// assetSpec accumulates everything known about one physical device.
	type assetSpec struct {
		displayName string   // taken from the light entry when present, else sensor
		services    []string // deduplicated list
		entries     []assetEntry
	}

	// mac → assetSpec
	byMAC := make(map[string]*assetSpec)

	// mac → normalized asset name (derived from the light's friendly name when available)
	macToName := make(map[string]string)

	addToSpec := func(mac, resource, id, displayName string, svcs []string) {
		spec := byMAC[mac]
		if spec == nil {
			spec = &assetSpec{}
			byMAC[mac] = spec
		}
		// Prefer the light's name as the display name (it is user-set in Phoscon).
		if resource == "lights" || spec.displayName == "" {
			spec.displayName = displayName
			norm := normalizeName(displayName)
			if norm == "" {
				norm = resource + "_" + id
			}
			macToName[mac] = norm
		}
		spec.entries = append(spec.entries, assetEntry{resource, id})
		for _, svc := range svcs {
			found := false
			for _, existing := range spec.services {
				if existing == svc {
					found = true
					break
				}
			}
			if !found {
				spec.services = append(spec.services, svc)
			}
		}
	}

	for id, light := range lights {
		svcs, ok := lightServices[light.Type]
		if !ok {
			log.Printf("beekeeper: unknown light type %q (%s) — skipping\n", light.Type, light.Name)
			continue
		}
		addToSpec(macPrefix(light.UniqueID), "lights", id, light.Name, svcs)
	}
	for id, sensor := range sensors {
		svcs, ok := sensorServices[sensor.Type]
		if !ok {
			continue // silently skip CLIP and other non-ZHA types
		}
		addToSpec(macPrefix(sensor.UniqueID), "sensors", id, sensor.Name, svcs)
	}

	if len(byMAC) == 0 {
		log.Fatal("beekeeper: no supported ZigBee devices found — check deCONZ pairing and API key")
	}

	// Build specs keyed by asset name and the asset index for goroutine routing.
	type namedSpec struct {
		name string
		*assetSpec
	}
	var namedSpecs []namedSpec
	for mac, spec := range byMAC {
		name, ok := macToName[mac]
		if !ok || name == "" {
			name = "device_" + mac
		}
		namedSpecs = append(namedSpecs, namedSpec{name, spec})
	}

	// Build the asset index used by the WebSocket and REST-poll goroutines (read-only after this).
	assetIndex := make(map[string]string) // "lights:3" → asset name
	for _, ns := range namedSpecs {
		for _, entry := range ns.entries {
			assetIndex[entry.resource+":"+entry.id] = ns.name
		}
	}

	cache := newDeviceCache()

	// Pre-populate the cache from the initial REST fetch.
	ts := time.Now()
	for id, light := range lights {
		if name, ok := assetIndex["lights:"+id]; ok {
			cache.update(name, extractLightMeasurements(light), ts)
		}
	}
	for id, sensor := range sensors {
		if name, ok := assetIndex["sensors:"+id]; ok {
			cache.update(name, extractSensorMeasurements(sensor), ts)
		}
	}

	var assets []*components.UnitAsset
	for _, ns := range namedSpecs {
		// Find the deCONZ light ID for this asset (needed to forward PUT on_off commands).
		lightID := ""
		for _, e := range ns.entries {
			if e.resource == "lights" {
				lightID = e.id
				break
			}
		}
		ua := newDeviceAsset(ns.name, ns.displayName, ns.services, lightID, cfg, sys, cache)
		assets = append(assets, ua)
		log.Printf("beekeeper: asset %q  services: %v\n", ns.name, ns.services)
	}

	go listenWebSocket(sys.Ctx, cfg, cache, assetIndex)
	go pollREST(sys.Ctx, cfg, cache, assetIndex)

	return assets, func() {
		log.Println("beekeeper: disconnecting from deCONZ")
	}
}

// newDeviceAsset creates a UnitAsset for one ZigBee device.
func newDeviceAsset(assetName, displayName string, services []string, lightID string, cfg DeconzConfig, sys *components.System, cache *DeviceCache) *components.UnitAsset {
	t := &Traits{assetName: assetName, lightID: lightID, cfg: cfg, cache: cache}

	svcMap := make(components.Services)
	for _, svc := range services {
		spec, ok := serviceSpecs[svc]
		if !ok {
			continue
		}
		mission, err := missionForService(svc)
		if err != nil {
			log.Fatalf("beekeeper: asset %q: %v\n", assetName, err)
		}
		s := &components.Service{
			Definition:  spec.definition,
			SubPath:     svc,
			Mission:     mission,
			Details:     map[string][]string{"Unit": {spec.unit}, "Forms": {"SignalA_v1a"}, "Methods": methodsFor(svc)},
			RegPeriod:   30,
			Description: spec.description,
		}
		svcMap[svc] = s
	}

	// The asset's own mission is the fallback for any service that declares
	// none; a device that can be driven at all is an actuator, and everything
	// else observes. Every service above declares one, so this is documentation
	// rather than a decision.
	assetMission := components.MissionMeasurement
	if lightID != "" {
		assetMission = components.MissionActuation
	}

	ua := &components.UnitAsset{
		Name:    assetName,
		Mission: assetMission,
		Owner:   sys,
		Details: map[string][]string{
			"DisplayName": {displayName},
		},
		ServicesMap: svcMap,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// fetchAllDevices retrieves all lights and sensors from the deCONZ REST API.
func fetchAllDevices(cfg DeconzConfig) (map[string]DeconzLight, map[string]DeconzSensor, error) {
	lights := make(map[string]DeconzLight)
	sensors := make(map[string]DeconzSensor)
	if err := getJSON(cfg.apiBase()+"/lights", &lights); err != nil {
		return nil, nil, fmt.Errorf("fetch lights: %w", err)
	}
	if err := getJSON(cfg.apiBase()+"/sensors", &sensors); err != nil {
		return nil, nil, fmt.Errorf("fetch sensors: %w", err)
	}
	return lights, sensors, nil
}

// getJSON performs a GET request and decodes the JSON response body into target.
func getJSON(url string, target interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

// pollREST periodically refreshes the cache from the deCONZ REST API.
// This is a fallback for devices whose WebSocket events are missed during reconnection gaps.
func pollREST(ctx context.Context, cfg DeconzConfig, cache *DeviceCache, assetIndex map[string]string) {
	period := cfg.Period * time.Second
	if period <= 0 {
		period = 30 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lights, sensors, err := fetchAllDevices(cfg)
			if err != nil {
				log.Printf("deCONZ REST poll error: %v\n", err)
				continue
			}
			ts := time.Now()
			for id, light := range lights {
				if name, ok := assetIndex["lights:"+id]; ok {
					cache.update(name, extractLightMeasurements(light), ts)
				}
			}
			for id, sensor := range sensors {
				if name, ok := assetIndex["sensors:"+id]; ok {
					cache.update(name, extractSensorMeasurements(sensor), ts)
				}
			}
		}
	}
}

// listenWebSocket connects to the deCONZ WebSocket and applies state-change events to
// the cache. It reconnects automatically on disconnect with a 10 s back-off.
func listenWebSocket(ctx context.Context, cfg DeconzConfig, cache *DeviceCache, assetIndex map[string]string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, cfg.wsURL(), nil)
		if err != nil {
			log.Printf("deCONZ WebSocket: connect failed (%v) — retrying in 10 s\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		log.Println("deCONZ WebSocket: connected")

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("deCONZ WebSocket: read error (%v) — reconnecting\n", err)
				conn.Close()
				break
			}

			var evt WSEvent
			if err := json.Unmarshal(msg, &evt); err != nil || evt.Event != "changed" {
				continue
			}

			key := evt.Resource + ":" + evt.ID
			assetName, ok := assetIndex[key]
			if !ok {
				continue
			}

			ts := time.Now()
			var measurements map[string]float64

			switch evt.Resource {
			case "lights":
				var st wsLightState
				if err := json.Unmarshal(evt.State, &st); err == nil {
					measurements = make(map[string]float64)
					if st.On != nil {
						v := 0.0
						if *st.On {
							v = 1.0
						}
						measurements["on_off"] = v
					}
					if st.Bri != nil {
						measurements["brightness"] = float64(*st.Bri) / 254.0 * 100.0
					}
				}
			case "sensors":
				var st SensorState
				if err := json.Unmarshal(evt.State, &st); err == nil {
					measurements = sensorStateToMap(st)
				}
			}

			if len(measurements) > 0 {
				cache.update(assetName, measurements, ts)
			}
		}
	}
}
