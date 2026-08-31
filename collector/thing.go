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
	// Period is the sampling period in seconds.
	//
	// An int rather than a time.Duration, because a Duration holding the number
	// 10 means ten nanoseconds and only becomes ten seconds when multiplied by
	// time.Second — which works, and reads as if the field were already a
	// duration. Anyone writing the obvious `Period: time.Second` for one second
	// would get 10^9 seconds, about 31 years, and the compiler would not object.
	// The unit belongs in the name and the conversion belongs at the point of
	// use.
	Period int `json:"samplingPeriod"`
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
		Mission: components.MissionLogging,
		Details: map[string][]string{"Database": {"InfluxDB"}},
		ServicesMap: components.Services{
			mqueryService.SubPath: &mqueryService,
		},
		Traits: &Traits{
			FluxURL: "http://localhost:8086",
			// Deliberately empty. This field held a working token from a past
			// deployment — checked into a public repository and copied into
			// every generated systemconfig.json, so every deployment shipped
			// with somebody else's credential and no sign that it was one.
			//
			// Empty is caught below with a message that says what to do, which
			// is what a template value should do when it cannot be guessed.
			Token:  "",
			Org:    "mbaigo",
			Bucket: "demo",
			Measurements: []MeasurementT{
				{
					Name:    "temperature",
					Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
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
		log.Fatalf("%s: the InfluxDB settings in systemconfig.json are incomplete "+
			"(db_url %q, token %s, organization %q, bucket %q). A freshly installed "+
			"InfluxDB 2 has no organization, bucket or token until `influx setup` has "+
			"been run — see this system's README, section 3.\n",
			configuredAsset.Name, t.FluxURL, present(t.Token), t.Org, t.Bucket)
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
			Nodes:      make(map[string][]components.NodeInfo),
			Mode:       "get",
		}
		cervMap[cMeasurement.Definition] = cMeasurement
	}
	t.cervices = cervMap

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Mobility:    configuredAsset.Mobility,
		TetheredTo:  configuredAsset.TetheredTo,
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
		}(measurement.Name, time.Duration(measurement.Period)*time.Second)
	}

	return ua, func() {
		log.Println("Waiting for all goroutines to finish...")
		wg.Wait()
		log.Println("Disconnecting from InfluxDB")
		t.client.Close()
	}
}

//-------------------------------------Service handlers

// measQuery handles GET requests for the list of measurements in the bucket.
func (t *Traits) measQuery(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		t.q4measurements(w)
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//-------------------------------------Unit asset's functionalities

// collectIngest discovers all providers of a measurement type and ingests a reading
// from each one into InfluxDB on every tick. Each point is tagged with the source
// node name so readings from different assets remain distinguishable in the bucket.
func (t *Traits) collectIngest(name string, period time.Duration, writeAPI api.WriteAPI) error {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	// One cervice per provider, kept for as long as that provider is in the
	// node list.
	//
	// These used to be built fresh on every tick — the Nodes map pre-populated
	// so discovery was skipped, which was the intent and which worked. What it
	// did not survive is following: GetState follows a service that offers it,
	// and the follow-state lives on the cervice, so a new cervice each tick
	// opened a new subscription every three seconds and never closed one. On
	// AlphaCloud that reached the provider's limit in about a minute and a half,
	// after which ds18b20 logged "refusing a subscription; 32 are already open"
	// several times a second.
	//
	// Local to this goroutine, which owns one measurement, so there is no shared
	// state to guard. Rebuilt when discovery runs, because that is exactly when
	// the set of providers may have changed.
	perNode := map[string]*components.Cervice{}

	for {
		select {
		case <-t.owner.Ctx.Done():
			log.Printf("Stopping data collection for measurement: %s", name)
			return t.owner.Ctx.Err()

		case <-ticker.C:
			cer := t.cervices[name]

			// Discover all providers of this measurement type on the first tick
			// or after a previous failure cleared the node list.
			if len(cer.Nodes) == 0 {
				if err := usecases.Search4MultipleServices(cer, t.owner); err != nil {
					log.Printf("discovery failed for %s: %v\n", name, err)
					continue
				}
				log.Printf("discovered %d node(s) for %s\n", len(cer.Nodes), name)
				// The providers may be different ones. Dropping the old cervices
				// ends their subscriptions with them.
				perNode = map[string]*components.Cervice{}
			}

			// Query each provider individually so we can tag the point with its node name
			// and the provider's registered details (unit, location, etc.).
			for node, nodeInfos := range cer.Nodes {
				for _, ni := range nodeInfos {
					// One cervice per provider, made once and kept. Its
					// pre-populated Nodes still skip re-discovery, and because
					// it survives the tick it also holds one subscription rather
					// than opening another.
					single, held := perNode[node]
					if !held {
						single = &components.Cervice{
							Definition: cer.Definition,
							Details:    ni.Details,
							Protos:     cer.Protos,
							Nodes:      map[string][]components.NodeInfo{node: {ni}},
						}
						perNode[node] = single
					}
					tf, err := usecases.GetState(single, t.owner)
					if err != nil {
						log.Printf("unable to read %s from %s: %v — re-discovering next tick\n", name, node, err)
						cer.Nodes = make(map[string][]components.NodeInfo) // reset so next tick re-discovers
						perNode = map[string]*components.Cervice{}
						break
					}
					value, ok := signalValue(tf)
					if !ok {
						log.Printf("unexpected form from %s for %s\n", node, name)
						continue
					}

					// Tag with the node name plus all details the provider registered
					// (e.g. Unit, Location) so streams are distinguishable in InfluxDB.
					tags := map[string]string{"source": node}
					for key, values := range ni.Details {
						tags[key] = strings.Join(values, ",")
					}

					point := write.NewPoint(
						name,
						tags,
						map[string]interface{}{"value": value},
						time.Now(),
					)
					writeAPI.WritePoint(point)
					log.Printf("collected %s from %-20s  value=%v\n", name, node, value)
				}
			}
		}
	}
}

// signalValue reduces a reading to the value this system records, and says
// whether it was a reading at all.
//
// A measurement is a number and a switch is not — but the *history* of a switch
// is. A plug reports SignalB_v1a, and this system used to accept only
// SignalA_v1a, so every configured OnOff discovered its providers, read them,
// and recorded nothing but a log line per device per tick.
//
// A switch is stored as InfluxDB's own boolean type rather than as 0 and 1, so
// what the bucket holds is what the provider said. The cost is that a boolean
// field cannot be averaged directly, and the average is the interesting figure:
// the mean of a heater's state over a window is its duty cycle, which is how
// long it actually ran. It is still one line of Flux away —
//
//	|> map(fn: (r) => ({r with _value: if r._value then 1.0 else 0.0}))
//	|> aggregateWindow(every: 1h, fn: mean)
//
// — so nothing is lost, and the stored form stays honest about what a switch is.
//
// The return is `any` because these are two different types and the point
// carries whichever it was given. That is also why this is worth keeping in one
// place: a caller that assumed a float would silently record `0` for every
// switch that was off and every switch that was on.
func signalValue(f forms.Form) (any, bool) {
	switch sig := f.(type) {
	case *forms.SignalA_v1a:
		return sig.Value, true
	case *forms.SignalB_v1a:
		return sig.Value, true
	default:
		return nil, false
	}
}

// q4measurements queries the bucket for the list of measurements
// q4measurements lists what the bucket holds.
//
// The query is Flux, which matters more than it looks: Flux is supported by
// InfluxDB 1.8 and 2.x and by no version of InfluxDB 3. Writes are unaffected —
// the client uses the v2 line-protocol endpoint, which 3.x keeps for
// compatibility — so a collector pointed at a v3 server ingests happily and
// fails only here. See this system's README.
func (t *Traits) q4measurements(w http.ResponseWriter) {
	text := "The list of measurements in the " + t.name + " bucket is:\n"
	queryAPI := t.client.QueryAPI(t.Org)

	query := fmt.Sprintf(`
		 import "influxdata/influxdb/schema"
		 schema.measurements(bucket: "%s")
	 `, t.name)

	// A failed query answers the caller. It used to call log.Fatal, which ends
	// the process — so one unreachable database, one restarted server, one
	// network blip took down a system that was otherwise ingesting correctly,
	// and took the ingestion with it. An HTTP handler has a way to say that
	// something went wrong, and exiting is not it.
	results, err := queryAPI.Query(context.Background(), query)
	if err != nil {
		log.Printf("%s: querying the measurements: %v\n", t.name, err)
		http.Error(w, "cannot read the measurements from InfluxDB: "+err.Error(),
			http.StatusServiceUnavailable)
		return
	}

	for results.Next() {
		measurement := fmt.Sprintf("%v", results.Record().Value())
		text += "- " + measurement + "\n"
	}

	if err := results.Err(); err != nil {
		log.Printf("%s: reading the measurements: %v\n", t.name, err)
		http.Error(w, "cannot read the measurements from InfluxDB: "+err.Error(),
			http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte(text)); err != nil {
		log.Printf("%s: writing the measurements: %v\n", t.name, err)
	}
}

// present says whether a secret is set without putting it in a log line.
func present(secret string) string {
	if secret == "" {
		return "not set"
	}
	return "set"
}
