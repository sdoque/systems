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

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// Traits are the painter's asset-specific parameters.
type Traits struct {
	// Period is how often the cloud is walked again, in seconds.
	Period int `json:"samplingPeriod"`

	owner *components.System
	name  string

	// registry is where the list of systems is read from, discovered like any
	// other consumed service so the read carries a token in an authorized cloud.
	registry *components.Cervice

	// picture is the last cloud drawn, swapped whole.
	//
	// Read by every browser asking for the model and written by the walk, so it
	// is replaced rather than edited: a page that arrived mid-walk would
	// otherwise be handed a cloud with half its systems missing and no way to
	// tell that from a cloud that had lost them.
	picture atomic.Pointer[Cloud]

	// walking is a test seam, so the loop can be driven without a cloud behind it.
	walking func()
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	view := components.Service{
		Definition:  "view",
		SubPath:     "view",
		Details:     map[string][]string{"Forms": {"text/html"}},
		RegPeriod:   30,
		Description: "a page showing the local cloud, its hosts, systems and the services connecting them (GET)",
	}
	model := components.Service{
		Definition:  "cloudpicture",
		SubPath:     "model",
		Details:     map[string][]string{"Forms": {"application/json"}},
		RegPeriod:   30,
		Description: "the cloud as the page draws it: hosts, systems, unit assets, their security posture and the services they consume from each other (GET)",
	}

	return &components.UnitAsset{
		Name: "canvas",
		// The painter reads what every system says about itself and shows it
		// together. It computes nothing, keeps nothing and drives nothing.
		Mission: components.MissionAggregation,
		Details: map[string][]string{},
		ServicesMap: components.Services{
			view.SubPath:  &view,
			model.SubPath: &model,
		},
		Traits: &Traits{Period: 15},
	}
}

//-------------------------------------Instantiate the unit asset

// newResource creates the unit asset with its pointers and channels based on
// the configuration.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{owner: sys, name: uac.Name}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], t); err != nil {
			log.Println("painter: could not unmarshal traits:", err)
		}
	}
	if t.Period <= 0 {
		t.Period = 15
	}
	t.registry = usecases.SystemListCervice(sys)

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	go t.keepLooking(sys.Ctx)

	return ua, func() {
		log.Println("painter: putting the brushes down")
	}
}

//-------------------------------------The walk

// keepLooking redraws the cloud at intervals for as long as the system runs.
//
// Polling rather than subscribing, which is the opposite of the choice the
// kgrapher made and deliberate. The kgrapher rebuilds because something
// changed and the graph must be correct; the painter redraws because somebody
// is watching, and a picture that is a few seconds behind is not wrong in a way
// an operator can be misled by. Polling also means the painter needs nothing
// from the registrar beyond the list it already reads, so it works in a cloud
// whose registrar has no subscription to offer.
func (t *Traits) keepLooking(ctx context.Context) {
	t.walk()

	ticker := time.NewTicker(time.Duration(t.Period) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.walk()
		}
	}
}

// walk asks every system what it is, and puts the answers side by side.
func (t *Traits) walk() {
	if t.walking != nil {
		t.walking()
		return
	}

	systems, err := usecases.SystemList(t.registry, t.owner)
	if err != nil {
		log.Printf("painter: could not read the list of systems (%v); keeping the last picture\n", err)
		return
	}

	graphs := make(map[string]string, len(systems))
	var unreachable []string
	for _, systemURL := range systems {
		graph, err := fetchGraph(systemURL)
		if err != nil {
			// One system that will not answer is not a reason to lose the rest
			// of the cloud, and its absence is itself worth showing.
			unreachable = append(unreachable, usecases.ForLog(systemURL))
			continue
		}
		graphs[systemURL] = graph
	}

	cloud := build(t.cloudName(), graphs)
	for _, url := range unreachable {
		cloud.Notes = append(cloud.Notes, "no answer from "+url)
	}
	t.picture.Store(cloud)
}

// cloudName is what the local cloud calls itself, taken from this system's own
// configuration — every system in a cloud is configured with the same one.
func (t *Traits) cloudName() string {
	if t.owner != nil && t.owner.Husk != nil {
		if names := t.owner.Husk.Details["LocalCloud"]; len(names) > 0 {
			return names[0]
		}
	}
	return "local cloud"
}

// fetchGraph asks one system to describe itself.
func fetchGraph(systemURL string) (string, error) {
	resp, err := http.DefaultClient.Get(systemURL + "/kgraph")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpError{status: resp.Status}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type httpError struct{ status string }

func (e *httpError) Error() string { return e.status }

//-------------------------------------Service handlers

// current returns the picture to serve, drawing one first if the loop has not
// finished its first walk.
func (t *Traits) current() *Cloud {
	if cloud := t.picture.Load(); cloud != nil {
		return cloud
	}
	return &Cloud{Name: t.cloudName(), Notes: []string{"still looking at the cloud"}}
}

// model serves the picture as JSON, which is what the page redraws from.
func (t *Traits) model(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(t.current()); err != nil {
		log.Printf("painter: writing the model: %v\n", err)
	}
}

// view serves the page itself.
func (t *Traits) view(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not supported", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, pageHTML); err != nil {
		log.Printf("painter: writing the page: %v\n", err)
	}
}
