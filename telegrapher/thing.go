/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
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
	"fmt"
	"log"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/sdoque/mbaigo/components"
)

// Define your global variable
var messageList map[string][]byte

func init() {
	// Initialize the map
	messageList = make(map[string][]byte)
}

//-------------------------------------Define the unit asset

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	//
	Broker     string   `json:"broker"`
	Topics     []string `json:"topics"`
	Pattern    []string `json:"pattern"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	client     mqtt.Client
	topic      string
	serviceDef string
	metatopic  string
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

// ensure UnitAsset implements components.UnitAsset (this check is done at during the compilation)
var _ components.UnitAsset = (*UnitAsset)(nil)

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	access := components.Service{
		Definition:  "access",
		SubPath:     "access",
		Details:     map[string][]string{"forms": {"payload"}},
		RegPeriod:   30,
		Description: "Read the current topic message (GET) or publish to it (PUT)",
	}

	uat := &UnitAsset{
		Name:    "MQTT Broker",
		Details: map[string][]string{"mqtt": {"home"}},
		ServicesMap: components.Services{
			access.SubPath: &access,
		},
		Broker:   "tcp://10.0.0.33:1883",
		Username: "aiko",
		Password: "babe",
		Topics:   []string{"kitchen/temperature", "topic2", "topic3"}, // Default topics
		Pattern:  []string{"pattern1", "pattern2", "pattern3"},        // Default patterns
	}
	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConig structs
func newResource(uac UnitAsset, sys *components.System, servs []components.Service) ([]components.UnitAsset, func()) {
	// Create MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(uac.Broker)
	opts.SetUsername(uac.Username)
	opts.SetPassword(uac.Password)

	// Create and start the MQTT client
	mClient := mqtt.NewClient(opts)
	if token := mClient.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Error connecting to MQTT broker: %v", token.Error())
	}
	fmt.Println("Connected to MQTT broker")

	assetList := []components.UnitAsset{}
	assetMap := make(map[string]components.UnitAsset) // Map asset names to UnitAssets

	// Define the message handler callback
	messageHandler := func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())

		// Ensure the map is initialized (just in case)
		if messageList == nil {
			messageList = make(map[string][]byte)
		}

		messageList[msg.Topic()] = msg.Payload() // Assign message to topic in the map
	}

	for _, topicItem := range uac.Topics {
		// Consider the last term of a topic to be a service, and the preceding part is the asset
		lastSlashIndex := strings.LastIndex(topicItem, "/")
		if lastSlashIndex == -1 {
			fmt.Printf("topic %s has no forward slash and is ignored\n", topicItem)
			continue
		}
		a := topicItem[:lastSlashIndex]   // The asset part
		s := topicItem[lastSlashIndex+1:] // The service part
		aName := strings.ReplaceAll(a, "/", "_")

		// Redefine the service
		access := components.Service{
			Definition:  s,
			SubPath:     s,
			Details:     map[string][]string{"forms": {"mqttPayload"}},
			RegPeriod:   30,
			Description: "Read the current topic message (GET) or publish to it (PUT)",
		}

		// Check if the unit asset already exists in the assetMap
		ua, exists := assetMap[aName]

		if !exists {
			// Instantiate a new concrete type `MyUnitAsset` implementing `UnitAsset`
			ua := &UnitAsset{
				Name:    aName,
				Owner:   sys,
				Details: make(map[string][]string), // Initialize the map here
				ServicesMap: components.Services{
					access.SubPath: &access,
				},
				// Initialize fields
				client:     mClient,
				topic:      topicItem,
				serviceDef: s,
				metatopic:  a,
			}

			// Add details on the unit asset based on the topic
			metaDetails := strings.Split(a, "/")
			for i := 0; i < len(uac.Pattern) && i < len(metaDetails); i++ {
				ua.Details[uac.Pattern[i]] = append(ua.Details[uac.Pattern[i]], metaDetails[i])
			}

			// Add the new asset to the assetList and assetMap
			assetList = append(assetList, ua)
			assetMap[aName] = ua
		} else {
			// If the asset exists, just add the new service to the ServicesMap
			ua.(*UnitAsset).ServicesMap[access.SubPath] = &access
		}
		// Subscribe to the topic
		if token := mClient.Subscribe(topicItem, 0, messageHandler); token.Wait() && token.Error() != nil {
			log.Fatalf("Error subscribing to topic: %v", token.Error())
		}
		fmt.Printf("Subscribed to topic: %s\n", topicItem)
	}
	return assetList, func() {
		log.Println("Disconnecting from MQTT broker")
		mClient.Disconnect(250)
	}
}

//-------------------------------------Unit asset's resource functions

// subscribeToTopic subscribes to the given MQTT topic
func (ua *UnitAsset) subscribeToTopic() {
	// Define the message handler callback
	messageHandler := func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())
		// ua.message = msg.Payload()
	}

	// Subscribe to the topic
	theTopic := ua.metatopic + "/" + ua.serviceDef
	if token := ua.client.Subscribe(theTopic, 0, messageHandler); token.Wait() && token.Error() != nil {
		log.Fatalf("Error subscribing to topic: %v", token.Error())
	}
	fmt.Printf("Subscribed to topic: %s\n", theTopic)
}
