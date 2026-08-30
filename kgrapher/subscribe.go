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

	// silenceLimit is how long the stream may say nothing at all before it is
	// treated as dead.
	//
	// The registry writes a keep-alive comment every 20 seconds, so silence past
	// this is not a quiet cloud — it is a connection that no longer exists. Left
	// to itself the read blocks forever: followOnce never returns, follow never
	// reconnects, and the graph goes on describing a cloud it stopped watching,
	// without a line in the log to say so. That is the one failure mode "it
	// never gives up" did not cover.
	//
	// Three keep-alives, so a missed one or a slow moment does not cost a
	// reconnection.
	silenceLimit = 65 * time.Second
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
	attempt := 0
	for {
		if err := t.followOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("kgrapher: the registry subscription ended (%v); reconnecting shortly\n", err)
		} else {
			attempt = 0 // a connection that worked starts the backoff again
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(attempt)):
		}
		attempt++
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

	// alive carries "the connection said something", which includes the registry's
	// keep-alive comments. Separate from events because a comment is not a change
	// and must not start a rebuild, but it is exactly what tells us the stream is
	// still there.
	alive := make(chan struct{}, 1)

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		// The snapshot is the whole system list on one line, so this bounds how
		// large a cloud this subscriber can follow. Past the limit the scanner
		// fails with bufio.ErrTooLong, followOnce returns and follow retries for
		// ever — an error that does not obviously mean "your cloud outgrew a
		// buffer". 8 MB is thousands of systems.
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		var kind string
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case alive <- struct{}{}:
			default: // already flagged; the watchdog only needs to know it is alive
			}
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

	limit := t.silenceFor
	if limit == 0 {
		limit = silenceLimit
	}
	silence := time.NewTimer(limit)
	defer silence.Stop()

	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-alive:
			if !silence.Stop() {
				select {
				case <-silence.C:
				default:
				}
			}
			silence.Reset(limit)
		case <-silence.C:
			// Nothing at all, not even a keep-alive. Returning hands this to
			// follow, which reconnects — and a reconnection re-reads the whole
			// registry, so nothing missed during the silence stays missed.
			if settle != nil {
				t.settled()
			}
			return fmt.Errorf("the registry said nothing for %s, not even a keep-alive", limit)
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

// incompleteRetry is how long to wait before assembling again after a pass that
// could not read every system. A variable so a test does not have to wait.
var incompleteRetry = 30 * time.Second

// settleDelay is how long after a change to look once more.
//
// A registration is an event and a binding is not. A consumer registers, the
// graph is assembled, and only then does it discover its providers — so the
// picture taken at the event shows a controller that wants a temperature and
// is bound to nothing, and no later event corrects it, because once the cloud
// settles there are none. On the first two-host deployment a thermostat that
// was reading a sensor on the other host showed two unmet wants for as long as
// anyone looked. One more pass, after the consumers have had time to bind, is
// what makes the canvas answer "is this right?" about a cloud that has just
// come up.
var settleDelay = 60 * time.Second

// rebuild assembles the graph and publishes it.
//
// A failure is logged and left: the next change will try again, and the graph
// already served stays as it was rather than being replaced by nothing.
//
// A pass that reads only *some* systems is the more dangerous case, because it
// succeeds. /kgraph requires an enrolled caller, and a system is registered
// under its plain-HTTP URL for the seconds between registering and its
// certificate arriving — so a rebuild triggered in that window silently omits
// it. Rebuilds happen on registry changes, and once the cloud settles there are
// none, so the omission would stand until something else moved: the authorizer
// went missing from the cottage's graph for ten minutes this way, while this
// function reported that the graph described the cloud.
func (t *Traits) rebuild() {
	t.rebuildThen(true)
}

// rebuildThen assembles and publishes; when settle is set and the pass was
// complete, it arranges one more pass after settleDelay. The settling pass
// itself does not, so a quiet cloud is assembled twice per change and not
// forever.
func (t *Traits) rebuildThen(settle bool) {
	assemble := t.assembleOntologies
	if t.assembling != nil {
		assemble = t.assembling
	}
	graph, skipped, err := assemble()
	if err != nil {
		log.Printf("kgrapher: could not assemble the graph: %v\n", err)
		return
	}
	t.store(graph)
	t.publishToStore(graph)

	if skipped > 0 {
		log.Printf("kgrapher: the graph is missing %d system(s) that could not be read; assembling again in %v\n",
			skipped, incompleteRetry)
		t.retryIncomplete()
		return
	}
	log.Printf("kgrapher: the graph now describes the cloud as of this change\n")
	if settle {
		t.settleLater()
	}
}

// settleLater schedules the one follow-up pass, and only one at a time: a
// burst of registrations at startup is one settling, not one per system.
func (t *Traits) settleLater() {
	if !t.settlePending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer t.settlePending.Store(false)
		ctx := context.Background()
		if t.owner != nil && t.owner.Ctx != nil {
			ctx = t.owner.Ctx
		}
		select {
		case <-time.After(settleDelay):
		case <-ctx.Done():
			return
		}
		log.Printf("kgrapher: looking once more, now that consumers have had %v to bind\n", settleDelay)
		t.rebuildThen(false)
	}()
}

// retryIncomplete schedules one more assembly, and only one: a retry that is
// still incomplete schedules its own, so without the guard each round would
// multiply.
func (t *Traits) retryIncomplete() {
	if !t.retryPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer t.retryPending.Store(false)
		ctx := t.owner.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-time.After(incompleteRetry):
		case <-ctx.Done():
			return
		}
		t.rebuild()
	}()
}

// backoff is how long to wait before the next attempt, growing with consecutive
// failures and jittered.
//
// A fixed delay meant every subscriber in the cloud reconnected in lockstep
// after a registrar restart, arriving together at the moment it is least able to
// answer. Growing it means a registrar that is down for a while is not asked
// every five seconds by everyone for the duration.
func backoff(attempt int) time.Duration {
	wait := retryAfter << min(attempt, 4) // 5s, 10s, 20s, 40s, 80s
	// Jitter is what actually breaks the lockstep; the growth only bounds the
	// cost. Derived from the attempt and the clock rather than a random source,
	// which keeps this testable and needs no seeding.
	jitter := time.Duration(time.Now().UnixNano()%int64(wait/2)) - wait/4
	return wait + jitter
}
