package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Define the SignalA_v1a struct with JSON and XML annotations
type SignalA_v1a struct {
	XMLName   xml.Name  `json:"-" xml:"SignalA"`
	Value     float64   `json:"value" xml:"value"`
	Unit      string    `json:"unit" xml:"unit"`
	Timestamp time.Time `json:"timestamp" xml:"timestamp"`
	Version   string    `json:"version" xml:"version"`
}

// Config structure to hold MQTT settings
type Config struct {
	Broker   string `json:"broker"`
	Topic    string `json:"topic"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func loadConfig(filename string) (Config, error) {
	var config Config

	// Check if config file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		// If file doesn't exist, create it with template settings
		fmt.Println("Config file not found, creating default config...")
		defaultConfig := Config{
			Broker:   "tcp://localhost:1883",
			Topic:    "myhome/groundfloor/kitchen/temperature",
			Username: "user",
			Password: "password",
		}

		// Write default config to file
		data, err := json.MarshalIndent(defaultConfig, "", "  ")
		if err != nil {
			return config, fmt.Errorf("could not marshal default config: %v", err)
		}

		if err := os.WriteFile(filename, data, 0644); err != nil {
			return config, fmt.Errorf("could not write default config file: %v", err)
		}

		// Return the default config
		return defaultConfig, nil
	}

	// Read the config file
	data, err := os.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("could not read config file: %v", err)
	}

	// Parse the config file
	err = json.Unmarshal(data, &config)
	if err != nil {
		return config, fmt.Errorf("could not unmarshal config file: %v", err)
	}

	return config, nil
}

func main() {
	// Load configuration from file or create default one
	configFile := "config.json"
	config, err := loadConfig(configFile)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	// MQTT connection settings from config file
	broker := config.Broker
	topic := config.Topic
	username := config.Username
	password := config.Password

	// Create MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetUsername(username)
	opts.SetPassword(password)

	// Create and start the MQTT client
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Error connecting to MQTT broker: %v", token.Error())
	}
	defer client.Disconnect(250)

	// Constants
	period := 30.0              // period of 30 seconds
	amplitude := 20.0           // amplitude is half of peak-to-peak (40/2 = 20)
	offset := 20.0              // offset to make the wave oscillate between 0 and 40
	interval := 1 * time.Second // publish interval of 1 second
	startTime := time.Now()

	// Infinite loop to publish the sine wave value every second
	for {
		// Calculate elapsed time in seconds
		elapsed := time.Since(startTime).Seconds()

		// Calculate the sine wave value (sin(2π * t / T))
		sineValue := amplitude*math.Sin(2*math.Pi*elapsed/period) + offset

		// Create the payload
		payload := SignalA_v1a{
			Value:     sineValue,
			Unit:      "celsius",
			Timestamp: time.Now(),
			Version:   "SignalA_v1.0",
		}

		// Marshal the struct to JSON
		jsonPayload, err := json.Marshal(payload)
		if err != nil {
			log.Printf("Error marshaling JSON: %v", err)
			continue
		}

		// Publish the JSON payload to the MQTT broker
		token := client.Publish(topic, 0, false, jsonPayload)
		token.Wait()

		// Print the published value for debugging
		fmt.Printf("Published to %s: %s\n", topic, jsonPayload)

		// Sleep for 1 second before publishing the next value
		time.Sleep(interval)
	}
}
