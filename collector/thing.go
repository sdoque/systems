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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define a measurement (or signal)
type MeasurementT struct {
	Name    string              `json:"serviceDefinition"`
	Details map[string][]string `json:"mdetails"`
	Period  time.Duration       `json:"samplingPeriod"`
}

//-------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	FluxURL      string         `json:"db_url"`
	Token        string         `json:"token"`
	Org          string         `json:"organization"`
	Bucket       string         `json:"bucket"`
	Measurements []MeasurementT `json:"measurements"`
	client       influxdb2.Client
	owner        *components.System  `json:"-"`
	cervices     components.Cervices `json:"-"`
	name         string              `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	mqueryService := components.Service{
		Definition:  "mquery",
		SubPath:     "mquery",
		Details:     map[string][]string{},
		RegPeriod:   60,
		CUnit:       "",
		Description: "provides the list of measurements in the bucket (GET)",
	}

	return &components.UnitAsset{
		Name:    "demo",
		Mission: "handle_timeseries",
		Details: map[string][]string{"Database": {"InfluxDB"}},
		ServicesMap: components.Services{
			mqueryService.SubPath: &mqueryService,
		},
		Traits: &Traits{
			FluxURL: "http://10.0.0.33:8086",
			Token:   "K1NTWNlToyUNXdii7IwNJ1W-kMsagUr8w1r4cRVYqK-N-R9vVT1MCJwHFBxOgiW85iKiMSsUpbrxQsQZJA8IzA==",
			Org:     "mbaigo",
			Bucket:  "demo",
			Measurements: []MeasurementT{
				{
					Name:    "temperature",
					Details: map[string][]string{"Location": {"Kitchen"}},
					Period:  3,
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

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	if t.FluxURL == "" || t.Token == "" || t.Org == "" || t.Bucket == "" {
		log.Fatal("Invalid InfluxDB configuration: missing required parameters")
	}

	// Create a new client for InfluxDB
	t.client = influxdb2.NewClient(t.FluxURL, t.Token)

	// Create a non-blocking write API
	writeAPI := t.client.WriteAPI(t.Org, t.Bucket)

	// Build cervices map from measurements
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	cervMap := make(components.Cervices)
	for _, measurement := range t.Measurements {
		cMeasurement := &components.Cervice{
			Definition: measurement.Name,
			Details:    measurement.Details,
			Protos:     sProtocols,
			Nodes:      make(map[string][]string),
		}
		cervMap[cMeasurement.Definition] = cMeasurement
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

	// Collect and ingest measurements
	var wg sync.WaitGroup
	for _, measurement := range t.Measurements {
		wg.Add(1)
		go func(name string, period time.Duration) {
			defer wg.Done()
			if err := t.collectIngest(name, period, writeAPI); err != nil {
				log.Printf("Error in collectIngest for measurement: %v", err)
			}
		}(measurement.Name, measurement.Period*time.Second)
	}

	return ua, func() {
		log.Println("Waiting for all goroutines to finish...")
		wg.Wait()
		log.Println("Disconnecting from InfluxDB")
		t.client.Close()
	}
}

//-------------------------------------Unit asset's functionalities

// collectIngest collects measurements and ingests them into InfluxDB
func (t *Traits) collectIngest(name string, period time.Duration, writeAPI api.WriteAPI) error {
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

			metaD := t.cervices[name].Details

			// Convert metaD (map[string][]string) into InfluxDB tags (map[string]string)
			tags := make(map[string]string)
			for key, values := range metaD {
				tags[key] = strings.Join(values, ",")
			}

			point := write.NewPoint(
				name,
				tags,
				map[string]interface{}{"value": tup.Value},
				time.Now(),
			)

			writeAPI.WritePoint(point)
		}
	}
}

// q4measurements queries the bucket for the list of measurements
func (t *Traits) q4measurements(w http.ResponseWriter) {
	text := "The list of measurements in the " + t.name + " bucket is:\n"
	queryAPI := t.client.QueryAPI(t.Org)

	query := fmt.Sprintf(`
		 import "influxdata/influxdb/schema"
		 schema.measurements(bucket: "%s")
	 `, t.name)

	results, err := queryAPI.Query(context.Background(), query)
	if err != nil {
		log.Fatal(err)
	}

	for results.Next() {
		measurement := fmt.Sprintf("%v", results.Record().Value())
		text += "- " + measurement + "\n"
	}

	if err := results.Err(); err != nil {
		log.Fatal(err)
	}

	w.Write([]byte(text))
}
