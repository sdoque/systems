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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// DeconzConfig holds the connection parameters for the deCONZ gateway.
type DeconzConfig struct {
	Host    string `json:"host"`
	APIPort int    `json:"apiPort"`
	WSPort  int    `json:"wsPort"`
	APIKey  string `json:"apiKey"`
	// Period is the sampling period in seconds.
	//
	// An int rather than a time.Duration, because a Duration holding the number
	// 10 means ten nanoseconds and only becomes ten seconds when multiplied by
	// time.Second — which works, and reads as if the field were already a
	// duration. Anyone writing the obvious `Period: time.Second` for one second
	// would get 10^9 seconds, about 31 years, and the compiler would not object.
	// The unit belongs in the name and the conversion belongs at the point of
	// use.
	Period int `json:"period"`
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

	// Where the gateway says each device is. Not worth failing discovery over: an
	// asset with no functional location is merely less well described, whereas a
	// cottage with no heater services is a cold room.
	lightLocations, err := fetchFunctionalLocations(cfg)
	if err != nil {
		log.Printf("beekeeper: %v — assets will carry no functional location\n", err)
	}

	// assetSpec accumulates everything known about one physical device.
	type assetSpec struct {
		displayName string   // taken from the light entry when present, else sensor
		services    []string // deduplicated list
		locations   []string // deCONZ group names, from the light entry
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
		// Only lights carry group membership: a group's sensor list holds the
		// switches that command it, which is wiring rather than placement.
		if resource == "lights" {
			for _, loc := range lightLocations[id] {
				spec.locations = appendUnique(spec.locations, loc)
			}
		}
		spec.entries = append(spec.entries, assetEntry{resource, id})
		for _, svc := range svcs {
			spec.services = appendUnique(spec.services, svc)
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
		ua := newDeviceAsset(ns.name, ns.displayName, ns.services, ns.locations, lightID, cfg, sys, cache)
		assets = append(assets, ua)
		where := ""
		if len(ns.locations) > 0 {
			where = fmt.Sprintf("  at: %v", ns.locations)
		}
		log.Printf("beekeeper: asset %q  services: %v%s\n", ns.name, ns.services, where)
	}

	go listenWebSocket(sys.Ctx, cfg, cache, assetIndex)
	go pollREST(sys.Ctx, cfg, cache, assetIndex)

	return assets, func() {
		log.Println("beekeeper: disconnecting from deCONZ")
	}
}

// newDeviceAsset creates a UnitAsset for one ZigBee device.
func newDeviceAsset(assetName, displayName string, services, locations []string, lightID string, cfg DeconzConfig, sys *components.System, cache *DeviceCache) *components.UnitAsset {
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
		// A service with no unit says nothing about units. Carrying an empty
		// one wrote afo:hasUnit "" into the graph, a literal where an object
		// property expects a qudt:Unit — five of them at the cottage, on the
		// booleans that have no unit to give.
		details := map[string][]string{"Forms": {"SignalA_v1a"}, "Methods": methodsFor(svc)}
		if spec.unit != "" {
			details["Unit"] = []string{spec.unit}
		}
		s := &components.Service{
			Definition:  spec.definition,
			SubPath:     svc,
			Mission:     mission,
			Details:     details,
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

	details := map[string][]string{
		"DisplayName": {displayName},
	}
	// Omitted rather than empty when the gateway places the device nowhere, on the
	// same reasoning as an empty unit: a detail that says nothing is noise in the
	// knowledge graph.
	if len(locations) > 0 {
		details["FunctionalLocation"] = locations
	}

	ua := &components.UnitAsset{
		Name:        assetName,
		Mission:     assetMission,
		Owner:       sys,
		Details:     details,
		ServicesMap: svcMap,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// fetchAllDevices retrieves all lights and sensors from the deCONZ REST API.
// DeconzGroup is a group as returned by /groups. Phoscon presents groups to the
// user as rooms, which is what makes a group name the closest thing the gateway
// holds to a functional location.
type DeconzGroup struct {
	Name   string   `json:"name"`
	Hidden bool     `json:"hidden"`
	Lights []string `json:"lights"` // deCONZ light IDs
}

// reservedAllGroup is ZigBee's 0xFFF0 broadcast group, which every node joins and
// which therefore names no place. The REST API omits it, but a node's own group
// list carries it, so it is excluded here rather than assumed away.
const reservedAllGroup = "65520"

// fetchFunctionalLocations maps a deCONZ light ID to the names of the groups that
// place it. A group qualifies only if it names a place: not hidden, not the
// broadcast group, and not one of Phoscon's internal groups, which carry a
// "Phoscon_" prefix. An empty group contributes nothing by construction.
//
// Membership is read once, at discovery. Moving a plug to another room in Phoscon
// changes the graph at the next restart, not immediately.
func fetchFunctionalLocations(cfg DeconzConfig) (map[string][]string, error) {
	groups := make(map[string]DeconzGroup)
	if err := getJSON(cfg.apiBase()+"/groups", &groups); err != nil {
		return nil, fmt.Errorf("fetch groups: %w", err)
	}
	byLight := make(map[string][]string)
	for id, group := range groups {
		if group.Hidden || id == reservedAllGroup || strings.HasPrefix(group.Name, "Phoscon_") {
			continue
		}
		place := placeName(group.Name)
		if place == "" {
			log.Printf("beekeeper: group %q yields no usable place name — ignored\n", group.Name)
			continue
		}
		for _, lightID := range group.Lights {
			byLight[lightID] = appendUnique(byLight[lightID], place)
		}
	}
	// A device may be in more than one group, and map iteration is unordered:
	// sort so the same gateway produces the same graph on every run.
	for _, names := range byLight {
		sort.Strings(names)
	}
	return byLight, nil
}

// placeName turns a Phoscon room name into the local part of an IRI:
// "Living room" becomes "LivingRoom".
//
// This is not cosmetic. afo:hasFunctionalLocation is an object property whose
// range is afo:FunctionalLocation, so its object must be an IRI. The knowledge
// graph mints one only when the detail value is a legal name — a value with a
// space in it becomes a string literal instead, which contradicts the range and
// joins to nothing. "Living room" would also miss alc:LivingRoom in the local
// classification scheme, which is the whole reason for publishing the group.
//
// Words are capitalized and joined; anything already written as one word is left
// as it is, so "LivingRoom" survives unchanged.
func placeName(groupName string) string {
	var b strings.Builder
	newWord := true
	for _, r := range groupName {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			if newWord && r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			newWord = false
		case r == '\'' || r == '\u2019':
			// An apostrophe separates nothing: "Jan's office" is two words, not
			// three, so it is dropped without starting a new one.
		default:
			newWord = true
		}
	}
	return b.String()
}

// appendUnique adds value to list unless it is already there.
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

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
	period := time.Duration(cfg.Period) * time.Second
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
