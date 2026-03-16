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
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// Define your global variable
var messageList map[string][]byte

func init() {
	// Initialize the map
	messageList = make(map[string][]byte)
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
	Message  []byte      `json:"-"`
	owner    *components.System  `json:"-"`
	cervices components.Cervices `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	access := components.Service{
		Definition:  "temperature",
		SubPath:     "access",
		Details:     map[string][]string{"forms": {"payload"}},
		RegPeriod:   30,
		Description: "Read the current topic message (GET) or publish to it (PUT)",
	}

	return &components.UnitAsset{
		Name:    "Kitchen/temperature",
		Mission: "passon_message",
		Details: map[string][]string{"mqtt": {"home"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Traits: &Traits{
			Broker:   "tcp://localhost:1883",
			Username: "user",
			Password: "password",
			Pattern:  []string{"Room"},
			Period:   -1,
		},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConig structs
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

	for _, raw := range configuredAsset.Traits {
		if err := json.Unmarshal(raw, t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
		break
	}

	t.Topic = topic

	if len(t.Pattern) <= 0 {
		log.Fatal("Error: UnitAsset must have at least one pattern defined in Traits")
	}

	// Fill Details from pattern and topic
	metaDetails := strings.Split(asset, "/")
	topicDetrails := make(map[string][]string)
	for i := 0; i < len(t.Pattern) && i < len(metaDetails); i++ {
		topicDetrails[t.Pattern[i]] = append(configuredAsset.Details[t.Pattern[i]], metaDetails[i])
	}
	if configuredAsset.Details == nil {
		configuredAsset.Details = make(map[string][]string)
	}
	configuredAsset.Details = components.MergeDetails(configuredAsset.Details, topicDetrails)

	ua := &components.UnitAsset{
		Name:    assetName,
		Owner:   sys,
		Details: configuredAsset.Details,
		Traits:  t,
	}

	// Make the topic an Arrowhead service (since we are subscribing to it)
	if t.Period < 0 {
		access := components.Service{
			Definition:  service,
			SubPath:     "access",
			Details:     map[string][]string{"forms": {"mqttPayload"}},
			RegPeriod:   30,
			Description: "Read the current topic message (GET) or publish to it (PUT)",
		}
		ua.ServicesMap = components.Services{access.SubPath: &access}
	}

	// Make the topic a consumed service to be published (since we are consuming it)
	if t.Period >= 0 {
		sProtocols := components.SProtocols(sys.Husk.ProtoPort)
		newCervice := &components.Cervice{
			Definition: service,
			Protos:     sProtocols,
			Nodes:      make(map[string][]string),
		}
		newCervice.Details = topicDetrails
		cervMap := components.Cervices{newCervice.Definition: newCervice}
		t.cervices = cervMap
		ua.CervicesMap = cervMap
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

			if messageList == nil {
				messageList = make(map[string][]byte)
			}
			t.Message = msg.Payload()
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

//-------------------------------------Unit asset's resource functions

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
