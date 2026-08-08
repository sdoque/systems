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
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// reading is one MQTT message and the moment it arrived.
//
// They are one observation and are swapped as one. Held as separate fields and
// read separately, the HTTP handler could take the payload from message n and
// the timestamp from message n+1, and serve a signal whose value and timestamp
// never coexisted.
type reading struct {
	payload  []byte
	received time.Time
}

// -------------------------------------Define the unit asset
// Traits are Asset-specific configurable parameters and variables
type Traits struct {
	Broker   string      `json:"broker"`
	mClient  mqtt.Client `json:"-"`
	Pattern  []string    `json:"pattern"`
	Username string      `json:"username"`
	Password string      `json:"password"`
	Topic    string      `json:"-"`      // Topic is the MQTT topic to which the unit asset subscribes or publishes
	Period   int         `json:"period"` // Period is the time interval for periodic service consumption, e.g., 30 seconds
	// latest is the most recent message. Written by the Paho callback, which
	// runs on the client's own goroutine, and read by the HTTP handler — so it
	// is swapped atomically rather than assigned.
	latest   atomic.Pointer[reading]
	unit     string              `json:"-"` // the configured unit, empty for a topic served raw
	owner    *components.System  `json:"-"`
	cervices components.Cervices `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// The mission is declared per service, not on the unit asset: the telegrapher
	// is a bridge to an MQTT broker rather than a thing, and a topic path
	// discloses nothing about whether what sits behind it is observed or driven.
	// Only whoever configures the topic knows, so only they can say.
	access := components.Service{
		Definition: "temperature",
		SubPath:    "access",
		Mission:    components.MissionMeasurement,
		// Most MQTT topics in a plant carry an analog signal — a temperature or a
		// pressure from an ESP32 — so that is what the template assumes. Declaring
		// a Unit is what says so: with one, the topic is served as a SignalA_v1a
		// and a consumer can convert it; without one it is passed through raw, as
		// a topic carrying something else must be.
		Details: map[string][]string{
			"Forms":        {"SignalA_v1a"},
			"Unit":         {"<http://qudt.org/vocab/unit/DEG_C>"},
			"QuantityKind": {"<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"},
		},
		RegPeriod:   30,
		Description: "Read the current topic message (GET) or publish to it (PUT)",
	}

	return &components.UnitAsset{
		Name:    "Kitchen/temperature",
		Details: map[string][]string{"mqtt": {"home"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Traits: &Traits{
			Broker:   "tcp://localhost:1883",
			Username: "user",
			Password: "password",
			Pattern:  []string{"FunctionalLocation"},
			Period:   -1,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the Traits struct
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	topic := configuredAsset.Name
	lastSlashIndex := strings.LastIndex(topic, "/")
	if lastSlashIndex == -1 {
		fmt.Printf("topic %s has no forward slash and is ignored\n", topic)
		return nil, func() {}
	}
	asset := topic[:lastSlashIndex]
	service := topic[lastSlashIndex+1:]
	assetName := strings.ReplaceAll(asset, "/", "_")

	t := &Traits{
		owner: sys,
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	t.Topic = topic

	if len(t.Pattern) <= 0 {
		log.Fatal("Error: UnitAsset must have at least one pattern defined in Traits")
	}

	if configuredAsset.Details == nil {
		configuredAsset.Details = make(map[string][]string)
	}
	for _, serv := range configuredAsset.Services {
		if values := serv.Details["Unit"]; len(values) > 0 {
			t.unit = values[0]
			break
		}
	}

	topicDetails := detailsFromTopic(t.Pattern, asset)
	configuredAsset.Details = components.MergeDetails(configuredAsset.Details, topicDetails)

	ua := &components.UnitAsset{
		Name:    assetName,
		Owner:   sys,
		Details: configuredAsset.Details,
		Traits:  t,
	}

	// Make the topic an Arrowhead service (since we are subscribing to it)
	if t.Period < 0 {
		// Use what was configured. Rebuilding the service here would discard the
		// unit and quantity kind the topic was commissioned with, leaving the
		// record undiscoverable by a consumer that asks for a temperature — and
		// the payload would still carry a unit the registration never mentioned.
		if len(configuredAsset.Services) > 0 {
			ua.ServicesMap = usecases.MakeServiceMap(configuredAsset.Services)
		} else {
			access := components.Service{
				Definition:  service,
				SubPath:     "access",
				Details:     map[string][]string{"Forms": {"mqttPayload"}},
				RegPeriod:   30,
				Description: "Read the current topic message (GET) or publish to it (PUT)",
			}
			ua.ServicesMap = components.Services{access.SubPath: &access}
		}
	}

	// Make the topic a consumed service to be published (since we are consuming it)
	if t.Period >= 0 {
		sProtocols := components.SProtocols(sys.Husk.ProtoPort)
		newCervice := &components.Cervice{
			Definition: service,
			Protos:     sProtocols,
			Nodes:      make(map[string][]components.NodeInfo),
			Mode:       "get",
		}
		newCervice.Details = topicDetails
		cervMap := components.Cervices{newCervice.Definition: newCervice}
		t.cervices = cervMap
		ua.CervicesMap = cervMap

		publish := components.Service{
			Definition:  service,
			SubPath:     "publish",
			Details:     map[string][]string{"forms": {"text/plain"}},
			RegPeriod:   30,
			Description: "Describes the source service being published to MQTT (GET)",
		}
		ua.ServicesMap = components.Services{publish.SubPath: &publish}
	}

	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// Create MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(t.Broker)
	if t.Username != "" {
		opts.SetUsername(t.Username)
		opts.SetPassword(t.Password)
	}
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Printf("Connection lost: %v", err)
	})
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Println("MQTT connection established")
	})

	log.Println("Connecting to broker:", t.Broker)
	t.mClient = mqtt.NewClient(opts)
	if token := t.mClient.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Error connecting to MQTT broker: %v", token.Error())
	}

	log.Println("Connected to MQTT broker")

	if t.Period < 0 {
		messageHandler := func(client mqtt.Client, msg mqtt.Message) {
			fmt.Printf("Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())

			t.latest.Store(&reading{payload: msg.Payload(), received: time.Now()})
		}

		if token := t.mClient.Subscribe(topic, 0, messageHandler); token.Wait() && token.Error() != nil {
			log.Fatalf("Error subscribing to topic: %v", token.Error())
		}
		fmt.Printf("Subscribed to topic: %s\n", topic)
	}

	if t.Period > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(t.Period) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					payload, err := usecases.GetState(t.cervices[service], t.owner)
					if err != nil {
						log.Printf("\nUnable to obtain a %s reading with error %s\n", service, err)
						continue
					}
					fmt.Printf("%+v\n", payload)
					sigForm, ok := payload.(*forms.SignalA_v1a)
					if !ok {
						log.Println("Problem unpacking the signal form")
						continue
					}
					message, err := usecases.Pack(sigForm, "application/json")
					if err != nil {
						log.Printf("Failed to pack signal form: %v", err)
						continue
					}
					if err := t.publishRaw(message); err != nil {
						log.Printf("Periodic publish failed for topic %s: %v", t.Topic, err)
					} else {
						log.Printf("Periodic message sent to topic %s", t.Topic)
					}
				case <-t.owner.Ctx.Done():
					log.Printf("Stopping periodic publishing for %s", t.Topic)
					return
				}
			}
		}()
	}

	return ua, func() {
		log.Println("Disconnecting from MQTT broker")
		t.mClient.Disconnect(250)
	}
}

//-------------------------------------Service handlers

func (t *Traits) access(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch r.Method {
	case "GET":
		// One load, so the payload and its timestamp are from the same message.
		last := t.latest.Load()
		if last == nil || len(last.payload) == 0 {
			http.Error(w, "The subscribed topic is not being published", http.StatusBadRequest)
			return
		}
		msg := last.payload
		if t.unit == "" {
			// No unit declared: the topic carries something this system does not
			// interpret, so it is passed through as it arrived.
			w.WriteHeader(http.StatusOK)
			w.Header().Set("Content-Type", "application/json")
			w.Write(msg)
			return
		}
		value, err := analogValue(msg)
		if err != nil {
			log.Printf("%s: %v", t.Topic, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var f forms.SignalA_v1a
		f.NewForm()
		f.Value = value
		f.Unit = t.unit
		f.Timestamp = last.received
		usecases.HTTPProcessGetRequest(w, r, &f)
	case "PUT":
		log.Printf("MQTT client is connected: %v", t.mClient.IsConnected())

		if err := t.publishRaw([]byte(`{"test":123}`)); err != nil {
			log.Printf("Failed to publish: %v", err)
			http.Error(w, "MQTT publish failed", http.StatusInternalServerError)
			return
		}
		log.Printf("MQTT client is connected: %v", t.mClient.IsConnected())

		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//-------------------------------------Unit asset's resource functions

// publishInfo writes a human-readable description of what is being published to MQTT.
func (t *Traits) publishInfo(w http.ResponseWriter) {
	cer := t.cervices[strings.Split(t.Topic, "/")[len(strings.Split(t.Topic, "/"))-1]]
	sources := []string{}
	if cer != nil {
		for _, nodeInfos := range cer.Nodes {
			for _, ni := range nodeInfos {
				sources = append(sources, ni.URL)
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain")
	if len(sources) == 0 {
		fmt.Fprintf(w, "Source: pending discovery\nMQTT topic: %s\nBroker: %s\nPeriod: %d s\n",
			t.Topic, t.Broker, t.Period)
	} else {
		for _, src := range sources {
			fmt.Fprintf(w, "Source: %s\nMQTT topic: %s\nBroker: %s\nPeriod: %d s\n",
				src, t.Topic, t.Broker, t.Period)
		}
	}
}

// publishToTopic publishes a payload to the MQTT topic of the unit asset.
func (t *Traits) publishToTopic(payload map[string]interface{}, contentType string) error {
	if t.mClient == nil {
		return fmt.Errorf("MQTT client not initialized")
	}

	var data []byte
	var err error
	switch contentType {
	case "application/json":
		data, err = json.Marshal(payload)
	default:
		data, err = json.Marshal(payload)
	}
	if err != nil {
		return fmt.Errorf("failed to encode payload: %w", err)
	}
	log.Println(contentType)

	token := t.mClient.Publish(t.Topic, 0, false, data)
	token.Wait()
	if token.Error() != nil {
		return fmt.Errorf("publish error: %w", token.Error())
	}
	return nil
}

// publishRaw publishes raw data to the MQTT topic of the unit asset.
func (t *Traits) publishRaw(data []byte) error {
	token := t.mClient.Publish(t.Topic, 0, false, data)

	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			log.Printf("Async publish error: %v", err)
		}
	}()

	return nil
}

// detailsFromTopic maps the segments of a topic onto the detail keys the pattern
// names, so that "Bathroom/temperature" under a pattern of FunctionalLocation
// registers the room rather than leaving it in the asset's name.
//
// The key matters more than it looks. The authorizer's pairing rule and the
// knowledge graph both look up the literal string FunctionalLocation, so a topic
// filed under any other key is an asset with no location at all — and an asset
// with no location is universally reachable, which is the permissive answer
// arrived at silently.
func detailsFromTopic(pattern []string, asset string) map[string][]string {
	segments := strings.Split(asset, "/")
	details := make(map[string][]string)
	for i := 0; i < len(pattern) && i < len(segments); i++ {
		if pattern[i] == "" || segments[i] == "" {
			continue
		}
		details[pattern[i]] = append(details[pattern[i]], segments[i])
	}
	return details
}

// analogValue reads a number out of an MQTT payload.
//
// Devices publish a reading in whichever of two shapes their firmware author
// preferred: a bare number, or a small JSON object with the reading under some
// key. Both are accepted, because a plant will contain both and neither is
// wrong.
//
// A payload that yields no number is an error rather than a zero. Zero is a
// plausible temperature, so guessing it would put a fabricated reading into a
// control loop.
func analogValue(payload []byte) (float64, error) {
	text := strings.TrimSpace(string(payload))

	if v, err := strconv.ParseFloat(text, 64); err == nil {
		return v, nil
	}

	var object map[string]any
	if err := json.Unmarshal(payload, &object); err == nil {
		// Only fields that plausibly carry the reading. Taking any number would
		// serve a humidity as a temperature: {"humidity":45,"temperature":21.5}
		// has no order that makes 45 the right answer, and the service is
		// registered as a temperature in degrees Celsius.
		for _, key := range []string{"value", "temperature", "temp", "pressure", "level"} {
			if v, ok := numberFrom(object[key]); ok {
				return v, nil
			}
		}
		return 0, fmt.Errorf("payload %q carries no reading under a name this system recognizes", truncate(text))
	}
	return 0, fmt.Errorf("no number in the payload %q", truncate(text))
}

func numberFrom(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
