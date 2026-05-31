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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define a measurement (or signal)
type SignalT struct {
	Name              string              `json:"serviceDefinition"`
	Details           map[string][]string `json:"details"`
	Period            time.Duration       `json:"samplingPeriod"`
	LowerThreshold    float64             `json:"lowerThreshold"`
	UpperThreshold    float64             `json:"upperThreshold"`
	TOverCount        map[string]int      `json:"-"` // consecutive out-of-range count per source node
	WorkRequested     map[string]bool     `json:"-"` // pending maintenance order per source node
	Operational       bool                `json:"-"` // false when any node has a pending order
	ValveTagByNode    map[string]string   `json:"-"` // node → actuator FL tag resolved from GraphDB
	UnresolvableNodes map[string]bool     `json:"-"` // nodes whose actuator could not be resolved; skipped
}

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the nurse unit asset.
type Traits struct {
	GraphDB_URL   string              `json:"graphdb_url"`
	Signals       []SignalT           `json:"signals"`
	pendingOrders map[string]string   // orderID → signalName; not serialized
	sapper        *components.Cervice // discovers the Sapper's MaintenanceOrder service
	owner         *components.System
	ua            *components.UnitAsset
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	monitorService := components.Service{
		Definition:  "SignalMonitoring",
		SubPath:     "monitor",
		Details:     map[string][]string{},
		RegPeriod:   22,
		Description: "monitors the value of the consumed service signal (GET)",
	}
	enrichmentService := components.Service{
		Definition:  "EnrichmentNotification",
		SubPath:     "enrichment",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   22,
		Description: "receives planner enrichment + release notifications (POST)",
	}

	return &components.UnitAsset{
		Name:    "HealthTracker",
		Details: map[string][]string{},
		ServicesMap: components.Services{
			monitorService.SubPath:    &monitorService,
			enrichmentService.SubPath: &enrichmentService,
		},
		Traits: &Traits{
			GraphDB_URL: "http://13.79.36.131:7200/repositories/arrowhead-skoghall-v2",
			Signals: []SignalT{
				{
					Name:           "pressure",
					Details:        map[string][]string{"Unit": {"kPa"}},
					Period:         4,
					LowerThreshold: 10.0,
					UpperThreshold: 25.0,
					Operational:    true,
				},
			},
		},
	}
}

//-------------------------------------Instantiate unit assets based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner:         sys,
		pendingOrders: make(map[string]string),
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	// Default all signals to operational at startup and initialise per-source counters.
	for i := range t.Signals {
		t.Signals[i].Operational = true
		t.Signals[i].TOverCount = make(map[string]int)
		t.Signals[i].WorkRequested = make(map[string]bool)
		t.Signals[i].ValveTagByNode = make(map[string]string)
		t.Signals[i].UnresolvableNodes = make(map[string]bool)
	}

	// The Sapper is discovered via Arrowhead at order-creation time — no
	// startup healthcheck. If the Sapper isn't yet registered when an order
	// needs to be raised, requestMaintenanceOrder logs the failure and the
	// signal stays in WorkRequested until the next process restart.

	// GraphDB is load-bearing: without it the nurse cannot resolve a sensor
	// to its associated asset and any maintenance order it raised would be
	// misdirected. Refuse to start rather than emit confused work orders.
	if t.GraphDB_URL == "" {
		log.Fatalf("nurse: graphdb_url is required in configuration")
	}
	if err := CheckGraphDBUp(t.GraphDB_URL, 5*time.Second); err != nil {
		log.Fatalf("nurse: GraphDB unreachable at %s: %v", t.GraphDB_URL, err)
	}
	log.Printf("GraphDB reachable at %s", t.GraphDB_URL)

	// GraphDB is load-bearing: without it the nurse cannot resolve a sensor
	// to its associated asset and any maintenance order it raised would be
	// misdirected. Refuse to start rather than emit confused work orders.
	if t.GraphDB_URL == "" {
		log.Fatalf("nurse: graphdb_url is required in configuration")
	}
	if err := CheckGraphDBUp(t.GraphDB_URL, 5*time.Second); err != nil {
		log.Fatalf("nurse: GraphDB unreachable at %s: %v", t.GraphDB_URL, err)
	}
	log.Printf("GraphDB reachable at %s", t.GraphDB_URL)

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	cervices := make(components.Cervices)
	for _, signal := range t.Signals {
		cSignal := components.Cervice{
			Definition: signal.Name,
			Details:    signal.Details,
			Protos:     sProtocols,
			Nodes:      make(map[string][]components.NodeInfo),
			Mode:       "get",
		}
		cervices[cSignal.Definition] = &cSignal
	}
	// Cervice used to discover the Sapper's MaintenanceOrder service at
	// runtime, replacing the previous hardcoded sap_url.
	t.sapper = &components.Cervice{
		Definition: "MaintenanceOrder",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "set",
	}
	cervices[t.sapper.Definition] = t.sapper

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: cervices,
		Traits:      t,
	}
	t.ua = ua
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// Start a monitoring goroutine for each signal.
	for _, signal := range t.Signals {
		go t.sigMon(signal.Name, signal.Period*time.Second)
	}

	return ua, func() {
		log.Println("Disconnecting from systems...")
	}
}

// findSignal returns a pointer to the named signal, or nil if not found.
func (t *Traits) findSignal(name string) *SignalT {
	for i := range t.Signals {
		if t.Signals[i].Name == name {
			return &t.Signals[i]
		}
	}
	return nil
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

//-------------------------------------Unit asset's function methods

// sigMon periodically monitors all providers of a signal and requests maintenance
// when any single source stays outside the [lower, upper] range 5 consecutive times.
func (t *Traits) sigMon(name string, period time.Duration) error {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-t.owner.Ctx.Done():
			log.Printf("Stopping monitoring for signal: %s", name)
			return t.owner.Ctx.Err()

		case <-ticker.C:
			sig := t.findSignal(name)
			if sig == nil {
				log.Printf("Signal %s not found in configuration\n", name)
				continue
			}

			cer := t.ua.CervicesMap[name]

			// Discover all providers on the first tick or after a failure cleared the list.
			if len(cer.Nodes) == 0 {
				if err := usecases.Search4MultipleServices(cer, t.owner); err != nil {
					log.Printf("Unable to discover providers for %s: %s\n", name, err)
					continue
				}
				log.Printf("Discovered %d provider(s) for %s\n", len(cer.Nodes), name)
			}

			// Resolve the actuator (e.g. valve) each provider's sensor diagnoses.
			// Done here, before polling, so a missing diagnosesActuator relationship
			// surfaces during normal operation rather than under an anomaly.
			t.resolveActuators(sig, cer.Nodes)

			// Query each provider individually to preserve its identity for per-source counting.
			failed := false
			for node, nodeInfos := range cer.Nodes {
				if failed {
					break
				}
				// Skip nodes whose actuator could not be resolved — sending an order
				// for a misidentified asset is worse than sending no order at all.
				if sig.UnresolvableNodes[node] {
					continue
				}
				for _, ni := range nodeInfos {
					// Skip this node while its maintenance order is pending.
					if sig.WorkRequested[node] {
						continue
					}
					tmp := &components.Cervice{
						Definition: cer.Definition,
						Details:    ni.Details,
						Protos:     cer.Protos,
						Nodes:      map[string][]components.NodeInfo{node: {ni}},
					}
					tf, err := usecases.GetState(tmp, t.owner)
					if err != nil {
						log.Printf("Unable to obtain a %s reading from %s: %s\n", name, node, err)
						cer.Nodes = make(map[string][]components.NodeInfo)
						failed = true
						break
					}
					tup, ok := tf.(*forms.SignalA_v1a)
					if !ok {
						log.Printf("Unexpected form from %s for %s\n", node, name)
						continue
					}

					log.Printf("Measurement: %s from %s, Value: %.2f, Time: %s\n",
						name, node, tup.Value, time.Now().Format(time.RFC3339))

					if tup.Value < sig.LowerThreshold || tup.Value > sig.UpperThreshold {
						sig.TOverCount[node]++
						log.Printf("ALERT: %s/%s value %.2f outside range [%.2f, %.2f] (count: %d/5)\n",
							name, node, tup.Value, sig.LowerThreshold, sig.UpperThreshold, sig.TOverCount[node])
						if sig.TOverCount[node] >= 5 {
							sig.Operational = false
							sig.WorkRequested[node] = true
							log.Printf("Signal %s/%s non-operational, requesting maintenance\n", name, node)
							equipmentID := assetNameFromURL(ni.URL)
							// The actuator FL tag resolved from GraphDB is the SAP
							// dispatch target — the sensor only diagnoses the fault.
							location := sig.ValveTagByNode[node]
							orderID := t.requestMaintenanceOrder(sig, equipmentID, location)
							if orderID != "" {
								t.pendingOrders[orderID] = name
								log.Printf("Maintenance order %s created for signal %s/%s\n", orderID, name, node)
								reason := fmt.Sprintf("Signal %s out of range [%.2f, %.2f]",
									sig.Name, sig.LowerThreshold, sig.UpperThreshold)
								go t.pushOrderContext(orderID, equipmentID, location, reason)
							} else {
								log.Printf("SAP order failed for signal %s/%s; monitoring paused until system restart\n", name, node)
							}
						}
					} else if sig.TOverCount[node] > 0 {
						log.Printf("Signal %s/%s back in range (resetting count from %d)\n",
							name, node, sig.TOverCount[node])
						sig.TOverCount[node] = 0
					}
				}
			}
		}
	}
}

// state reports the current status of all monitored signals.
func (t *Traits) state(w http.ResponseWriter) {
	text := "The list of measurements that are monitored by " + t.ua.Name + "\n"
	for _, signal := range t.Signals {
		counts := ""
		for node, n := range signal.TOverCount {
			counts += fmt.Sprintf(" %s:%d", node, n)
		}
		if counts == "" {
			counts = " none"
		}
		pending := ""
		for node := range signal.WorkRequested {
			pending += " " + node
		}
		if pending == "" {
			pending = " none"
		}
		text += fmt.Sprintf("Signal: %s, Range: [%.2f, %.2f], TOverCount:[%s], Operational: %t, WorkRequested:[%s]\n",
			signal.Name, signal.LowerThreshold, signal.UpperThreshold, counts, signal.Operational, pending)
	}
	w.Write([]byte(text))
}

// enrichment receives the planner's release-and-enrichment notification from
// the Sapper. For now the Nurse simply logs the payload to the terminal so
// the demo audience can see the planner's decision arriving downstream of the
// originating signal. Future work could attach it to the signal's state for
// the /monitor GET view.
func (t *Traits) enrichment(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		log.Printf("← enrichment from %s\n%s\n", r.RemoteAddr, pretty.String())
	} else {
		log.Printf("← enrichment from %s\n%s\n", r.RemoteAddr, string(body))
	}

	w.WriteHeader(http.StatusNoContent)
}

// update handles an incoming completion notification and restores the signal to operational.
// It accepts both the nurse's MaintenanceDoneEvent format (orderId) and the SAP adaptor's
// notifyDigitalTwin format (maintenanceOrder), so either party can call this endpoint.
func (t *Traits) update(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var prettyBody bytes.Buffer
	if err := json.Indent(&prettyBody, body, "", "  "); err == nil {
		log.Printf("← monitor POST from %s\n%s\n", r.RemoteAddr, prettyBody.String())
	} else {
		log.Printf("← monitor POST from %s\n%s\n", r.RemoteAddr, string(body))
	}

	var event MaintenanceDoneEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "failed to unmarshal request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// The SAP adaptor's notifyDigitalTwin sends "maintenanceOrder" instead of "orderId".
	// Fall back to that field if orderId was not set.
	if event.OrderID == "" {
		var raw map[string]json.RawMessage
		if json.Unmarshal(body, &raw) == nil {
			if v, ok := raw["maintenanceOrder"]; ok {
				json.Unmarshal(v, &event.OrderID)
			}
			if event.Status == "" {
				if v, ok := raw["status"]; ok {
					json.Unmarshal(v, &event.Status)
				}
			}
		}
	}

	// Find the signal that raised this order and restore it to operational.
	if signalName, ok := t.pendingOrders[event.OrderID]; ok {
		if sig := t.findSignal(signalName); sig != nil {
			sig.Operational = true
			sig.WorkRequested = make(map[string]bool)
			sig.TOverCount = make(map[string]int)
			delete(t.pendingOrders, event.OrderID)
			log.Printf("Signal %s restored to operational (order %s: %s)\n",
				signalName, event.OrderID, event.Status)
		}
	} else {
		log.Printf("Received completion for unknown order %s\n", event.OrderID)
	}

	ttl, err := ConvertMaintenanceDoneEventToTurtle(event, "https://sinetiq.se/sap/")
	if err != nil {
		http.Error(w, "failed to convert to turtle: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/turtle")
	w.Write([]byte(ttl))
}


// pushOrderContext POSTs a SPARQL INSERT linking the Sapper-issued Order IRI
// to the sensor-side facts only the Nurse knows: which sensor raised the
// order, which actuator FL tag it diagnoses, and the threshold-breach reason.
// The Order IRI shape matches the Sapper's, so the three sets of triples —
// Sapper CRTD, Nurse context, Sapper TECO — merge on the same subject in the
// graph. Graph publication is a side effect; failures are logged but do not
// block the maintenance loop.
func (t *Traits) pushOrderContext(orderID, sensorName, valveTag, reason string) {
	if t.GraphDB_URL == "" {
		return
	}
	orderURI := "https://sinetiq.se/sap/MaintenanceOrder/" + orderID
	sparql := fmt.Sprintf(`PREFIX ex: <https://sinetiq.se/sap/>
INSERT DATA {
    GRAPH <https://arrowheadweb.org/graph/sap/workorders> {
        <%s> ex:bySensor %q ;
            ex:targetFLTag %q ;
            ex:reason %q .
    }
}`, orderURI, sensorName, valveTag, reason)

	log.Printf("→ GraphDB INSERT (context) order=%s\n%s\n", orderID, sparql)

	endpoint := strings.TrimRight(t.GraphDB_URL, "/") + "/statements"
	resp, err := http.Post(endpoint, "application/sparql-update", strings.NewReader(sparql))
	if err != nil {
		log.Printf("pushOrderContext: POST failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	log.Printf("← GraphDB %s  body=%s\n", resp.Status, string(msg))
}

// resolveActuators looks up the actuator (e.g. valve) functional-location tag
// for every newly-discovered node in nodes. The result is cached on the signal
// so each sensor pays the lookup cost at most once. A sensor with no
// diagnosesActuator relationship, with multiple candidates, or whose lookup
// fails for any reason is marked unresolvable and skipped in future polls —
// emitting a maintenance order against a misidentified asset is a worse
// failure mode than emitting nothing.
//
// The expected graph shape is:
//
//	<sensor-iri>  afo:hasName            "<sensor-tag>" .
//	<sensor-iri>  afo:diagnosesActuator  <valve-fl-iri> .
//	<valve-fl-iri> arrowhead:functionalLocation "<valve-tag>" .
//
// The third triple is already in the Skoghall store for modelled equipment;
// the first two must be added by INSERT when the sensor's knowledge graph
// is published to GraphDB.
func (t *Traits) resolveActuators(sig *SignalT, nodes map[string][]components.NodeInfo) {
	for node, nodeInfos := range nodes {
		if _, ok := sig.ValveTagByNode[node]; ok {
			continue
		}
		if sig.UnresolvableNodes[node] {
			continue
		}
		if len(nodeInfos) == 0 {
			continue
		}
		sensorName := assetNameFromURL(nodeInfos[0].URL)
		if sensorName == "" {
			log.Printf("nurse: cannot extract sensor name from %s; marking node %s unresolvable",
				nodeInfos[0].URL, node)
			sig.UnresolvableNodes[node] = true
			continue
		}
		tag, err := resolveActuatorTag(t.GraphDB_URL, sensorName, 5*time.Second)
		if err != nil {
			log.Printf("nurse: cannot resolve actuator for sensor %s on node %s: %v",
				sensorName, node, err)
			sig.UnresolvableNodes[node] = true
			continue
		}
		log.Printf("nurse: sensor %s on node %s diagnoses actuator at %s",
			sensorName, node, tag)
		sig.ValveTagByNode[node] = tag
	}
}

// resolveActuatorTag asks GraphDB for the FL tag of the actuator a sensor
// diagnoses. Returns the tag on success, or an error if the sensor has no
// diagnosesActuator triple, has more than one match (ambiguous), or the
// endpoint is unreachable.
func resolveActuatorTag(endpoint, sensorName string, timeout time.Duration) (string, error) {
	// afo: is the producer's namespace — the same URI the kgrapher emits and
	// the same URI under which the sensor's triples live in the remote graph.
	// arrowhead: is the upstream (Skoghall) namespace under which the valve's
	// functional-location object is published. The mixed prefixes are
	// intentional: each side keeps its own vocabulary; alignment ontologies
	// bridge them at reasoning time.
	query := fmt.Sprintf(`PREFIX afo: <http://www.synecdoque.com/2025/afo#>
PREFIX arrowhead: <https://arrowheadweb.org/ont/arrowhead#>
SELECT ?valveTag WHERE {
  ?sensor afo:hasName %q .
  ?sensor afo:diagnosesActuator ?valveFL .
  ?valveFL arrowhead:functionalLocation ?valveTag .
}`, sensorName)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sparql-query")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var result struct {
		Results struct {
			Bindings []map[string]struct {
				Value string `json:"value"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	bs := result.Results.Bindings
	switch len(bs) {
	case 0:
		return "", fmt.Errorf("no diagnosesActuator relationship for sensor %q", sensorName)
	case 1:
		tag := bs[0]["valveTag"].Value
		if tag == "" {
			return "", fmt.Errorf("empty valveTag binding for sensor %q", sensorName)
		}
		return tag, nil
	default:
		return "", fmt.Errorf("ambiguous diagnosesActuator for sensor %q (%d matches)",
			sensorName, len(bs))
	}
}

// CheckGraphDBUp probes the GraphDB SPARQL endpoint with a trivial query.
// It verifies both reachability and that the repository accepts SPARQL —
// a simple TCP/HTTP check would not catch a wrong repository name. Returns
// nil on success, an error explaining the failure otherwise.
func CheckGraphDBUp(endpoint string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// ASK with a trivial triple pattern is the most universally-accepted probe:
	// no projection expressions (some GraphDB versions misparse `(N AS ?v)`),
	// no aggregation, just a yes/no.
	const probe = `ASK { ?s ?p ?o }`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(probe))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sparql-query")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// resolveActuators looks up the actuator (e.g. valve) functional-location tag
// for every newly-discovered node in nodes. The result is cached on the signal
// so each sensor pays the lookup cost at most once. A sensor with no
// diagnosesActuator relationship, with multiple candidates, or whose lookup
// fails for any reason is marked unresolvable and skipped in future polls —
// emitting a maintenance order against a misidentified asset is a worse
// failure mode than emitting nothing.
//
// The expected graph shape is:
//
//	<sensor-iri>  afo:hasName            "<sensor-tag>" .
//	<sensor-iri>  afo:diagnosesActuator  <valve-fl-iri> .
//	<valve-fl-iri> arrowhead:functionalLocation "<valve-tag>" .
//
// The third triple is already in the Skoghall store for modelled equipment;
// the first two must be added by INSERT when the sensor's knowledge graph
// is published to GraphDB.
func (t *Traits) resolveActuators(sig *SignalT, nodes map[string][]components.NodeInfo) {
	for node, nodeInfos := range nodes {
		if _, ok := sig.ValveTagByNode[node]; ok {
			continue
		}
		if sig.UnresolvableNodes[node] {
			continue
		}
		if len(nodeInfos) == 0 {
			continue
		}
		sensorName := assetNameFromURL(nodeInfos[0].URL)
		if sensorName == "" {
			log.Printf("nurse: cannot extract sensor name from %s; marking node %s unresolvable",
				nodeInfos[0].URL, node)
			sig.UnresolvableNodes[node] = true
			continue
		}
		tag, err := resolveActuatorTag(t.GraphDB_URL, sensorName, 5*time.Second)
		if err != nil {
			log.Printf("nurse: cannot resolve actuator for sensor %s on node %s: %v",
				sensorName, node, err)
			sig.UnresolvableNodes[node] = true
			continue
		}
		log.Printf("nurse: sensor %s on node %s diagnoses actuator at %s",
			sensorName, node, tag)
		sig.ValveTagByNode[node] = tag
	}
}

// resolveActuatorTag asks GraphDB for the FL tag of the actuator a sensor
// diagnoses. Returns the tag on success, or an error if the sensor has no
// diagnosesActuator triple, has more than one match (ambiguous), or the
// endpoint is unreachable.
func resolveActuatorTag(endpoint, sensorName string, timeout time.Duration) (string, error) {
	query := fmt.Sprintf(`PREFIX afo: <https://arrowheadweb.org/ont/afo#>
PREFIX arrowhead: <https://arrowheadweb.org/ont/arrowhead#>
SELECT ?valveTag WHERE {
  ?sensor afo:hasName %q .
  ?sensor afo:diagnosesActuator ?valveFL .
  ?valveFL arrowhead:functionalLocation ?valveTag .
}`, sensorName)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sparql-query")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var result struct {
		Results struct {
			Bindings []map[string]struct {
				Value string `json:"value"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	bs := result.Results.Bindings
	switch len(bs) {
	case 0:
		return "", fmt.Errorf("no diagnosesActuator relationship for sensor %q", sensorName)
	case 1:
		tag := bs[0]["valveTag"].Value
		if tag == "" {
			return "", fmt.Errorf("empty valveTag binding for sensor %q", sensorName)
		}
		return tag, nil
	default:
		return "", fmt.Errorf("ambiguous diagnosesActuator for sensor %q (%d matches)",
			sensorName, len(bs))
	}
}

// CheckGraphDBUp probes the GraphDB SPARQL endpoint with a trivial query.
// It verifies both reachability and that the repository accepts SPARQL —
// a simple TCP/HTTP check would not catch a wrong repository name. Returns
// nil on success, an error explaining the failure otherwise.
func CheckGraphDBUp(endpoint string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	const probe = `SELECT (1 AS ?ok) WHERE {}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(probe))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sparql-query")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// assetNameFromURL extracts the unit asset name from an Arrowhead service URL.
// The path structure is /<system>/<asset>/<service>, so the asset is segment [2].
func assetNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// requestMaintenanceOrder posts a maintenance order to the SAP system for the given signal
// and returns the SAP order ID on success, or an empty string on failure.
func (t *Traits) requestMaintenanceOrder(sig *SignalT, equipmentID string, location string) string {
	start := time.Now().Add(24 * time.Hour)
	end := start.Add(8 * time.Hour)

	payload := MaintenanceOrderEvent{
		EquipmentID:          equipmentID,
		FunctionalLocation:   location,
		Plant:                "1000",
		Description:          fmt.Sprintf("Signal %s out of range [%.2f, %.2f]", sig.Name, sig.LowerThreshold, sig.UpperThreshold),
		Priority:             "3",
		MaintenanceOrderType: "PM01",
		PlannedStartTime:     &start,
		PlannedEndTime:       &end,
		Operations: []MaintenanceOperation{
			{
				OperationID:  "0010",
				Text:         fmt.Sprintf("Inspect and service equipment for signal %s", sig.Name),
				WorkCenter:   "MAINT-WC01",
				Duration:     4,
				DurationUnit: "H",
			},
		},
	}

	bodyBytes, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Printf("requestMaintenanceOrder: marshal payload: %v\n", err)
		return ""
	}

	// Discover the Sapper's MaintenanceOrder service via Arrowhead. The
	// previous design hardcoded a sap_url; orchestration removes that
	// topology dependency from the config.
	if len(t.sapper.Nodes) == 0 {
		if err := usecases.Search4Services(t.sapper, t.owner); err != nil {
			log.Printf("requestMaintenanceOrder: discovery failed: %v\n", err)
			return ""
		}
	}
	var sapURL string
	for _, nodes := range t.sapper.Nodes {
		if len(nodes) > 0 {
			sapURL = nodes[0].URL
			break
		}
	}
	if sapURL == "" {
		log.Printf("requestMaintenanceOrder: no MaintenanceOrder provider found\n")
		t.sapper.Nodes = make(map[string][]components.NodeInfo) // force re-discovery next time
		return ""
	}

	log.Printf("→ SAP POST %s\n%s\n", sapURL, string(bodyBytes))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sapURL, bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("requestMaintenanceOrder: new request: %v\n", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("requestMaintenanceOrder: do request: %v\n", err)
		return ""
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("requestMaintenanceOrder: read response: %v\n", err)
		return ""
	}

	var prettyResp bytes.Buffer
	if err := json.Indent(&prettyResp, respBody, "", "  "); err == nil {
		log.Printf("← SAP %s\n%s\n", resp.Status, prettyResp.String())
	} else {
		log.Printf("← SAP %s\n%s\n", resp.Status, string(respBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	var out MaintenanceOrderResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		log.Printf("requestMaintenanceOrder: unmarshal response: %v\n", err)
		return ""
	}

	log.Printf("Maintenance order %s created for equipment %s\n", out.MaintenanceOrder, equipmentID)
	return out.MaintenanceOrder
}

// -----------------------------Turtle conversion functions

// Ack represents the incoming JSON acknowledgment from the SAP simulator.
type Ack struct {
	MaintenanceOrder        string `json:"maintenanceOrder"`
	MaintenanceNotification string `json:"maintenanceNotification"`
	Status                  string `json:"status"`
	Message                 string `json:"message"`
	CreatedAt               string `json:"createdAt"`
}

// ConvertAckJSONToTurtle converts the provided JSON bytes (in the Ack format) into a Turtle TTL string.
// If baseIRI is empty, it defaults to "https://sinetiq.se/sap/".
// Returns the TTL text or an error.
func ConvertAckJSONToTurtle(jsonBytes []byte, baseIRI string) (string, error) {
	var ack Ack
	if err := json.Unmarshal(jsonBytes, &ack); err != nil {
		return "", fmt.Errorf("unmarshal ack json: %w", err)
	}

	if baseIRI == "" {
		baseIRI = "https://sinetiq.se/sap/"
	}

	u, err := url.Parse(baseIRI)
	if err != nil {
		return "", fmt.Errorf("invalid base IRI: %w", err)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path = path.Join(u.Path, "/")
	}
	base := u.String()

	buildIRI := func(kind, id string) string {
		parsed, _ := url.Parse(base)
		parsed.Path = path.Join(parsed.Path, kind, id)
		return "<" + parsed.String() + ">"
	}

	escapeLiteral := func(s string) string {
		return strconv.Quote(s)
	}

	var b strings.Builder

	b.WriteString("@prefix ex:   <" + base + "> .\n")
	b.WriteString("@prefix schema: <http://schema.org/> .\n")
	b.WriteString("@prefix dcterms: <http://purl.org/dc/terms/> .\n")
	b.WriteString("@prefix xsd:  <http://www.w3.org/2001/XMLSchema#> .\n\n")

	orderIRI := buildIRI("MaintenanceOrder", ack.MaintenanceOrder)
	notifIRI := buildIRI("MaintenanceNotification", ack.MaintenanceNotification)

	b.WriteString(orderIRI + "\n")
	b.WriteString("    a ex:MaintenanceOrder ;\n")
	b.WriteString("    ex:orderNumber " + escapeLiteral(ack.MaintenanceOrder) + " ;\n")
	b.WriteString("    ex:status " + escapeLiteral(ack.Status) + " ;\n")
	if ack.Message != "" {
		b.WriteString("    schema:description " + escapeLiteral(ack.Message) + " ;\n")
	}
	if ack.CreatedAt != "" {
		b.WriteString("    dcterms:created " + escapeLiteral(ack.CreatedAt) + "^^xsd:dateTime ;\n")
	}
	b.WriteString("    ex:hasNotification " + notifIRI + " .\n\n")

	b.WriteString(notifIRI + "\n")
	b.WriteString("    a ex:MaintenanceNotification ;\n")
	b.WriteString("    ex:notificationNumber " + escapeLiteral(ack.MaintenanceNotification) + " ;\n")
	b.WriteString("    dcterms:isPartOf " + orderIRI + " .\n")

	return b.String(), nil
}

// ConvertMaintenanceDoneEventToTurtle converts a MaintenanceDoneEvent into a Turtle TTL string.
func ConvertMaintenanceDoneEventToTurtle(event MaintenanceDoneEvent, baseIRI string) (string, error) {
	if baseIRI == "" {
		baseIRI = "https://sinetiq.se/sap/"
	}

	u, err := url.Parse(baseIRI)
	if err != nil {
		return "", fmt.Errorf("invalid base IRI: %w", err)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path = path.Join(u.Path, "/")
	}
	base := u.String()

	buildIRI := func(kind, id string) string {
		parsed, _ := url.Parse(base)
		parsed.Path = path.Join(parsed.Path, kind, id)
		return "<" + parsed.String() + ">"
	}

	var b strings.Builder
	b.WriteString("@prefix ex:   <" + base + "> .\n")
	b.WriteString("@prefix schema: <http://schema.org/> .\n")
	b.WriteString("@prefix dcterms: <http://purl.org/dc/terms/> .\n")
	b.WriteString("@prefix xsd:  <http://www.w3.org/2001/XMLSchema#> .\n\n")

	orderIRI := buildIRI("MaintenanceOrder", event.OrderID)
	b.WriteString(orderIRI + "\n")
	b.WriteString("    a ex:MaintenanceOrder ;\n")
	b.WriteString("    ex:orderNumber " + strconv.Quote(event.OrderID) + " ;\n")
	b.WriteString("    ex:status " + strconv.Quote(event.Status) + " ;\n")
	if event.CompletedAt != nil {
		b.WriteString("    dcterms:date " + strconv.Quote(event.CompletedAt.Format(time.RFC3339)) + "^^xsd:dateTime ;\n")
	}
	if event.ActualWorkHours > 0 {
		b.WriteString("    ex:actualWorkHours \"" + strconv.FormatFloat(event.ActualWorkHours, 'f', -1, 64) + "\"^^xsd:decimal ;\n")
	}
	if event.Notes != "" {
		b.WriteString("    schema:description " + strconv.Quote(event.Notes) + " ;\n")
	}
	b.WriteString("    .\n")

	return b.String(), nil
}
