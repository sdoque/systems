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
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------- Define the unit asset

// Traits holds the runtime state for the beehive dashboard unit asset.
type Traits struct {
	Period int `json:"period"` // state poll interval in seconds (default: 10)

	mu       sync.RWMutex
	switches []SwitchInfo
	owner    *components.System
}

// SwitchInfo describes one discovered OnOff service endpoint.
type SwitchInfo struct {
	Name   string `json:"name"`   // human-readable device name (derived from service URL)
	URL    string `json:"url"`    // full HTTP URL of the on_off service
	State  bool   `json:"state"`  // last known on/off state
	Online bool   `json:"online"` // false when the last state fetch failed
}

// ------------------------------------- Instantiate a unit asset template

// initTemplate returns the template UnitAsset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	return &components.UnitAsset{
		Name:    "BeehiveDashboard",
		Mission: "web_dashboard",
		Details: map[string][]string{},
		ServicesMap: components.Services{
			"dashboard": &components.Service{
				Definition:  "Dashboard",
				SubPath:     "dashboard",
				Details:     map[string][]string{"Forms": {"text/html"}},
				RegPeriod:   30,
				Description: "HTML dashboard with on/off toggle switches for ZigBee devices (GET)",
			},
		},
		Traits: &Traits{Period: 10},
	}
}

// ------------------------------------- Instantiate a unit asset based on configuration

// newResource creates the runtime unit asset from the configuration file and
// registers the extra HTTP endpoints used by the dashboard's JavaScript.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{owner: sys}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("beehive: could not unmarshal traits:", err)
		}
	}
	if t.Period <= 0 {
		t.Period = 10
	}

	// Attempt an initial service discovery and state fetch.
	t.discoverAndPoll()

	go t.backgroundPoll()

	// Register extra HTTP endpoints used by the dashboard's JavaScript.
	http.HandleFunc("/beehive/api/state", func(w http.ResponseWriter, r *http.Request) {
		stateHandler(t, w, r)
	})
	http.HandleFunc("/beehive/api/toggle", func(w http.ResponseWriter, r *http.Request) {
		toggleHandler(t, w, r, sys.Ctx)
	})

	ua := &components.UnitAsset{
		Name:    "BeehiveDashboard",
		Mission: "web_dashboard",
		Owner:   sys,
		Details: map[string][]string{},
		ServicesMap: components.Services{
			"dashboard": &components.Service{
				Definition:  "Dashboard",
				SubPath:     "dashboard",
				Details:     map[string][]string{"Forms": {"text/html"}},
				RegPeriod:   30,
				Description: "HTML dashboard with on/off toggle switches for ZigBee devices (GET)",
			},
		},
		Traits: t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua, func() {}
}

// ------------------------------------- Background work

// discoverAndPoll asks the Orchestrator for all OnOff services and fetches their states.
func (t *Traits) discoverAndPoll() {
	cer := &components.Cervice{
		Definition: "OnOff",
		Protos:     []string{"http"},
		Nodes:      make(map[string][]components.NodeInfo),
	}

	if err := usecases.Search4MultipleServices(cer, t.owner); err != nil {
		log.Printf("beehive: service discovery failed: %v\n", err)
		return
	}

	var newSwitches []SwitchInfo
	for _, nodes := range cer.Nodes {
		for _, ni := range nodes {
			state, online := fetchOnOffState(ni.URL)
			newSwitches = append(newSwitches, SwitchInfo{
				Name:   nameFromURL(ni.URL),
				URL:    ni.URL,
				State:  state,
				Online: online,
			})
		}
	}

	sort.Slice(newSwitches, func(i, j int) bool {
		return newSwitches[i].Name < newSwitches[j].Name
	})

	t.mu.Lock()
	t.switches = newSwitches
	t.mu.Unlock()

	log.Printf("beehive: %d OnOff service(s) discovered\n", len(newSwitches))
}

// backgroundPoll periodically re-discovers services and refreshes switch states.
// When no services are known yet (e.g. beehive started before beekeeper) it retries
// every 3 seconds so the dashboard populates quickly once beekeeper registers.
// Once services are found it settles into the configured Period interval.
func (t *Traits) backgroundPoll() {
	for {
		t.mu.RLock()
		hasServices := len(t.switches) > 0
		t.mu.RUnlock()

		interval := time.Duration(t.Period) * time.Second
		if !hasServices {
			interval = 3 * time.Second
		}

		select {
		case <-t.owner.Ctx.Done():
			return
		case <-time.After(interval):
			t.discoverAndPoll()
		}
	}
}

// ------------------------------------- Helpers

// fetchOnOffState retrieves the current boolean value from one on_off service URL.
func fetchOnOffState(url string) (state bool, online bool) {
	// Preserve framework-installed TLS so this works against HTTPS-only peers.
	client := &http.Client{Timeout: 3 * time.Second, Transport: http.DefaultClient.Transport}
	resp, err := client.Get(url)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, false
	}
	f, err := usecases.Unpack(body, resp.Header.Get("Content-Type"))
	if err != nil {
		return false, false
	}
	sig, ok := f.(*forms.SignalB_v1a)
	if !ok {
		return false, false
	}
	return sig.Value, true
}

// nameFromURL extracts a human-readable device name from an Arrowhead service URL.
// URL format: http://host:port/systemname/assetname/servicepath
// Returns the assetname segment with underscores replaced by spaces.
func nameFromURL(rawURL string) string {
	s := rawURL
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	// s is now "systemname/assetname/servicepath"
	parts := strings.SplitN(s, "/", 3)
	if len(parts) >= 2 {
		return strings.ReplaceAll(parts[1], "_", " ")
	}
	return rawURL
}

// stateHandler serves GET /beehive/api/state — returns the current switch list as JSON.
func stateHandler(t *Traits, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	t.mu.RLock()
	list := make([]SwitchInfo, len(t.switches))
	copy(list, t.switches)
	t.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// toggleHandler serves PUT /beehive/api/toggle?name=<device>&value=<true|false>.
// It proxies a SignalB_v1a PUT to the discovered on_off service URL.
func toggleHandler(t *Traits, w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	valueStr := r.URL.Query().Get("value")

	t.mu.RLock()
	var targetURL string
	for _, sw := range t.switches {
		if sw.Name == name {
			targetURL = sw.URL
			break
		}
	}
	t.mu.RUnlock()

	if targetURL == "" {
		http.Error(w, "unknown device: "+name, http.StatusNotFound)
		return
	}

	newState := valueStr == "true"
	var sig forms.SignalB_v1a
	sig.NewForm()
	sig.Value = newState
	sig.Timestamp = time.Now()

	body, err := usecases.Pack(&sig, "application/json")
	if err != nil {
		http.Error(w, "pack error", http.StatusInternalServerError)
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "request error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Preserve framework-installed TLS so this works against HTTPS-only peers.
	client := &http.Client{Timeout: 5 * time.Second, Transport: http.DefaultClient.Transport}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "upstream returned "+resp.Status, http.StatusBadGateway)
		return
	}

	// Re-fetch the confirmed state from beekeeper rather than applying an optimistic
	// update. This keeps beehive accurate even when beekeeper restarts mid-session.
	actualState, online := fetchOnOffState(targetURL)
	t.mu.Lock()
	for i, sw := range t.switches {
		if sw.Name == name {
			t.switches[i].State = actualState
			t.switches[i].Online = online
			break
		}
	}
	t.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------- Dashboard HTML

// dashboardHTML is the complete single-page dashboard served at /beehive/BeehiveDashboard/dashboard.
// It uses plain HTML/CSS/JS with no external dependencies.
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Beehive — Device Dashboard</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #1a1a2e;
    color: #e0e0e0;
    min-height: 100vh;
    padding: 2rem;
  }
  h1 {
    text-align: center;
    font-size: 1.8rem;
    margin-bottom: 0.4rem;
    color: #f0c040;
    letter-spacing: 0.05em;
  }
  .subtitle {
    text-align: center;
    color: #888;
    font-size: 0.85rem;
    margin-bottom: 2rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 1.2rem;
    max-width: 1000px;
    margin: 0 auto;
  }
  .card {
    background: #16213e;
    border-radius: 12px;
    padding: 1.2rem 1rem;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.8rem;
    border: 1px solid #0f3460;
    transition: box-shadow 0.2s;
  }
  .card:hover { box-shadow: 0 0 12px rgba(240,192,64,0.15); }
  .card.offline { opacity: 0.45; }
  .device-name {
    font-size: 0.95rem;
    font-weight: 600;
    text-align: center;
    word-break: break-word;
    color: #c8d8e4;
  }
  .switch { position: relative; width: 56px; height: 28px; }
  .switch input { opacity: 0; width: 0; height: 0; }
  .slider {
    position: absolute; inset: 0;
    background: #334;
    border-radius: 28px;
    cursor: pointer;
    transition: background 0.25s;
  }
  .slider::before {
    content: "";
    position: absolute;
    height: 22px; width: 22px;
    left: 3px; bottom: 3px;
    background: #aaa;
    border-radius: 50%;
    transition: transform 0.25s, background 0.25s;
  }
  input:checked + .slider { background: #f0a500; }
  input:checked + .slider::before { transform: translateX(28px); background: #fff; }
  .state-label { font-size: 0.75rem; color: #888; }
  #status-bar {
    text-align: center;
    color: #555;
    font-size: 0.78rem;
    margin-top: 2rem;
  }
  .empty {
    grid-column: 1/-1;
    text-align: center;
    color: #555;
    padding: 3rem;
  }
</style>
</head>
<body>
<h1>&#x1F41D; Beehive</h1>
<p class="subtitle">ZigBee device controls</p>
<div class="grid" id="grid"><div class="empty">Discovering devices&hellip;</div></div>
<div id="status-bar">last update: &mdash;</div>
<script>
const grid = document.getElementById('grid');
const statusBar = document.getElementById('status-bar');

async function fetchState() {
  try {
    const resp = await fetch('/beehive/api/state');
    if (!resp.ok) return;
    const devices = await resp.json();
    renderGrid(devices);
    statusBar.textContent = 'last update: ' + new Date().toLocaleTimeString();
  } catch(e) {
    statusBar.textContent = 'error: ' + e.message;
  }
}

function renderGrid(devices) {
  if (!devices || devices.length === 0) {
    grid.innerHTML = '<div class="empty">No OnOff services found in the local cloud.</div>';
    return;
  }
  const pending = new Set(Array.from(grid.querySelectorAll('input[data-pending]')).map(el => el.dataset.name));
  grid.innerHTML = '';
  devices.forEach(dev => {
    const card = document.createElement('div');
    card.className = 'card' + (dev.online ? '' : ' offline');

    const label = document.createElement('div');
    label.className = 'device-name';
    label.textContent = dev.name;

    const swLabel = document.createElement('label');
    swLabel.className = 'switch';

    const input = document.createElement('input');
    input.type = 'checkbox';
    input.checked = dev.state;
    input.dataset.name = dev.name;
    if (pending.has(dev.name)) input.dataset.pending = '1';
    input.addEventListener('change', () => toggle(dev.name, input.checked, input));

    const slider = document.createElement('div');
    slider.className = 'slider';

    swLabel.appendChild(input);
    swLabel.appendChild(slider);

    const stateLabel = document.createElement('div');
    stateLabel.className = 'state-label';
    stateLabel.textContent = dev.online ? (dev.state ? 'ON' : 'OFF') : 'offline';

    card.appendChild(label);
    card.appendChild(swLabel);
    card.appendChild(stateLabel);
    grid.appendChild(card);
  });
}

async function toggle(name, value, inputEl) {
  inputEl.dataset.pending = '1';
  try {
    const resp = await fetch(
      '/beehive/api/toggle?name=' + encodeURIComponent(name) + '&value=' + value,
      { method: 'PUT' }
    );
    if (!resp.ok) inputEl.checked = !value;
  } catch(e) {
    inputEl.checked = !value;
  }
  delete inputEl.dataset.pending;
  fetchState();
}

fetchState();
setInterval(fetchState, 10000);
</script>
</body>
</html>`
