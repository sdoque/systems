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
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define a measurement (or signal)
type SignalT struct {
	Name          string              `json:"serviceDefinition"`
	Details       map[string][]string `json:"details"`
	Period        time.Duration       `json:"samplingPeriod"`
	Threshold     float64             `json:"threshold"`
	TOverCount    int                 `json:"-"`
	Operational   bool                `json:"-"`
	WorkRequested bool                `json:"-"`
}

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	SAP_URL string    `json:"sap_url"`
	Signals []SignalT `json:"signals"`
}

// UnitAsset type models the unit asset (interface) of the system
type UnitAsset struct {
	Name        string              `json:"assetName"`
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
	Operational := components.Service{
		Definition:  "SignalMonitoring",
		SubPath:     "monitor",
		Details:     map[string][]string{},
		RegPeriod:   22,
		Description: "monitors the value of the consumed service signal (GET)",
	}

	uat := &UnitAsset{
		Name:    "HealthTracker",
		Details: map[string][]string{},
		Traits: Traits{
			SAP_URL: "http://sap.example.com",
			Signals: []SignalT{
				{
					Name:          "temperature",
					Details:       map[string][]string{"Unit": {"Celsius"}},
					Period:        4,
					Threshold:     75.0,
					TOverCount:    3,
					Operational:   true,
					WorkRequested: false,
				},
			},
		},
		ServicesMap: components.Services{
			Operational.SubPath: &Operational,
		},
	}

	return uat
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates a new UnitAsset resource based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {
	ua := &UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: make(map[string]*components.Cervice), // Initialize map
	}

	traits, err := UnmarshalTraits(configuredAsset.Traits)
	if err != nil {
		log.Println("Warning: could not unmarshal traits:", err)
	} else if len(traits) > 0 {
		ua.Traits = traits[0] // or handle multiple traits if needed
	}

	r := CheckServerUp(ua.SAP_URL, 2*time.Second)
	if r.Up {
		fmt.Printf("SAP server is up (status=%d, in %s)\n", r.StatusCode, r.Duration)
	} else {
		fmt.Printf("SAP server is down (%v, in %s)\n", r.Err, r.Duration)
	}

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	for _, signal := range ua.Traits.Signals {
		// determine the protocols that the system supports
		cSignal := components.Cervice{
			Definition: signal.Name,
			Details:    signal.Details,
			Protos:     sProtocols,
			Nodes:      make(map[string][]string, 0),
		}
		ua.CervicesMap[cSignal.Definition] = &cSignal

		// start the data collection goroutine for the measurement
		go ua.sigMon(signal.Name, signal.Period*time.Second)

	}

	// Return the unit asset and a cleanup function to close the InfluxDB client
	return ua, func() {

		log.Println("Disconnecting from systems...")
		// ua.client.Close()
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

//-------------------------------------Unit asset's functionalities

// sigMon periodically monitors the measurement and if it exceeds the threshold, it reports to the SAP system
func (ua *UnitAsset) sigMon(name string, period time.Duration) error {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ua.Owner.Ctx.Done():
			log.Printf("Stopping data collection for measurement: %s", name)
			return ua.Owner.Ctx.Err()

		case <-ticker.C:
			tf, err := usecases.GetState(ua.CervicesMap[name], ua.Owner)
			if err != nil {
				log.Printf("\nUnable to obtain a %s reading with error %s\n", name, err)
				continue // return fmt.Errorf("unsupported measurement: %s", name)
			}
			fmt.Printf("%+v\n", tf)
			// Perform a type assertion to convert the returned Form to SignalA_v1a
			tup, ok := tf.(*forms.SignalA_v1a)
			if !ok {
				log.Println("Problem unpacking the signal form")
				continue // return fmt.Errorf("problem unpacking measurement: %s", name)
			}

			point := fmt.Sprintf("Measurement: %s, Value: %.2f, Time: %s", name, tup.Value, time.Now().Format(time.RFC3339))
			log.Printf("%s", point)

			// Check if the value exceeds the threshold
			if tup.Value > ua.Traits.Signals[0].Threshold {
				log.Printf("ALERT: %s value %.2f exceeds threshold %.2f\n", name, tup.Value, ua.Traits.Signals[0].Threshold)
				ua.Traits.Signals[0].TOverCount++
				if ua.Traits.Signals[0].TOverCount >= ua.Traits.Signals[0].TOverCount {
					ua.Traits.Signals[0].Operational = false
					ua.Traits.Signals[0].WorkRequested = true
					log.Printf("Action required: %s is not operational. Work requested.\n", name)
				}
				// Here you would add the code to report the anomaly to the SAP system using ua.SAP_URL
			}
		}
	}
}

// state queries the bucket for the list of measurements
func (ua *UnitAsset) state(w http.ResponseWriter) {
	text := "The list of measurements that are monitored by " + ua.Name + "\n"
	for _, signal := range ua.Traits.Signals {
		text += fmt.Sprintf("Signal: %s, Threshold: %f, TOverCount: %d, Operational: %t, WorkRequested: %t\n",
			signal.Name, signal.Threshold, signal.TOverCount, signal.Operational, signal.WorkRequested)
	}
	w.Write([]byte(text))
}

// //-------------------------------------SAP server interaction functions
type CheckResult struct {
	Up         bool
	StatusCode int
	Err        error
	Duration   time.Duration
}

func CheckServerUp(rawURL string, timeout time.Duration) CheckResult {
	start := time.Now()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return CheckResult{Up: false, Err: fmt.Errorf("invalid url: %w", err), Duration: time.Since(start)}
	}

	// Transport with sane timeouts. (Default transport has some timeouts but not all.)
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,

		// If you are checking HTTPS with self-signed certs, you might be tempted to set
		// InsecureSkipVerify=true. Avoid that in production; prefer a proper CA/cert setup.
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	client := &http.Client{
		Timeout:   timeout, // total time limit including redirects
		Transport: tr,
	}

	// Use GET for robustness; discard body quickly.
	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return CheckResult{Up: false, Err: fmt.Errorf("build request: %w", err), Duration: time.Since(start)}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{Up: false, Err: err, Duration: time.Since(start)}
	}
	defer resp.Body.Close()

	// "Up" here means we got *an* HTTP response.
	return CheckResult{Up: true, StatusCode: resp.StatusCode, Duration: time.Since(start)}
}
