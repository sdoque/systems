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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
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
	Name          string              `json:"serviceDefinition"`
	Details       map[string][]string `json:"details"`
	Period        time.Duration       `json:"samplingPeriod"`
	Threshold     float64             `json:"threshold"`
	TOverCount    map[string]int  `json:"-"` // consecutive over-threshold count per source node
	WorkRequested map[string]bool `json:"-"` // pending maintenance order per source node
	Operational   bool            `json:"-"` // false when any node has a pending order
}

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the nurse unit asset.
type Traits struct {
	SAP_URL       string            `json:"sap_url"`
	Signals       []SignalT         `json:"signals"`
	pendingOrders map[string]string // orderID → signalName; not serialized
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

	return &components.UnitAsset{
		Name:    "HealthTracker",
		Details: map[string][]string{},
		ServicesMap: components.Services{
			monitorService.SubPath: &monitorService,
		},
		Traits: &Traits{
			SAP_URL: "http://192.168.1.108:20191/sapper/SAPSimulator/orders",
			Signals: []SignalT{
				{
					Name:          "temperature",
					Details:       map[string][]string{"Unit": {"Celsius"}},
					Period:        4,
					Threshold:     75.0,
					Operational: true,
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
	}

	// Derive the health endpoint from the SAP URL (replace the API path with /health)
	sapHealthURL := t.SAP_URL
	if u, err := url.Parse(t.SAP_URL); err == nil {
		u.Path = "/health"
		sapHealthURL = u.String()
	}
	r := CheckServerUp(sapHealthURL, 2*time.Second)
	if r.Up {
		fmt.Printf("SAP server is up (status=%d, in %s)\n", r.StatusCode, r.Duration)
	} else {
		fmt.Printf("SAP server is down (%v, in %s)\n", r.Err, r.Duration)
	}

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	cervices := make(components.Cervices)
	for _, signal := range t.Signals {
		cSignal := components.Cervice{
			Definition: signal.Name,
			Details:    signal.Details,
			Protos:     sProtocols,
			Nodes:      make(map[string][]components.NodeInfo),
		}
		cervices[cSignal.Definition] = &cSignal
	}

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
// when any single source exceeds the threshold 5 consecutive times.
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

			// Query each provider individually to preserve its identity for per-source counting.
			failed := false
			for node, nodeInfos := range cer.Nodes {
				if failed {
					break
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

					if tup.Value > sig.Threshold {
						sig.TOverCount[node]++
						log.Printf("ALERT: %s/%s value %.2f exceeds threshold %.2f (count: %d/5)\n",
							name, node, tup.Value, sig.Threshold, sig.TOverCount[node])
						if sig.TOverCount[node] >= 5 {
							sig.Operational = false
							sig.WorkRequested[node] = true
							log.Printf("Signal %s/%s non-operational, requesting maintenance\n", name, node)
							equipmentID := assetNameFromURL(ni.URL)
							location := ""
							if locs, ok := ni.Details["FunctionalLocation"]; ok && len(locs) > 0 {
								location = locs[0]
							}
							orderID := t.requestMaintenanceOrder(sig, equipmentID, location)
							if orderID != "" {
								t.pendingOrders[orderID] = name
								log.Printf("Maintenance order %s created for signal %s/%s\n", orderID, name, node)
							} else {
								log.Printf("SAP order failed for signal %s/%s; monitoring paused until system restart\n", name, node)
							}
						}
					} else if sig.TOverCount[node] > 0 {
						log.Printf("Signal %s/%s back below threshold (resetting count from %d)\n",
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
		text += fmt.Sprintf("Signal: %s, Threshold: %f, TOverCount:[%s], Operational: %t, WorkRequested:[%s]\n",
			signal.Name, signal.Threshold, counts, signal.Operational, pending)
	}
	w.Write([]byte(text))
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
		Description:          fmt.Sprintf("Signal %s exceeded threshold %.2f", sig.Name, sig.Threshold),
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

	log.Printf("→ SAP POST %s\n%s\n", t.SAP_URL, string(bodyBytes))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.SAP_URL, bytes.NewReader(bodyBytes))
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
