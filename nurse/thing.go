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
	SAP_URL  string              `json:"sap_url"`
	Signals  []SignalT           `json:"signals"`
	owner    *components.System  `json:"-"`
	cervices components.Cervices `json:"-"`
	name     string              `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	Operational := components.Service{
		Definition:  "SignalMonitoring",
		SubPath:     "monitor",
		Details:     map[string][]string{},
		RegPeriod:   22,
		Description: "monitors the value of the consumed service signal (GET)",
	}

	return &components.UnitAsset{
		Name:    "HealthTracker",
		Mission: "monitor_signal",
		Details: map[string][]string{},
		ServicesMap: components.Services{
			Operational.SubPath: &Operational,
		},
		Traits: &Traits{
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
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates a new UnitAsset resource based on the configuration
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
		name:  configuredAsset.Name,
	}

	for _, raw := range configuredAsset.Traits {
		if err := json.Unmarshal(raw, t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
		break
	}

	r := CheckServerUp(t.SAP_URL, 2*time.Second)
	if r.Up {
		fmt.Printf("SAP server is up (status=%d, in %s)\n", r.StatusCode, r.Duration)
	} else {
		fmt.Printf("SAP server is down (%v, in %s)\n", r.Err, r.Duration)
	}

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	cervMap := make(components.Cervices)
	for _, signal := range t.Signals {
		cSignal := &components.Cervice{
			Definition: signal.Name,
			Details:    signal.Details,
			Protos:     sProtocols,
			Nodes:      make(map[string][]string),
		}
		cervMap[cSignal.Definition] = cSignal

		go t.sigMon(signal.Name, signal.Period*time.Second)
	}
	t.cervices = cervMap

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: cervMap,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		log.Println("Disconnecting from systems...")
	}
}

//-------------------------------------Unit asset's functionalities

// sigMon periodically monitors the measurement and if it exceeds the threshold, it reports to the SAP system
func (t *Traits) sigMon(name string, period time.Duration) error {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-t.owner.Ctx.Done():
			log.Printf("Stopping data collection for measurement: %s", name)
			return t.owner.Ctx.Err()

		case <-ticker.C:
			tf, err := usecases.GetState(t.cervices[name], t.owner)
			if err != nil {
				log.Printf("\nUnable to obtain a %s reading with error %s\n", name, err)
				continue
			}
			fmt.Printf("%+v\n", tf)
			tup, ok := tf.(*forms.SignalA_v1a)
			if !ok {
				log.Println("Problem unpacking the signal form")
				continue
			}

			point := fmt.Sprintf("Measurement: %s, Value: %.2f, Time: %s", name, tup.Value, time.Now().Format(time.RFC3339))
			log.Printf("%s", point)

			// Check if the value exceeds the threshold
			if tup.Value > t.Signals[0].Threshold {
				log.Printf("ALERT: %s value %.2f exceeds threshold %.2f\n", name, tup.Value, t.Signals[0].Threshold)
				t.Signals[0].TOverCount++
				if t.Signals[0].TOverCount >= t.Signals[0].TOverCount {
					t.Signals[0].Operational = false
					t.Signals[0].WorkRequested = true
					log.Printf("Action required: %s is not operational. Work requested.\n", name)
				}
				// Here you would add the code to report the anomaly to the SAP system using t.SAP_URL
			}
		}
	}
}

// state queries the bucket for the list of measurements
func (t *Traits) state(w http.ResponseWriter) {
	text := "The list of measurements that are monitored by " + t.name + "\n"
	for _, signal := range t.Signals {
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
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: tr,
	}

	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return CheckResult{Up: false, Err: fmt.Errorf("build request: %w", err), Duration: time.Since(start)}
	}

	resp, err := client.Do(req)
	if err != nil {
		return CheckResult{Up: false, Err: err, Duration: time.Since(start)}
	}
	defer resp.Body.Close()

	return CheckResult{Up: true, StatusCode: resp.StatusCode, Duration: time.Since(start)}
}
