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
	"os"
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
			SAP_URL: "http://192.168.1.4:8080/api/v1/maintenance-orders",
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
	ua.Traits.Signals[0].Operational = true // default to operational

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
			if tup.Value > ua.Traits.Signals[0].Threshold && ua.Traits.Signals[0].Operational {
				log.Printf("ALERT: %s value %.2f exceeds threshold %.2f\n", name, tup.Value, ua.Traits.Signals[0].Threshold)
				ua.Traits.Signals[0].TOverCount++
				if ua.Traits.Signals[0].TOverCount >= 5 {
					ua.Traits.Signals[0].Operational = false
					log.Printf("Action required: %s is not operational. Work requested.\n", name)
				}
				if !ua.Traits.Signals[0].Operational {
					ua.requestMaintenanceOrder()
					ua.Traits.Signals[0].WorkRequested = true
				}
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

func (ua *UnitAsset) update(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var event MaintenanceDoneEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "failed to unmarshal request body: "+err.Error(), http.StatusBadRequest)
		return
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

func (ua *UnitAsset) requestMaintenanceOrder() {

	// Parse RFC3339 timestamps (same format as in your curl JSON)
	start, err := time.Parse(time.RFC3339, "2025-08-21T08:00:00Z")
	if err != nil {
		fatalf("parse plannedStartTime: %v", err)
	}
	end, err := time.Parse(time.RFC3339, "2025-08-21T16:00:00Z")
	if err != nil {
		fatalf("parse plannedEndTime: %v", err)
	}

	// Build request payload using your models
	payload := MaintenanceOrderEvent{
		EquipmentID:          "10000045",
		FunctionalLocation:   "FL100-200-300",
		Plant:                "1000",
		Description:          "Replace pump seal due to leakage",
		Priority:             "3",
		MaintenanceOrderType: "PM01",
		PlannedStartTime:     &start,
		PlannedEndTime:       &end,
		Operations: []MaintenanceOperation{
			{
				Text:         "Disassemble pump",
				WorkCenter:   "PUMP-WC01",
				Duration:     4,
				DurationUnit: "H",
			},
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		fatalf("marshal payload: %v", err)
	}

	// Add a timeout so you don’t hang forever if the server never replies
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ua.SAP_URL, bytes.NewReader(bodyBytes))
	if err != nil {
		fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout: 20 * time.Second, // overall client timeout
	}

	resp, err := client.Do(req)
	if err != nil {
		fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fatalf("read response: %v", err)
	}

	// If server returns non-2xx, print body to help debugging
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fatalf("server returned %s\nbody:\n%s", resp.Status, string(respBody))
	}

	// Try to decode into your expected response struct
	var out MaintenanceOrderResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		// If response isn’t exactly that JSON, still show raw body
		fmt.Printf("Response status: %s\nRaw body:\n%s\n", resp.Status, string(respBody))
		fatalf("unmarshal MaintenanceOrderResponse: %v", err)
	}

	// Pretty-print the parsed response
	pretty, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("Response status: %s\nParsed response:\n%s\n", resp.Status, string(pretty))

	ttl, err := ConvertAckJSONToTurtle(respBody, "https://sinetiq.se/sap/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "convert to turtle: %v\n", err)
		os.Exit(3)
	}

	fmt.Println(ttl)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
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

	// normalize base IRI to ensure it ends with '/'
	u, err := url.Parse(baseIRI)
	if err != nil {
		return "", fmt.Errorf("invalid base IRI: %w", err)
	}
	// ensure trailing slash
	if !strings.HasSuffix(u.Path, "/") {
		u.Path = path.Join(u.Path, "/")
	}
	base := u.String()

	// Helper to build a safe resource IRI (we assume order/notification ids are safe tokens)
	buildIRI := func(kind, id string) string {
		// create a path like .../MaintenanceOrder/400000875
		// Use path.Join on the path portion to avoid // issues
		parsed, _ := url.Parse(base)
		parsed.Path = path.Join(parsed.Path, kind, id)
		return "<" + parsed.String() + ">"
	}

	// escape literals for Turtle (we can leverage strconv.Quote which returns a valid double-quoted string
	// with escapes for newline, quotes, backslashes etc.)
	escapeLiteral := func(s string) string {
		return strconv.Quote(s)
	}

	var b strings.Builder

	// Prefixes
	b.WriteString("@prefix ex:   <" + base + "> .\n")
	b.WriteString("@prefix schema: <http://schema.org/> .\n")
	b.WriteString("@prefix dcterms: <http://purl.org/dc/terms/> .\n")
	b.WriteString("@prefix xsd:  <http://www.w3.org/2001/XMLSchema#> .\n\n")

	// Order resource
	orderIRI := buildIRI("MaintenanceOrder", ack.MaintenanceOrder)
	notifIRI := buildIRI("MaintenanceNotification", ack.MaintenanceNotification)

	b.WriteString(orderIRI + "\n")
	b.WriteString("    a ex:MaintenanceOrder ;\n")
	b.WriteString("    ex:orderNumber " + escapeLiteral(ack.MaintenanceOrder) + " ;\n")
	// status as literal (could later be modelled as a resource)
	b.WriteString("    ex:status " + escapeLiteral(ack.Status) + " ;\n")
	// message -> schema:description
	if ack.Message != "" {
		b.WriteString("    schema:description " + escapeLiteral(ack.Message) + " ;\n")
	}
	// createdAt typed as xsd:dateTime if present
	if ack.CreatedAt != "" {
		// use quoted literal ^^xsd:dateTime
		b.WriteString("    dcterms:created " + escapeLiteral(ack.CreatedAt) + "^^xsd:dateTime ;\n")
	}
	// link to notification
	b.WriteString("    ex:hasNotification " + notifIRI + " .\n\n")

	// Notification resource
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
