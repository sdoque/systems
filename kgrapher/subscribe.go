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
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

// Following the service registry, so the graph describes the cloud as it is
// rather than as it was when somebody last asked.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	// settleFor is how long the subscriber waits for the registry to go quiet
	// before rebuilding.
	//
	// A system starting up registers its services one at a time, so an arrival
	// is several events a few milliseconds apart, and each one would otherwise
	// cost a full rebuild: one request to the registrar and one to every system
	// in the cloud. Waiting for quiet turns a burst into a single rebuild, at
	// the price of a graph that lags a change by this much.
	settleFor = 2 * time.Second

	// retryAfter is how long to wait before opening the stream again. Server-sent
	// events reconnect on any interruption, and a registrar that is restarting
	// should not be hammered while it does.
	retryAfter = 5 * time.Second
)

// follow keeps the graph current by watching the service registry.
//
// The registry reports a service registering or deregistering — a system
// arriving, departing, or gaining a unit asset — and each of those changes what
// the graph should say. Rebuilding on a change rather than on a request is what
// lets the graph be read as often as anyone likes: a dashboard polling every few
// seconds used to trigger one call to the registrar and one to every system in
// the cloud, every time, and an upload to the triple store with it.
//
// It never gives up. A registry that cannot be reached is a reason to wait and
// try again, not to stop following: the cloud outlives any one registrar, and a
// grapher that quietly stopped watching would keep serving a graph that looked
// current.
func (t *Traits) follow(ctx context.Context) {
	for {
		if err := t.followOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("kgrapher: the registry subscription ended (%v); reconnecting in %s\n", err, retryAfter)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryAfter):
		}
	}
}

// followOnce opens one subscription and reads it until it ends.
func (t *Traits) followOnce(ctx context.Context) error {
	registrar, err := components.GetRunningCoreSystemURL(t.owner, components.ServiceRegistrarName)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registrar+"/syslist", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	// The stream is a continuous read of the registry, so it carries the token
	// for a read like any other call. GetState builds the request itself and
	// cannot hold a connection open, so both the discovery and the token are
	// done here.
	//
	// Discovered on each attempt rather than once at startup: a token outlives
	// neither the authorizer that minted it nor this connection, and a
	// reconnection is exactly when the old one is most likely to have expired.
	//
	// A cloud with no authorizer mints nothing, and discovery there either
	// returns a node without a token or fails outright. Neither is a reason not
	// to subscribe — the registry will say whether it wants one.
	if err := usecases.Search4ServicesAs(t.registry, t.owner, "read"); err != nil {
		log.Printf("kgrapher: no token for the registry subscription (%v); subscribing without one\n", err)
	}
	if token, ok := t.registryToken(); ok && token != "" {
		req.Header.Set(usecases.TokenHeader, token)
	}

	client := &http.Client{Transport: http.DefaultClient.Transport} // no timeout: the point is to stay open
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the registry refused the subscription: %s: %s",
			resp.Status, strings.TrimSpace(string(reason)))
	}
	log.Printf("kgrapher: following the service registry at %s\n", registrar)

	return t.read(ctx, resp)
}

// read consumes the event stream, rebuilding once the registry goes quiet.
//
// The registry sends a snapshot when the connection opens and an event for each
// change after it. Both mean the same thing here — the cloud may not be what the
// graph says — so both start the timer rather than a rebuild.
func (t *Traits) read(ctx context.Context, resp *http.Response) error {
	events := make(chan string)
	done := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var kind string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				describe(kind, strings.TrimPrefix(line, "data: "))
				select {
				case events <- kind:
				case <-ctx.Done():
					done <- ctx.Err()
					return
				}
			}
		}
		done <- scanner.Err()
	}()

	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if settle != nil {
				// Events arrived and the connection dropped before the registry
				// went quiet. The rebuild is owed either way, and doing it on
				// the way out rather than leaving it to the reconnect keeps the
				// graph from lagging by the retry delay as well as the settle.
				t.settled()
			}
			return err
		case <-events:
			// Restarted on every event, so a burst of registrations rebuilds
			// once, after the last of them.
			settle = time.After(settleFor)
		case <-settle:
			settle = nil
			t.settled()
		}
	}
}

// settled is what happens when the registry goes quiet. A seam rather than a
// direct call, so a test can count rebuilds without a registrar, a cloud or a
// triple store behind it.
func (t *Traits) settled() {
	if t.rebuilding != nil {
		t.rebuilding()
		return
	}
	t.rebuild()
}

// describe logs what a change was, so an operator can see why the graph moved.
func describe(kind, data string) {
	if kind != "change" {
		return
	}
	var event forms.RegistryEvent_v1
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return
	}
	log.Printf("kgrapher: %s %s from %s\n", event.Record.ServiceDefinition, event.Change, event.Record.SystemName)
}

// rebuild assembles the graph and publishes it.
//
// A failure is logged and left: the next change will try again, and the graph
// already served stays as it was rather than being replaced by nothing.
func (t *Traits) rebuild() {
	graph, err := t.assembleOntologies()
	if err != nil {
		log.Printf("kgrapher: could not assemble the graph: %v\n", err)
		return
	}
	t.store(graph)
	t.publishToStore(graph)
	log.Printf("kgrapher: the graph now describes the cloud as of this change\n")
}
