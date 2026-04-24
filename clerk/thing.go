/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
 *   Franziska Sievert - initial implementation
 *   Jan A. van Deventer, Luleå - modernized for current mbaigo
 ***************************************************************************SDG*/

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Order form

// PenHolderOrder_v1 is the exchanged form for pen holder orders.
type PenHolderOrder_v1 struct {
	OrderNumber        int       `json:"order_number"`
	Name               string    `json:"name"`
	Email              string    `json:"email"`
	Height             float64   `json:"height"`
	Depth              float64   `json:"depth"`
	Roughness          int       `json:"roughness"`
	OrderedTimestamp   time.Time `json:"timestamp"`
	CompletedTimestamp time.Time `json:"completed_timestamp"`
	ProductionLine     string    `json:"production_line"`
	PeppolID           string    `json:"peppol_id"`
	Version            string    `json:"version"`
}

func (f *PenHolderOrder_v1) NewForm() forms.Form {
	f.Version = "PenHolderOrder_v1"
	return f
}

func (f *PenHolderOrder_v1) FormVersion() string {
	return f.Version
}

func init() {
	forms.FormTypeMap["PenHolderOrder_v1"] = reflect.TypeOf(PenHolderOrder_v1{})
}

//-------------------------------------Define the unit asset

// Traits holds the runtime state for the clerk unit asset.
type Traits struct {
	owner    *components.System  `json:"-"`
	cervices components.Cervices `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used to seed systemconfig.json.
func initTemplate() *components.UnitAsset {
	ordersService := components.Service{
		Definition:  "orders",
		SubPath:     "orders",
		Details:     map[string][]string{"Forms": {"PenHolderOrder_v1"}},
		RegPeriod:   60,
		Description: "browser order form (GET), submit new order (POST), look up order (GET ?id=N)",
	}

	return &components.UnitAsset{
		Name:    "product",
		Mission: "take_orders",
		Details: map[string][]string{"Collection": {"PenHolder"}},
		ServicesMap: components.Services{
			ordersService.SubPath: &ordersService,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its cervices.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)

	// Cervice: the tracker's order service (we POST and GET to it).
	orderCer := &components.Cervice{
		Definition: "order",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}

	t := &Traits{
		owner: sys,
		cervices: components.Cervices{
			orderCer.Definition: orderCer,
		},
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: t.cervices,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {}
}

//-------------------------------------Service handlers

// ordersHandler routes GET (page or lookup) and POST (new order).
func (t *Traits) ordersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		email := r.URL.Query().Get("email")
		if id != "" && email != "" {
			t.lookupFromTracker(w, id, email)
		} else if id != "" || email != "" {
			http.Error(w, "both id and email are required for order lookup", http.StatusBadRequest)
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, orderPage)
		}
	case http.MethodPost:
		t.submitOrder(w, r)
	default:
		http.Error(w, "Method is not supported.", http.StatusMethodNotAllowed)
	}
}

// submitOrder unpacks the incoming JSON order, forwards it to the tracker, and
// returns the confirmed record (including the assigned order number) as JSON.
func (t *Traits) submitOrder(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "reading request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	unpacked, err := usecases.Unpack(body, "application/json")
	if err != nil {
		http.Error(w, "unpacking order: "+err.Error(), http.StatusBadRequest)
		return
	}
	order, ok := unpacked.(*PenHolderOrder_v1)
	if !ok {
		http.Error(w, "expected PenHolderOrder_v1 body", http.StatusBadRequest)
		return
	}

	if order.Height <= 0 || order.Height > 21 {
		http.Error(w, "height must be between 0 and 21 mm", http.StatusBadRequest)
		return
	}
	if order.Depth < 0 || order.Depth > order.Height {
		http.Error(w, "depth must be ≥ 0 and not exceed height", http.StatusBadRequest)
		return
	}

	packed, err := usecases.Pack(order, "application/json")
	if err != nil {
		http.Error(w, "packing order: "+err.Error(), http.StatusInternalServerError)
		return
	}

	f, err := usecases.SetState(t.cervices["order"], t.owner, packed)
	if err != nil {
		log.Printf("clerk: could not reach tracker: %v\n", err)
		http.Error(w, "tracker unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	confirmed, ok := f.(*PenHolderOrder_v1)
	if !ok {
		http.Error(w, "unexpected response from tracker", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(confirmed) //nolint:errcheck
}

// lookupFromTracker discovers the tracker's order service URL, appends ?id=N&email=E,
// and proxies the response back to the browser.
func (t *Traits) lookupFromTracker(w http.ResponseWriter, id, email string) {
	cer := t.cervices["order"]

	if len(cer.Nodes) == 0 {
		if err := usecases.Search4Services(cer, t.owner); err != nil {
			http.Error(w, "could not discover order service: "+err.Error(), http.StatusBadGateway)
			return
		}
	}

	var baseURL string
	for _, nodes := range cer.Nodes {
		if len(nodes) > 0 {
			baseURL = nodes[0].URL
			break
		}
	}
	if baseURL == "" {
		http.Error(w, "order service not found", http.StatusBadGateway)
		return
	}

	targetURL := baseURL + "?id=" + url.QueryEscape(id) + "&email=" + url.QueryEscape(email)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(targetURL)
	if err != nil {
		cer.Nodes = make(map[string][]components.NodeInfo)
		http.Error(w, "tracker error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "reading tracker response: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

//-------------------------------------Embedded page

// orderPage is the complete single-page UI served to browsers.
const orderPage = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Pen Holder Orders</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: system-ui, -apple-system, sans-serif;
      background: #f0f2f5;
      color: #333;
      padding: 2rem 1rem;
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      align-items: center;
    }
    h1 {
      font-size: 1.6rem;
      color: #1a237e;
      margin-bottom: 1.75rem;
      letter-spacing: -0.02em;
      width: 100%;
      max-width: 500px;
    }
    h2 {
      font-size: 1rem;
      font-weight: 600;
      color: #444;
      margin-bottom: 1.25rem;
      padding-bottom: 0.5rem;
      border-bottom: 2px solid #e8eaf6;
    }
    .card {
      background: #fff;
      border-radius: 10px;
      padding: 1.75rem;
      margin-bottom: 1.5rem;
      box-shadow: 0 2px 8px rgba(0,0,0,0.08);
      width: 100%;
      max-width: 500px;
    }
    .field { margin-bottom: 1.1rem; }
    label {
      display: block;
      font-size: 0.85rem;
      font-weight: 600;
      color: #555;
      margin-bottom: 0.3rem;
    }
    label .note {
      font-weight: 400;
      color: #999;
      margin-left: 0.3rem;
    }
    input[type=text],
    input[type=email],
    input[type=number],
    select {
      width: 100%;
      padding: 0.55rem 0.75rem;
      border: 1.5px solid #ccc;
      border-radius: 6px;
      font-size: 0.95rem;
      transition: border-color 0.15s, box-shadow 0.15s;
    }
    input:focus, select:focus {
      outline: none;
      border-color: #5c6bc0;
      box-shadow: 0 0 0 3px rgba(92,107,192,0.15);
    }
    input.invalid { border-color: #e53935; }
    .err {
      font-size: 0.78rem;
      color: #e53935;
      min-height: 1.1rem;
      margin-top: 0.2rem;
    }
    .row { display: flex; gap: 1rem; }
    .row .field { flex: 1; }
    .btn-row { display: flex; gap: 0.75rem; margin-top: 1.5rem; }
    button {
      padding: 0.6rem 1.4rem;
      border: none;
      border-radius: 6px;
      font-size: 0.95rem;
      font-weight: 600;
      cursor: pointer;
      transition: background 0.15s, transform 0.1s;
    }
    button:active { transform: scale(0.97); }
    .btn-primary { background: #5c6bc0; color: #fff; }
    .btn-primary:hover { background: #3949ab; }
    .btn-secondary { background: #eceff1; color: #555; }
    .btn-secondary:hover { background: #dde1e4; }
    .alert {
      padding: 0.85rem 1rem;
      border-radius: 6px;
      margin-top: 1.25rem;
      font-size: 0.9rem;
      line-height: 1.5;
    }
    .alert-success {
      background: #e8f5e9;
      color: #2e7d32;
      border: 1px solid #a5d6a7;
    }
    .alert-error {
      background: #ffebee;
      color: #c62828;
      border: 1px solid #ef9a9a;
    }
    .hidden { display: none; }
    dl.detail { font-size: 0.9rem; }
    dl.detail div { display: flex; gap: 0.5rem; padding: 0.2rem 0; }
    dl.detail dt { font-weight: 600; color: #555; min-width: 130px; }
    dl.detail dd { color: #333; }
    .tag {
      display: inline-block;
      background: #e8eaf6;
      color: #3949ab;
      border-radius: 4px;
      padding: 0.1rem 0.5rem;
      font-size: 0.8rem;
      font-weight: 700;
    }
  </style>
</head>
<body>
  <h1>Pen Holder Orders</h1>

  <!-- ── New order ───────────────────────────────────────── -->
  <div class="card">
    <h2>New Order</h2>
    <form id="orderForm" novalidate>

      <div class="row">
        <div class="field">
          <label for="name">Name</label>
          <input type="text" id="name" placeholder="Full name" autocomplete="name">
          <div class="err" id="err-name"></div>
        </div>
        <div class="field">
          <label for="email">Email</label>
          <input type="email" id="email" placeholder="you@example.com" autocomplete="email">
          <div class="err" id="err-email"></div>
        </div>
      </div>

      <div class="row">
        <div class="field">
          <label for="height">Height <span class="note">mm, max 21</span></label>
          <input type="number" id="height" min="0.1" max="21" step="0.1" placeholder="e.g. 15">
          <div class="err" id="err-height"></div>
        </div>
        <div class="field">
          <label for="depth">Depth <span class="note">mm, ≤ height</span></label>
          <input type="number" id="depth" min="0" step="0.1" placeholder="e.g. 5">
          <div class="err" id="err-depth"></div>
        </div>
      </div>

      <div class="field">
        <label for="roughness">Surface Roughness</label>
        <select id="roughness">
          <option value="32">Ra max. 32 µm — Fine (grinding / finish turning)</option>
          <option value="63" selected>Ra max. 63 µm — Standard (general turning)</option>
          <option value="125">Ra max. 125 µm — Rough (heavy turning / milling)</option>
        </select>
      </div>

      <div class="field">
        <label for="line">Production Line <span class="note">optional</span></label>
        <input type="text" id="line" placeholder="e.g. LineA">
      </div>

      <div class="field">
        <label for="peppol">Peppol ID <span class="note">optional — enables e-invoice delivery</span></label>
        <input type="text" id="peppol" placeholder="e.g. 0192:987654321">
      </div>

      <div class="btn-row">
        <button type="submit" class="btn-primary">Place Order</button>
        <button type="button" class="btn-secondary" onclick="resetForm()">Clear</button>
      </div>
    </form>
    <div id="feedback" class="hidden"></div>
  </div>

  <!-- ── Look up ──────────────────────────────────────────── -->
  <div class="card">
    <h2>Look Up Order</h2>
    <div class="row">
      <div class="field">
        <label for="lookupId">Order Number</label>
        <input type="number" id="lookupId" placeholder="e.g. 42" min="1">
      </div>
      <div class="field">
        <label for="lookupEmail">Email Address</label>
        <input type="email" id="lookupEmail" placeholder="you@example.com">
      </div>
    </div>
    <div class="btn-row">
      <button class="btn-primary" onclick="lookupOrder()">Look Up Order</button>
    </div>
    <div id="lookupResult" class="hidden" style="margin-top:1rem"></div>
  </div>

  <script>
    // ── Submit new order ────────────────────────────────────
    document.getElementById('orderForm').addEventListener('submit', async function(e) {
      e.preventDefault();
      if (!validate()) return;

      const order = {
        order_number:        0,
        name:                document.getElementById('name').value.trim(),
        email:               document.getElementById('email').value.trim(),
        height:              parseFloat(document.getElementById('height').value),
        depth:               parseFloat(document.getElementById('depth').value),
        roughness:           parseInt(document.getElementById('roughness').value, 10),
        production_line:     document.getElementById('line').value.trim(),
        peppol_id:           document.getElementById('peppol').value.trim(),
        timestamp:           new Date().toISOString(),
        completed_timestamp: '0001-01-01T00:00:00Z',
        version:             'PenHolderOrder_v1'
      };

      const fb = document.getElementById('feedback');
      fb.className = 'hidden';

      try {
        const resp = await fetch(window.location.pathname, {
          method:  'POST',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify(order)
        });
        if (resp.ok) {
          const r = await resp.json();
          fb.className = 'alert alert-success';
          fb.innerHTML =
            '<strong>Order <span class="tag">#' + r.order_number + '</span> confirmed.</strong><br>' +
            'Thank you, ' + esc(r.name) + '. A confirmation will be sent to ' + esc(r.email) + '.';
          document.getElementById('orderForm').reset();
          clearErrors();
        } else {
          const msg = await resp.text();
          fb.className = 'alert alert-error';
          fb.textContent = msg || 'An error occurred while placing the order.';
        }
      } catch (err) {
        fb.className = 'alert alert-error';
        fb.textContent = 'Network error: ' + err.message;
      }
    });

    // ── Client-side validation ──────────────────────────────
    function validate() {
      let ok = true;
      function set(id, msg) {
        const el = document.getElementById('err-' + id);
        const inp = document.getElementById(id);
        el.textContent = msg;
        if (msg) { inp.classList.add('invalid'); ok = false; }
        else       inp.classList.remove('invalid');
      }
      const name  = document.getElementById('name').value.trim();
      const email = document.getElementById('email').value.trim();
      const h     = parseFloat(document.getElementById('height').value);
      const d     = parseFloat(document.getElementById('depth').value);

      set('name',   name ? '' : 'Name is required.');
      set('email',  /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ? '' : 'A valid email address is required.');
      set('height', !isNaN(h) && h > 0 && h <= 21 ? '' : 'Height must be between 0 and 21 mm.');
      set('depth',  !isNaN(d) && d >= 0 && d <= h  ? '' : 'Depth must be \u2265 0 mm and not exceed height.');
      return ok;
    }

    // Clear field error on input.
    ['name','email','height','depth'].forEach(function(id) {
      document.getElementById(id).addEventListener('input', function() {
        document.getElementById('err-' + id).textContent = '';
        this.classList.remove('invalid');
      });
    });

    // Tighten depth max when height changes.
    document.getElementById('height').addEventListener('input', function() {
      const h = parseFloat(this.value);
      const dep = document.getElementById('depth');
      if (!isNaN(h)) dep.max = h;
    });

    function clearErrors() {
      ['name','email','height','depth'].forEach(function(id) {
        document.getElementById('err-' + id).textContent = '';
        document.getElementById(id).classList.remove('invalid');
      });
    }

    function resetForm() {
      document.getElementById('orderForm').reset();
      clearErrors();
      document.getElementById('peppol').value = '';
      const fb = document.getElementById('feedback');
      fb.className = 'hidden';
      fb.textContent = '';
    }

    // ── Look up order ───────────────────────────────────────
    async function lookupOrder() {
      const id    = document.getElementById('lookupId').value.trim();
      const email = document.getElementById('lookupEmail').value.trim();
      const out   = document.getElementById('lookupResult');
      if (!id || !email) {
        out.className = 'alert alert-error';
        out.textContent = 'Both an order number and an email address are required.';
        return;
      }
      out.className = 'hidden';
      try {
        const qs   = '?id=' + encodeURIComponent(id) + '&email=' + encodeURIComponent(email);
        const resp = await fetch(window.location.pathname + qs);
        if (resp.ok) {
          const o = await resp.json();
          const completed = o.completed_timestamp && !o.completed_timestamp.startsWith('0001')
            ? fmtDate(o.completed_timestamp) : '\u2014';
          const peppolRow = o.peppol_id ? row('Peppol ID', esc(o.peppol_id)) : '';
          out.className = 'alert alert-success';
          out.innerHTML =
            '<dl class="detail">' +
            row('Order',           '<span class="tag">#' + o.order_number + '</span>') +
            row('Name',            esc(o.name)) +
            row('Email',           esc(o.email)) +
            row('Dimensions',      o.height + ' \u00d7 ' + o.depth + ' mm') +
            row('Roughness',       'Ra max. ' + o.roughness + ' \u00b5m') +
            row('Production line', esc(o.production_line) || '\u2014') +
            peppolRow +
            row('Ordered',         fmtDate(o.timestamp)) +
            row('Completed',       completed) +
            '</dl>';
        } else {
          out.className = 'alert alert-error';
          out.textContent = await resp.text();
        }
      } catch (err) {
        out.className = 'alert alert-error';
        out.textContent = 'Network error: ' + err.message;
      }
    }

    function row(label, value) {
      return '<div><dt>' + label + '</dt><dd>' + value + '</dd></div>';
    }
    function esc(s) {
      return String(s || '').replace(/[&<>"']/g, function(c) {
        return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c];
      });
    }
    function fmtDate(s) {
      try { return new Date(s).toLocaleString(); } catch(e) { return s; }
    }
  </script>
</body>
</html>`
