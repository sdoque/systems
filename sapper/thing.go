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
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// firefightingHTML is the html/template body served at
// /sapper/SAPSimulator/firefighting. Inlined here (rather than as a separate
// .html file) to keep the system self-contained — same convention as the
// clerk system's orderPage.
const firefightingHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Sapper — Firefighting</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
           margin: 1.5em; color: #222; }
    h1 { margin-top: 0; }
    h2 { margin-top: 1.5em; border-bottom: 1px solid #ccc; padding-bottom: 0.2em; }
    table { border-collapse: collapse; width: 100%; }
    th, td { padding: 0.4em 0.6em; text-align: left; border-bottom: 1px solid #eee; }
    th { background: #f3f3f3; }
    tr:hover { background: #fafafa; }
    textarea { width: 100%; font-family: ui-monospace, "SF Mono", Menlo, monospace;
               font-size: 0.9em; padding: 0.5em; }
    .submit { margin-top: 1em; }
    .submit button { padding: 0.6em 1.4em; font-size: 1em; cursor: pointer;
                     background: #2d6cdf; color: white; border: 0; border-radius: 4px; }
    .submit button:hover { background: #1a4fb8; }
    .empty { color: #888; font-style: italic; }
    .flash { padding: 0.6em 0.9em; margin-bottom: 1em; border-radius: 4px; }
    .flash.ok { background: #e3f7e0; border: 1px solid #8bce85; }
    .flash.err { background: #fce6e6; border: 1px solid #d97070; }
  </style>
</head>
<body>
  <h1>Sapper · Firefighting</h1>
  <p>Work orders awaiting planner enrichment and release. Pick an order, edit
     the operations JSON to match the field decision, then submit. The order
     transitions <code>CRTD&nbsp;→&nbsp;REL</code> and the 30 s TECO countdown
     begins.</p>

  {{if .Flash}}<div class="flash {{.FlashKind}}">{{.Flash}}</div>{{end}}

  <form method="POST" action="firefighting">
    <h2>Work orders (CRTD)</h2>
    <table>
      <thead>
        <tr>
          <th></th><th>Order</th><th>Sensor</th><th>Functional location</th>
          <th>Description</th><th>Created</th>
        </tr>
      </thead>
      <tbody>
        {{range .Orders}}
        <tr>
          <td><input type="radio" name="orderId" value="{{.ID}}" data-enrichment="{{.SuggestedEnrichment}}" required></td>
          <td><code>{{.ID}}</code></td>
          <td>{{.Request.EquipmentID}}</td>
          <td>{{.Request.FunctionalLocation}}</td>
          <td>{{.Request.Description}}</td>
          <td>{{.CreatedAt.Format "15:04:05"}}</td>
        </tr>
        {{else}}
        <tr><td colspan="6" class="empty">No CRTD orders awaiting release.</td></tr>
        {{end}}
      </tbody>
    </table>

    <h2>Enrichment payload</h2>
    <textarea name="enrichment" rows="32" spellcheck="false" placeholder="Select a work order to load its suggested enrichment payload."></textarea>

    <p class="submit"><button type="submit">Submit</button></p>
  </form>

  <script>
    // Selecting an order swaps the textarea to that order's suggested enrichment.
    // Event delegation on the form so it survives tbody refreshes below.
    const form = document.querySelector("form");
    const textarea = document.querySelector('textarea[name="enrichment"]');
    form.addEventListener("change", e => {
      if (e.target.matches('input[name="orderId"]')) {
        const v = e.target.dataset.enrichment;
        if (v) textarea.value = v;
      }
    });

    // Periodically refresh just the order list so new CRTD orders appear and
    // released ones drop off, without disturbing the textarea the planner is
    // editing. The currently-selected order is restored if still present.
    async function refreshTable() {
      const sel = form.querySelector('input[name="orderId"]:checked');
      const selID = sel ? sel.value : null;
      try {
        const resp = await fetch("firefighting");
        if (!resp.ok) return;
        const html = await resp.text();
        const doc = new DOMParser().parseFromString(html, "text/html");
        const newBody = doc.querySelector("tbody");
        if (!newBody) return;
        document.querySelector("tbody").innerHTML = newBody.innerHTML;
        if (selID) {
          const r = form.querySelector('input[name="orderId"][value="' + selID + '"]');
          if (r) r.checked = true;
        }
      } catch (_) { /* transient network blip; try again next tick */ }
    }
    setInterval(refreshTable, 3000);
  </script>
</body>
</html>
`

// defaultEnrichmentJSON pre-fills the textarea with Alex's seals/gaskets
// example payload. Edit-in-place is the planner's main job.
const defaultEnrichmentJSON = `{
  "operations": [
    {
      "operation": "0010",
      "description": "Isolate valve and verify closure",
      "plannedWorkQuantity": "1.0",
      "workQuantityUnit": "H",
      "workCenter": "VALVE-WC01",
      "plant": "1000"
    },
    {
      "operation": "0020",
      "description": "Disassemble valve body",
      "plannedWorkQuantity": "1.5",
      "workQuantityUnit": "H",
      "workCenter": "VALVE-WC01",
      "plant": "1000"
    },
    {
      "operation": "0030",
      "description": "Replace gaskets and stem",
      "plannedWorkQuantity": "1.0",
      "workQuantityUnit": "H",
      "workCenter": "VALVE-WC01",
      "plant": "1000",
      "components": [
        {
          "material": "VALVE-GASKET-V50",
          "description": "Valve gasket set",
          "requiredQuantity": "2.0",
          "unit": "EA",
          "plant": "1000",
          "storageLocation": "0001"
        },
        {
          "material": "VALVE-STEM-KIT",
          "description": "Valve stem repair kit",
          "requiredQuantity": "1.0",
          "unit": "EA",
          "plant": "1000",
          "storageLocation": "0001"
        }
      ]
    },
    {
      "operation": "0040",
      "description": "Reassemble and pressure test valve",
      "plannedWorkQuantity": "1.5",
      "workQuantityUnit": "H",
      "workCenter": "VALVE-WC01",
      "plant": "1000",
      "components": [
        {
          "material": "THREAD-SEALANT",
          "description": "Thread sealant compound",
          "requiredQuantity": "1.0",
          "unit": "EA",
          "plant": "1000",
          "storageLocation": "0001"
        }
      ]
    }
  ]
}
`

var firefightingTemplate = template.Must(template.New("firefighting").Parse(firefightingHTML))

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the sapper unit asset.
type Traits struct {
	CompletionDelay time.Duration `json:"completionDelay"` // stored as seconds; multiplied by time.Second at runtime
	GraphDBURL      string        `json:"graphDbUrl"`      // SPARQL update endpoint; empty = disabled
	orders          map[string]*Order
	mu              sync.Mutex
	seq             atomic.Int64 // monotonic counter for order IDs
	primeOnce       sync.Once    // guards a single graph-peek before the first order is allocated
	monitor         *components.Cervice
	enrichment      *components.Cervice // discovers the nurse's enrichment endpoint
	owner           *components.System
	ua              *components.UnitAsset
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used by the configuration step.
func initTemplate() *components.UnitAsset {
	ordersService := components.Service{
		Definition:  "MaintenanceOrder",
		SubPath:     "maintenanceorders",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   30,
		Description: "creates (POST) and queries (GET ?id=<orderID>) maintenance orders",
	}
	firefightingService := components.Service{
		Definition:  "firefighting",
		SubPath:     "firefighting",
		Details:     map[string][]string{"Forms": {"text/html"}},
		RegPeriod:   30,
		Description: "planner UI (GET) to enrich and release CRTD work orders (POST)",
	}

	return &components.UnitAsset{
		Name:    "SAPSimulator",
		Details: map[string][]string{"Plant": {"1000"}},
		ServicesMap: components.Services{
			ordersService.SubPath:       &ordersService,
			firefightingService.SubPath: &firefightingService,
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

	// Build cervices so the sapper can discover, at runtime, the nurse's
	// callback endpoints: SignalMonitoring (TECO completion) and
	// EnrichmentNotification (planner's release with enrichment payload).
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	monitorCervice := &components.Cervice{
		Definition: "SignalMonitoring",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "get",
	}
	enrichmentCervice := &components.Cervice{
		Definition: "EnrichmentNotification",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "set",
	}
	t.monitor = monitorCervice
	t.enrichment = enrichmentCervice

	cervices := components.Cervices{
		"monitor":    monitorCervice,
		"enrichment": enrichmentCervice,
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

// nextOrderID generates a zero-padded order number using a monotonic counter.
// On the very first call, it queries GraphDB for the highest order number
// already in the workorders graph and jumps the counter past it, so a
// restarted Sapper does not produce IDs that collide with historical orders.
func (t *Traits) nextOrderID() string {
	t.primeOnce.Do(t.primeCounterFromGraph)
	n := t.seq.Add(1)
	return fmt.Sprintf("4%08d", n)
}

// primeCounterFromGraph fast-forwards the order counter to one past the
// highest existing ID in the workorders graph. Silently no-ops if the graph
// is unconfigured, unreachable, empty, or returns garbage — in any of those
// cases the counter stays at 0 and the next order will be 400000001 (the
// previous, restart-resetting behaviour).
func (t *Traits) primeCounterFromGraph() {
	if t.GraphDBURL == "" {
		return
	}
	maxN, ok := t.peekMaxOrderID()
	if !ok {
		return
	}
	// CAS loop so a concurrent caller can't drag the counter backwards if
	// they raced ahead between our load and store.
	for {
		cur := t.seq.Load()
		if cur >= maxN {
			return
		}
		if t.seq.CompareAndSwap(cur, maxN) {
			log.Printf("sapper: counter primed from graph at %d (next order will be 4%08d)", maxN, maxN+1)
			return
		}
	}
}

// peekMaxOrderID queries GraphDB for the highest order number already
// recorded as a step:WorkOrder under our IRI namespace. Returns (n, true) on
// success, (0, true) when the graph is empty, (0, false) on any error.
func (t *Traits) peekMaxOrderID() (int64, bool) {
	const query = `PREFIX step: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/>
PREFIX identifier: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/Identifier>
PREFIX workorder: <http://www.semanticweb.org/ARROWHEADfPVN/ontologies/STEP_AP4K/WorkOrder>
SELECT (MAX(?id) AS ?max) WHERE {
    ?wo a step:WorkOrder ;
        workorder:Id ?idNode .
    FILTER(STRSTARTS(STR(?wo), "https://sinetiq.se/sap/"))
    ?idNode identifier:Id ?id .
}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.GraphDBURL, strings.NewReader(query))
	if err != nil {
		log.Printf("sapper: peekMaxOrderID: build request: %v", err)
		return 0, false
	}
	req.Header.Set("Content-Type", "application/sparql-query")
	req.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("sapper: peekMaxOrderID: POST failed: %v", err)
		return 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("sapper: peekMaxOrderID: HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		return 0, false
	}

	var result struct {
		Results struct {
			Bindings []map[string]struct {
				Value string `json:"value"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("sapper: peekMaxOrderID: decode: %v", err)
		return 0, false
	}
	if len(result.Results.Bindings) == 0 {
		return 0, true // aggregation returned no rows: empty graph
	}
	binding, ok := result.Results.Bindings[0]["max"]
	if !ok || binding.Value == "" {
		return 0, true // no MAX binding: graph has no WorkOrder triples under our namespace
	}
	if len(binding.Value) < 1 || binding.Value[0] != '4' {
		log.Printf("sapper: peekMaxOrderID: unexpected ID format %q", binding.Value)
		return 0, false
	}
	n, err := strconv.ParseInt(binding.Value[1:], 10, 64)
	if err != nil {
		log.Printf("sapper: peekMaxOrderID: parse %q: %v", binding.Value, err)
		return 0, false
	}
	return n, true
}

func (t *Traits) nextNotifID() string {
	n := t.seq.Load()
	return fmt.Sprintf("2%08d", n)
}

// createOrder stores a new order in CRTD status. It does NOT start the
// lifecycle countdown — the order waits in CRTD until a planner enriches and
// releases it through the firefighting UI.
func (t *Traits) createOrder(req OrderRequest) *Order {
	o := &Order{
		ID:                  t.nextOrderID(),
		Notification:        t.nextNotifID(),
		Status:              "CRTD",
		CreatedAt:           time.Now(),
		Request:             req,
		SuggestedEnrichment: defaultEnrichmentJSON,
	}
	t.mu.Lock()
	t.orders[o.ID] = o
	t.mu.Unlock()
	return o
}

// enrichAndRelease attaches the planner's enrichment to the order, transitions
// it to REL, records the release in GraphDB, notifies the nurse with the
// enrichment payload, and starts the TECO countdown. Returns false if the
// order does not exist or is not in CRTD status.
func (t *Traits) enrichAndRelease(orderID string, enrichment json.RawMessage) (*Order, error) {
	t.mu.Lock()
	o, ok := t.orders[orderID]
	if !ok {
		t.mu.Unlock()
		return nil, fmt.Errorf("order %s not found", orderID)
	}
	if o.Status != "CRTD" {
		t.mu.Unlock()
		return nil, fmt.Errorf("order %s is %s, only CRTD orders can be released", orderID, o.Status)
	}
	o.Status = "REL"
	o.ReleasedAt = time.Now()
	o.Enrichment = enrichment
	t.mu.Unlock()
	log.Printf("order %s → REL (planner authorised)\n", o.ID)

	go t.insertReleaseToGraphDB(o)
	go t.notifyEnrichment(o)
	go t.runTECOCountdown(o)
	return o, nil
}

// runTECOCountdown sleeps the configured completion delay and then transitions
// the order to TECO. Called from enrichAndRelease — the countdown is measured
// from the planner's release, not from order creation.
func (t *Traits) runTECOCountdown(o *Order) {
	delay := t.CompletionDelay * time.Second
	if delay <= 0 {
		delay = 30 * time.Second // safe default
	}
	time.Sleep(delay)

	t.mu.Lock()
	o.Status = "TECO"
	t.mu.Unlock()
	log.Printf("order %s → TECO\n", o.ID)

	go t.insertCompletionToGraphDB(o)
	t.notifyConsumer(o)
}

// discoverMonitor is a variable so tests can substitute a fake implementation
// without needing a running Arrowhead orchestrator.
var discoverMonitor = func(c *components.Cervice, sys *components.System) error {
	c.Nodes = make(map[string][]components.NodeInfo) // reset so each call triggers fresh discovery
	return usecases.Search4Services(c, sys)
}

// discoverEnrichment mirrors discoverMonitor for the nurse's enrichment endpoint.
var discoverEnrichment = func(c *components.Cervice, sys *components.System) error {
	c.Nodes = make(map[string][]components.NodeInfo)
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

// buildRELInsertSPARQL records the planner's release of an order: the new REL
// status triple and the release timestamp. The enrichment JSON itself is not
// stored as a literal in the graph — it ships to the nurse over the
// EnrichmentNotification path and is captured in the nurse's log; embedding
// it as a SPARQL string literal couples graph state to textarea encoding and
// produces malformed queries on CRLF input.
func (t *Traits) buildRELInsertSPARQL(o *Order) string {
	orderURI := "https://sinetiq.se/sap/MaintenanceOrder/" + o.ID
	releasedAt := o.ReleasedAt.UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf(`PREFIX ex: <https://sinetiq.se/sap/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
INSERT DATA {
    GRAPH <https://arrowheadweb.org/graph/sap/workorders> {
        <%s> ex:status "REL" ;
            ex:releasedAt "%s"^^xsd:dateTime .
    }
}`, orderURI, releasedAt)
}

// insertReleaseToGraphDB pushes the REL transition to GraphDB.
func (t *Traits) insertReleaseToGraphDB(o *Order) {
	t.postSPARQL("REL", o.ID, t.buildRELInsertSPARQL(o))
}

// notifyEnrichment posts the planner's enrichment payload to the nurse's
// enrichment endpoint discovered via Arrowhead. Independent from
// notifyConsumer (which fires on TECO).
func (t *Traits) notifyEnrichment(o *Order) {
	if t.enrichment == nil {
		log.Printf("notifyEnrichment: no enrichment cervice for order %s\n", o.ID)
		return
	}
	if err := discoverEnrichment(t.enrichment, t.owner); err != nil {
		log.Printf("notifyEnrichment: discovery failed for order %s: %v\n", o.ID, err)
		return
	}
	var callbackURL string
	for _, nodes := range t.enrichment.Nodes {
		if len(nodes) > 0 {
			callbackURL = nodes[0].URL
			break
		}
	}
	if callbackURL == "" {
		log.Printf("notifyEnrichment: no EnrichmentNotification endpoint found for order %s\n", o.ID)
		return
	}

	// The body bundles the order ID with the planner's payload so the nurse
	// can correlate without parsing the URL.
	envelope, err := json.Marshal(struct {
		OrderID    string          `json:"orderId"`
		Status     string          `json:"status"`
		ReleasedAt time.Time       `json:"releasedAt"`
		Enrichment json.RawMessage `json:"enrichment"`
	}{o.ID, o.Status, o.ReleasedAt, o.Enrichment})
	if err != nil {
		log.Printf("notifyEnrichment: marshal error: %v\n", err)
		return
	}

	log.Printf("→ enrichment %s  order=%s\n", callbackURL, o.ID)
	resp, err := http.Post(callbackURL, "application/json", bytes.NewReader(envelope))
	if err != nil {
		log.Printf("notifyEnrichment: POST failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	log.Printf("← enrichment %s  body=%s\n", resp.Status, string(msg))
}

// buildTECOInsertSPARQL constructs the SPARQL UPDATE statement that records an
// order's transition to TECO. It appends new triples (status, completion time,
// actual work hours) to the existing Order IRI created by buildSPARQL — the
// graph retains both the CRTD and TECO status triples as an audit trail.
func (t *Traits) buildTECOInsertSPARQL(o *Order) string {
	orderURI := "https://sinetiq.se/sap/MaintenanceOrder/" + o.ID
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	actualHours := float64(t.CompletionDelay) / 3600.0 // CompletionDelay is in seconds

	return fmt.Sprintf(`PREFIX ex: <https://sinetiq.se/sap/>
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
INSERT DATA {
    GRAPH <https://arrowheadweb.org/graph/sap/workorders> {
        <%s> ex:status "TECO" ;
            ex:completedAt "%s"^^xsd:dateTime ;
            ex:actualWorkHours "%g"^^xsd:decimal .
    }
}`, orderURI, completedAt, actualHours)
}

// updateEndpoint returns the SPARQL update URL by appending /statements to the
// repository base URL the operator configured. Empty in, empty out.
func (t *Traits) updateEndpoint() string {
	if t.GraphDBURL == "" {
		return ""
	}
	return strings.TrimRight(t.GraphDBURL, "/") + "/statements"
}

// insertToGraphDB prints the SPARQL UPDATE to the terminal and, when GraphDBURL
// is configured, POSTs it to GraphDB.
func (t *Traits) insertToGraphDB(o *Order) {
	t.postSPARQL("CRTD", o.ID, t.buildSPARQL(o))
}

// insertCompletionToGraphDB records the order's TECO transition in the graph.
// Called from runLifecycle once the in-memory status flips to TECO.
func (t *Traits) insertCompletionToGraphDB(o *Order) {
	t.postSPARQL("TECO", o.ID, t.buildTECOInsertSPARQL(o))
}

// postSPARQL is the common path for both lifecycle pushes. Graph publication is
// a side effect of the SAP lifecycle, not load-bearing for it, so failures here
// are logged and dropped — the maintenance loop continues either way.
func (t *Traits) postSPARQL(stage, orderID, sparql string) {
	log.Printf("→ GraphDB INSERT (%s) order=%s\n%s\n", stage, orderID, sparql)

	endpoint := t.updateEndpoint()
	if endpoint == "" {
		return
	}

	resp, err := http.Post(endpoint, "application/sparql-update", strings.NewReader(sparql))
	if err != nil {
		log.Printf("postSPARQL (%s): POST failed: %v\n", stage, err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	log.Printf("← GraphDB %s  body=%s\n", resp.Status, string(msg))
}

//-------------------------------------HTTP handlers

// firefightingHandler routes the firefighting UI: GET renders the planner
// page (work-order list + enrichment textarea), POST processes a Submit then
// redirects (303 See Other) back to GET so browser reload doesn't re-POST.
func (t *Traits) firefightingHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		t.renderFirefighting(w, q.Get("msg"), q.Get("kind"))
	case http.MethodPost:
		t.submitFirefighting(w, r)
	default:
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
	}
}

// renderFirefighting writes the firefighting HTML, optionally with a flash
// message ("ok" or "err") after a Submit.
func (t *Traits) renderFirefighting(w http.ResponseWriter, flash, kind string) {
	t.mu.Lock()
	crtd := make([]*Order, 0, len(t.orders))
	for _, o := range t.orders {
		if o.Status == "CRTD" {
			crtd = append(crtd, o)
		}
	}
	t.mu.Unlock()
	sort.Slice(crtd, func(i, j int) bool { return crtd[i].ID < crtd[j].ID })

	data := struct {
		Orders    []*Order
		Flash     string
		FlashKind string
	}{crtd, flash, kind}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := firefightingTemplate.Execute(w, data); err != nil {
		log.Printf("firefighting: template error: %v\n", err)
	}
}

// submitFirefighting handles the planner's Submit: validate, release the
// order, then 303-redirect to GET with a flash message in the query string.
// Post/Redirect/Get prevents browser reload from re-submitting the form.
func (t *Traits) submitFirefighting(w http.ResponseWriter, r *http.Request) {
	msg, kind := t.processSubmit(r)
	q := url.Values{"msg": {msg}, "kind": {kind}}
	http.Redirect(w, r, "firefighting?"+q.Encode(), http.StatusSeeOther)
}

func (t *Traits) processSubmit(r *http.Request) (string, string) {
	if err := r.ParseForm(); err != nil {
		return "Could not parse form: " + err.Error(), "err"
	}
	orderID := strings.TrimSpace(r.FormValue("orderId"))
	enrichment := strings.TrimSpace(r.FormValue("enrichment"))
	if orderID == "" {
		return "Pick a work order before submitting.", "err"
	}
	if !json.Valid([]byte(enrichment)) {
		return "Enrichment is not valid JSON.", "err"
	}
	o, err := t.enrichAndRelease(orderID, json.RawMessage(enrichment))
	if err != nil {
		return err.Error(), "err"
	}
	return fmt.Sprintf("Order %s released. TECO in %d s.", o.ID, t.CompletionDelay), "ok"
}

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
