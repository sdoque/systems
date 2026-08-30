/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
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
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// The envoy's second shape: instead of writing what it read to disk, it holds
// the connection open and hands each read to a browser on the loopback
// interface.
//
// The capture mode answers "what did the cloud look like at 16:29"; this one
// answers "show me the cloud now, and let me click on it". Both delegate in the
// same way and for the same reason — the certificate stays with the binary, the
// person never holds one — and they are one binary because the whitelist keys on
// a hash, so two would be two entries to keep in step for one capability.
//
// Everything here is deliberately narrow. It binds the loopback interface and
// nothing else, it answers GET and nothing else, and it discovers with the
// "read" action and nothing else. A delegated credential reachable from the
// network is not a viewer, it is a way into the cloud for whoever finds the
// port; that is the boundary warning from chronicler/README.md one level down,
// and it applies here with more force because this credential is real.

// discoveryRetry is how long to wait before asking the cloud again for a
// definition nobody provides yet. Long enough not to hammer the orchestrator
// while a cloud starts, short enough that a viewer is up before anyone looks.
const discoveryRetry = 15 * time.Second

// route is one service definition the proxy publishes, together with the
// discovery result it is currently serving from.
//
// The cervice is kept rather than rediscovered per request because discovery
// costs an orchestration round trip, and replaced when a provider refuses the
// token in it. The mutex serializes that replacement: two browser requests
// arriving together on an expired token would otherwise both rediscover, and
// the second would overwrite the first's result with an equivalent one while
// the first was reading it.
type route struct {
	definition string
	mu         sync.Mutex
	cer        *components.Cervice
}

// proxy publishes one route per service definition, keyed by the last segment
// of the provider's own URL.
//
// Keying on the provider's path is what makes a page work unmodified. The
// painter's canvas asks for its data with fetch("model") — a relative URL — so
// a page served at /view resolves it to /model on this same origin, and the
// browser never learns the cloud's addresses at all. Rewriting HTML is the
// usual cost of proxying a UI, and declining to invent our own paths avoids it.
type proxy struct {
	sys    *components.System
	routes map[string]*route
	// first is the segment the root path redirects to, so opening the bare
	// address lands on something rather than a list.
	first string
}

// discover finds every provider of one service definition, as a person's
// delegate reading on their behalf.
//
// "read" explicitly: a token names the action it permits, and the provider
// recomputes that action from the HTTP method it receives.
func discover(sys *components.System, definition string) (*components.Cervice, error) {
	cer := &components.Cervice{
		Definition: definition,
		Protos:     components.SProtocols(sys.Husk.ProtoPort),
		Nodes:      make(map[string][]components.NodeInfo),
	}
	if err := usecases.Search4MultipleServicesAs(cer, sys, "read"); err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}
	if len(cer.Nodes) == 0 {
		return nil, fmt.Errorf("no provider offers this service")
	}
	return cer, nil
}

// firstNode returns one provider from a discovery result.
//
// A cervice may hold several. The capture mode reads them all, because an
// operator archiving the cloud wants every provider's answer; a viewer wants one
// page. Which one is not a decision worth making cleverly, so it is the first
// the map yields — and the count is logged, because a canvas that silently shows
// one of two clouds is worse than one that says so.
func firstNode(cer *components.Cervice) (components.NodeInfo, bool) {
	for _, nodes := range cer.Nodes {
		for _, ni := range nodes {
			return ni, true
		}
	}
	return components.NodeInfo{}, false
}

// segment is the last path element of a provider's URL — "view" out of
// https://host:30187/painter/canvas/view.
func segment(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return parts[len(parts)-1]
}

// newProxy discovers each definition once and builds the route table from what
// the providers call themselves.
//
// A definition that cannot be discovered now is reported and skipped rather than
// fatal: a cloud where the painter is down but the kgrapher is up should still
// show the kgrapher. Discovering nothing at all is an error rather than an empty
// proxy — a port answering 404 to everything looks like a bug — but serve treats
// that error as "not yet" and asks again, because in systems.txt this starts
// beside the providers it proxies.
func newProxy(sys *components.System, definitions []string) (*proxy, error) {
	p := &proxy{sys: sys, routes: make(map[string]*route)}
	for _, definition := range definitions {
		cer, err := discover(sys, definition)
		if err != nil {
			log.Printf("envoy: %s: %v", definition, err)
			continue
		}
		ni, ok := firstNode(cer)
		if !ok {
			log.Printf("envoy: %s: discovery returned no node", definition)
			continue
		}
		seg := segment(ni.URL)
		if seg == "" {
			log.Printf("envoy: %s: cannot derive a path from %s", definition, ni.URL)
			continue
		}
		if n := countNodes(cer); n > 1 {
			log.Printf("envoy: %s: %d providers offer this; serving %s", definition, n, ni.URL)
		}
		p.routes[seg] = &route{definition: definition, cer: cer}
		if p.first == "" {
			p.first = seg
		}
		log.Printf("envoy: /%s → %s (%s)", seg, ni.URL, definition)
	}
	if len(p.routes) == 0 {
		return nil, fmt.Errorf("nothing to serve: no definition could be discovered")
	}
	return p, nil
}

// countNodes reports how many providers a discovery result holds.
func countNodes(cer *components.Cervice) int {
	n := 0
	for _, nodes := range cer.Nodes {
		n += len(nodes)
	}
	return n
}

// read fetches this route's service, rediscovering once if the provider says the
// token is no longer good.
//
// The retry exists because this process now outlives its tokens. The capture
// mode ran for seconds and could not meet an expiry; a viewer left open on a
// desk meets one every token lifetime, and without this the canvas would simply
// stop refreshing and give no reason.
func (p *proxy) read(rt *route) (body []byte, contentType string, err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	ni, ok := firstNode(rt.cer)
	if ok {
		body, contentType, status, ferr := fetchStatus(ni)
		if ferr == nil {
			return body, contentType, nil
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return nil, "", ferr
		}
		log.Printf("envoy: %s: %s — rediscovering", rt.definition, http.StatusText(status))
	}

	cer, derr := discover(p.sys, rt.definition)
	if derr != nil {
		return nil, "", fmt.Errorf("%s: %w", rt.definition, derr)
	}
	rt.cer = cer
	fresh, ok := firstNode(cer)
	if !ok {
		return nil, "", fmt.Errorf("%s: discovery returned no node", rt.definition)
	}
	body, contentType, _, err = fetchStatus(fresh)
	return body, contentType, err
}

// loopback reports whether a request's Host header names this machine.
//
// Binding 127.0.0.1 stops another machine connecting, and does not stop a page
// in the operator's own browser from being pointed here by a name that resolves
// to 127.0.0.1 — DNS rebinding, where a site the operator visits makes their
// browser the attacker's client. Checking the Host header closes it: the browser
// sends the name it was given, and a name we did not expect is not us.
func loopback(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopback(r.Host) {
		http.Error(w, "the envoy answers only on the loopback interface", http.StatusForbidden)
		log.Printf("envoy: refused a request for host %q", usecases.ForLog(r.Host))
		return
	}
	// GET only, and said out loud. This binary holds a certificate the cloud
	// issued and tokens minted for reading; forwarding a method that could
	// actuate would turn a viewer into an unattested way to switch a heater.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "the envoy reads; it does not write", http.StatusMethodNotAllowed)
		return
	}

	seg := strings.Trim(r.URL.Path, "/")
	if seg == "" {
		http.Redirect(w, r, "/"+p.first, http.StatusFound)
		return
	}
	rt, ok := p.routes[seg]
	if !ok {
		http.Error(w, "no service is published on this path", http.StatusNotFound)
		return
	}

	body, contentType, err := p.read(rt)
	if err != nil {
		log.Printf("envoy: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// The canvas is redrawn from a fresh read on every request; a cached copy
	// would show a cloud that has since changed and give no sign of it.
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

// serve runs the proxy until the process is stopped.
//
// Discovery is retried rather than fatal, for the reason the certificate wait
// is: in systems.txt this starts alongside everything else, and the painter it
// proxies may not have registered yet. A viewer that exits because the cloud was
// half a second behind it is a viewer that is missing every time the cloud
// restarts — which is exactly when somebody opens it.
func serve(sys *components.System, definitions []string, port int) error {
	var p *proxy
	for attempt := 0; ; attempt++ {
		var err error
		if p, err = newProxy(sys, definitions); err == nil {
			break
		}
		if attempt == 0 {
			log.Printf("envoy: %v — retrying until a provider appears", err)
		}
		select {
		case <-time.After(discoveryRetry):
		case <-sys.Ctx.Done():
			return nil
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("envoy: serving on http://%s/ — open it in a browser on this host", addr)
	// Explicitly 127.0.0.1 rather than :port. The whole safety of this rests on
	// not being reachable from the network, and a bind address is the only part
	// of that no configuration mistake elsewhere can undo.
	return http.ListenAndServe(addr, p)
}
