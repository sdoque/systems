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
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sdoque/mbaigo/components"
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
	Period   int         `json:"period"` // Period is the time interval for periodic service consumption, e.g., 30 seconds
	Topic    string      `json:"-"`      // Topic is the MQTT topic to which the unit asset subscribes
	Message  []byte      `json:"-"`
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"topic"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	Traits
}

// GetName returns the name of the Resource.
func (ua *UnitAsset) GetName() string {
	return ua.Name
}

// GetServices returns the services of the Resource.
func (ua *UnitAsset) GetServices() components.Services {
	return ua.ServicesMap
}

// GetCervices returns the list of consumed services by the Resource.
func (ua *UnitAsset) GetCervices() components.Cervices {
	return ua.CervicesMap
}

// GetDetails returns the details of the Resource.
func (ua *UnitAsset) GetDetails() map[string][]string {
	return ua.Details
}

// GetTraits returns the traits of the Resource.
func (ua *UnitAsset) GetTraits() any {
	return ua.Traits
}

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	access := components.Service{
		Definition:  "temperature",
		SubPath:     "access",
		Details:     map[string][]string{"forms": {"payload"}},
		RegPeriod:   30,
		Description: "Read the current topic message (GET) or publish to it (PUT)",
	}

	assetTraits := Traits{
		Broker:   "tcp://localhost:1883",
		Username: "user",
		Password: "password",
		// Topic:    "kitchen/temperature", // Default topics
		Pattern: []string{"Room"}, // Default patterns e.g. "House", "Room" as in "MyHouse/Kitchen"
	}

	uat := &UnitAsset{
		Name:    "Kitchen/temperature",
		Details: map[string][]string{"mqtt": {"home"}},
		Traits:  assetTraits,
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	topic := configuredAsset.Name
	assetName := strings.ReplaceAll(topic, "/", "_")
	// instantiate the unit asset
	ua := &UnitAsset{
		Name:        assetName,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	if len(ua.Pattern) > 0 {
		lastSlashIndex := strings.LastIndex(topic, "/")
		if lastSlashIndex == -1 {
			fmt.Printf("topic %s has no forward slash and is ignored\n", topic)
			return nil, func() {}
		}
		a := topic[:lastSlashIndex]
		s := topic[lastSlashIndex+1:]

		// Fill Details from pattern and topic
		metaDetails := strings.Split(a, "/")
		if ua.Details == nil {
			ua.Details = make(map[string][]string)
		}
		for i := 0; i < len(ua.Pattern) && i < len(metaDetails); i++ {
			ua.Details[ua.Pattern[i]] = append(ua.Details[ua.Pattern[i]], metaDetails[i])
		}

		access := components.Service{
			Definition:  s,
			SubPath:     "access",
			Details:     map[string][]string{"forms": {"mqttPayload"}},
			RegPeriod:   30,
			Description: "Read the current topic message (GET) or publish to it (PUT)",
		}
		ua.ServicesMap[access.SubPath] = &access

		// If the unit asset has a period defined, instantiate the consumed services
		// if ua.Period > 0 {
		// 	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
		// 	t := &components.Cervice{
		// 		Definition: s,
		// 		Protos:     sProtocols,
		// 		Nodes:      make(map[string][]string), // ✅ Corrected
		// 	}
		// 	if ua.CervicesMap == nil {
		// 		ua.CervicesMap = make(components.Cervices)
		// 	}
		// 	ua.CervicesMap[t.Definition] = t
		// }

	}

	// Create MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(ua.Broker)
	if ua.Username != "" { // Password can be empty string for some brokers
		opts.SetUsername(ua.Username)
		opts.SetPassword(ua.Password)
	}
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Printf("Connection lost: %v", err)
	})
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Println("MQTT connection established")
	})

	// Create and start the MQTT client
	log.Println("Connecting to broker:", ua.Traits.Broker)
	ua.mClient = mqtt.NewClient(opts)
	if token := ua.mClient.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Error connecting to MQTT broker: %v", token.Error())
	}

	log.Println("Connected to MQTT broker")

	// Define the message handler callback
	messageHandler := func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())

		// Ensure the map is initialized (just in case)
		if messageList == nil {
			messageList = make(map[string][]byte)
		}
		ua.Message = msg.Payload() // Assign message to topic in the map
	}

	// Subscribe to the topic
	if token := ua.mClient.Subscribe(topic, 0, messageHandler); token.Wait() && token.Error() != nil {
		log.Fatalf("Error subscribing to topic: %v", token.Error())
	}
	fmt.Printf("Subscribed to topic: %s\n", topic)

	// Periodically publish a message to the topic
	// if ua.Period > 0 {
	// 	go func(ua *UnitAsset) {
	// 		ticker := time.NewTicker(time.Duration(ua.Period) * time.Second)
	// 		defer ticker.Stop()
	// 		for {
	// 			select {
	// 			case <-ticker.C:
	// 				payload := map[string]interface{}{
	// 					"value":     0,
	// 					"unit":      "celsius",
	// 					"timestamp": time.Now().Format(time.RFC3339),
	// 					"version":   "SignalA_v1.0",
	// 				}
	// 				if err := ua.publishToTopic(payload, "application/json"); err != nil {
	// 					log.Printf("Periodic publish failed for topic %s: %v", ua.Topic, err)
	// 				} else {
	// 					log.Printf("Periodic message sent to topic %s", ua.Topic)
	// 				}
	// 			case <-sys.Ctx.Done():
	// 				log.Printf("Stopping periodic publishing for %s", ua.Topic)
	// 				return
	// 			}
	// 		}
	// 	}(ua)
	// }

	return ua, func() {
		log.Println("Disconnecting from MQTT broker")
		ua.mClient.Disconnect(250)
	}
}

// UnmarshalTraits unmarshals a slice of json.RawMessage into a slice of Traits.
func UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("failed to unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

//-------------------------------------Unit asset's resource functions

// publishToTopic publishes a payload to the MQTT topic of the unit asset.
func (ua *UnitAsset) publishToTopic(payload map[string]interface{}, contentType string) error {
	if ua.mClient == nil {
		return fmt.Errorf("MQTT client not initialized")
	}

	// Serialize the message based on content type
	// var data []byte
	// var err error
	// switch contentType {
	// case "application/json":
	// 	data, err = json.Marshal(payload)
	// default:
	// 	// Fallback to JSON encoding for now
	// 	data, err = json.Marshal(payload)
	// }
	// if err != nil {
	// 	return fmt.Errorf("failed to encode payload: %w", err)
	// }
	log.Println(contentType)

	token := ua.mClient.Publish(ua.Topic, 0, false, payload)
	token.Wait()
	if token.Error() != nil {
		return fmt.Errorf("publish error: %w", token.Error())
	}
	return nil
}

func (ua *UnitAsset) publishRaw(data []byte) error {
	// Just publish and return immediately
	token := ua.mClient.Publish(ua.Topic, 0, false, data)

	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			log.Printf("Async publish error: %v", err)
		}
	}()

	return nil
}
