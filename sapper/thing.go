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
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the sapper unit asset.
type Traits struct {
	CompletionDelay time.Duration `json:"completionDelay"` // stored as seconds; multiplied by time.Second at runtime
	GraphDBURL      string        `json:"graphDbUrl"`       // SPARQL update endpoint; empty = disabled
	orders          map[string]*Order
	mu              sync.Mutex
	seq             atomic.Int64 // monotonic counter for order IDs
	monitor         *components.Cervice
	owner           *components.System
	ua              *components.UnitAsset
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used by the configuration step.
func initTemplate() *components.UnitAsset {
	ordersService := components.Service{
		Definition:  "MaintenanceOrder",
		SubPath:     "orders",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   30,
		Description: "creates (POST) and queries (GET ?id=<orderID>) maintenance orders",
	}

	return &components.UnitAsset{
		Name:    "SAPSimulator",
		Details: map[string][]string{"Plant": {"1000"}},
		ServicesMap: components.Services{
			ordersService.SubPath: &ordersService,
		},
		Traits: &Traits{
			CompletionDelay: 30, // 30 × time.Second = 30 s
		},
	}
}

//-------------------------------------Instantiate unit assets based on configuration

// newResource creates the unit asset with its runtime state based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		orders: make(map[string]*Order),
		owner:  sys,
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	// Build a cervice so the sapper can discover who consumes MaintenanceOrders
	// (i.e., the nurse's "SignalMonitoring" monitor endpoint) at runtime.
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	monitorCervice := &components.Cervice{
		Definition: "SignalMonitoring",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}
	t.monitor = monitorCervice

	cervices := components.Cervices{
		"monitor": monitorCervice,
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

	return ua, func() {
		log.Printf("disconnecting from %s\n", ua.Name)
	}
}

//-------------------------------------Order lifecycle

// nextID generates a zero-padded order number using a monotonic counter.
func (t *Traits) nextOrderID() string {
	n := t.seq.Add(1)
	return fmt.Sprintf("4%08d", n)
}

func (t *Traits) nextNotifID() string {
	n := t.seq.Load()
	return fmt.Sprintf("2%08d", n)
}

// createOrder stores a new order and starts its lifecycle goroutine.
func (t *Traits) createOrder(req OrderRequest) *Order {
	o := &Order{
		ID:           t.nextOrderID(),
		Notification: t.nextNotifID(),
		Status:       "CRTD",
		CreatedAt:    time.Now(),
		Request:      req,
	}
	t.mu.Lock()
	t.orders[o.ID] = o
	t.mu.Unlock()

	go t.runLifecycle(o)
	return o
}

// runLifecycle advances order status CRTD → REL → TECO and then notifies the consumer.
func (t *Traits) runLifecycle(o *Order) {
	delay := t.CompletionDelay * time.Second
	if delay <= 0 {
		delay = 30 * time.Second // safe default
	}

	// Advance to REL at half-time.
	time.Sleep(delay / 2)
	t.mu.Lock()
	o.Status = "REL"
	t.mu.Unlock()
	log.Printf("order %s → REL\n", o.ID)

	// Advance to TECO at full delay.
	time.Sleep(delay / 2)
	t.mu.Lock()
	o.Status = "TECO"
	t.mu.Unlock()
	log.Printf("order %s → TECO\n", o.ID)

	t.notifyConsumer(o)
}

// discoverMonitor is a variable so tests can substitute a fake implementation
// without needing a running Arrowhead orchestrator.
var discoverMonitor = func(c *components.Cervice, sys *components.System) error {
	c.Nodes = make(map[string][]components.NodeInfo) // reset so each call triggers fresh discovery
	return usecases.Search4Services(c, sys)
}

// notifyConsumer discovers the SignalMonitoring endpoint via Arrowhead and POSTs
// the completion event.
func (t *Traits) notifyConsumer(o *Order) {
	if err := discoverMonitor(t.monitor, t.owner); err != nil {
		log.Printf("notifyConsumer: discovery failed for order %s: %v\n", o.ID, err)
		return
	}

	// Pick the first discovered URL.
	var callbackURL string
	for _, nodes := range t.monitor.Nodes {
		if len(nodes) > 0 {
			callbackURL = nodes[0].URL
			break
		}
	}
	if callbackURL == "" {
		log.Printf("notifyConsumer: no SignalMonitoring endpoint found for order %s\n", o.ID)
		return
	}

	now := time.Now()
	event := CompletionEvent{
		OrderID:         o.ID,
		Status:          "TECO",
		CompletedAt:     &now,
		ActualWorkHours: float64(t.CompletionDelay) / 3600,
		Notes:           "Completed by SAP simulator",
	}
	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("notifyConsumer: marshal error: %v\n", err)
		return
	}

	log.Printf("→ notify %s  order=%s\n", callbackURL, o.ID)
	resp, err := http.Post(callbackURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("notifyConsumer: POST failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	log.Printf("← monitor %s  body=%s\n", resp.Status, string(msg))
}

//-------------------------------------GraphDB SPARQL insert

// buildSPARQL constructs the SPARQL UPDATE statement for a newly created order.
func (t *Traits) buildSPARQL(o *Order) string {
	orderURI := "https://sinetiq.se/sap/MaintenanceOrder/" + o.ID
	notifURI := "https://sinetiq.se/sap/MaintenanceNotification/" + o.Notification
	equipHash := fmt.Sprintf("%x", md5.Sum([]byte(o.Request.EquipmentID)))
	createdAt := o.CreatedAt.UTC().Format(time.RFC3339Nano)
	desc := strings.ReplaceAll(o.Request.Description, `"`, `\"`) // escape any quotes

	return fmt.Sprintf(`PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX ex: <https://sinetiq.se/sap/>
PREFIX schema: <http://schema.org/>
PREFIX dcterms: <http://purl.org/dc/terms/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
PREFIX step: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/>
PREFIX workorder: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/WorkOrder>
PREFIX workrequest: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/WorkRequest>
PREFIX workrequestassignment: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/WorkRequestAssignment>
PREFIX identifier: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/Identifier>
INSERT
{
    GRAPH<https://arrowheadweb.org/graph/sap/workorders>
    {
        # step:WorkOrder
        <%s> a step:WorkOrder ;
            workorder:Id <%s-id> ;
            workorder:Description <%s-description> ;
            ex:status "%s" ;
            dcterms:created "%s"^^xsd:dateTime ;
            workorder:InResponseTo <%s> .

        <%s-id> a step:Identifier ;
            identifier:Id "%s" .

        <%s-description> a step:LocalizedString ;
            rdfs:label "Maintenance order created successfully"@en .

        # step:WorkRequest
        <%s> a step:WorkRequest ;
            workrequest:Id <%s-id> ;
            workrequest:Description <%s-description> ;
            dcterms:isPartOf <%s> .

        <%s-id> a step:Identifier ;
            identifier:Id "%s" .

        <%s-description> a step:LocalizedString ;
            rdfs:label "%s"@en .

        # step:WorkRequestAssignment
        <%s-WRA> a step:WorkRequestAssignment ;
            workrequestassignment:AssignedTo <https://arrowheadweb.org/data/%s> ;
            workrequestassignment:AssignedWorkRequest <%s> .
    }
}
where
{
}`,
		// WorkOrder
		orderURI, orderURI, orderURI,
		o.Status, createdAt, notifURI,
		// WorkOrder-id
		orderURI, o.ID,
		// WorkOrder-description
		orderURI,
		// WorkRequest
		notifURI, notifURI, notifURI, orderURI,
		// WorkRequest-id
		notifURI, o.Notification,
		// WorkRequest-description
		notifURI, desc,
		// WorkRequestAssignment
		notifURI, equipHash, notifURI,
	)
}

// insertToGraphDB prints the SPARQL UPDATE to the terminal and, when GraphDBURL
// is configured, POSTs it to GraphDB.
func (t *Traits) insertToGraphDB(o *Order) {
	sparql := t.buildSPARQL(o)
	log.Printf("→ GraphDB INSERT order=%s\n%s\n", o.ID, sparql)

	if t.GraphDBURL == "" {
		return
	}

	resp, err := http.Post(t.GraphDBURL, "application/sparql-update", strings.NewReader(sparql))
	if err != nil {
		log.Printf("insertToGraphDB: POST failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	log.Printf("← GraphDB %s  body=%s\n", resp.Status, string(msg))
}

//-------------------------------------HTTP handlers

// createOrderHandler handles POST /orders — creates a new maintenance order.
func (t *Traits) createOrderHandler(w http.ResponseWriter, r *http.Request) {
	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.EquipmentID == "" || req.Plant == "" || req.Description == "" {
		http.Error(w, "equipmentId, plant and description are required", http.StatusBadRequest)
		return
	}

	o := t.createOrder(req)
	log.Printf("order created: id=%s equipment=%s\n", o.ID, req.EquipmentID)
	go t.insertToGraphDB(o)

	resp := OrderResponse{
		MaintenanceOrder:        o.ID,
		MaintenanceNotification: o.Notification,
		Status:                  o.Status,
		Message:                 "Maintenance order created successfully",
		CreatedAt:               o.CreatedAt,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// queryOrderHandler handles GET /orders?id=<orderID>.
func (t *Traits) queryOrderHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "query parameter 'id' is required", http.StatusBadRequest)
		return
	}
	t.mu.Lock()
	o, ok := t.orders[id]
	t.mu.Unlock()
	if !ok {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"maintenanceOrder": o.ID,
		"status":           o.Status,
		"createdAt":        o.CreatedAt.Format(time.RFC3339),
	})
}
