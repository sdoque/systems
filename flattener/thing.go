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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the flattener unit asset.
type Traits struct {
	MinSetPoint float64 `json:"minSetPoint"` // °C used when price is at or above MaxPrice
	MaxSetPoint float64 `json:"maxSetPoint"` // °C used when price is at or below MinPrice
	MinPrice    float64 `json:"minPrice"`    // SEK/kWh: price floor (maps to MaxSetPoint)
	MaxPrice    float64 `json:"maxPrice"`    // SEK/kWh: price ceiling (maps to MinSetPoint)
	Region      string  `json:"region"`      // Swedish price region: SE1, SE2, SE3, SE4
	Period      int     `json:"period"`      // update interval in seconds (default 3600)

	currentSetPoint float64
	currentPrice    float64
	owner           *components.System
	ua              *components.UnitAsset
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used by the configuration step.
func initTemplate() *components.UnitAsset {
	setpointSvc := components.Service{
		Definition:  "setPoint",
		SubPath:     "setpoint",
		Details:     map[string][]string{"Unit": {"Celsius"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the currently calculated temperature setpoint (GET)",
	}

	priceSvc := components.Service{
		Definition:  "price",
		SubPath:     "price",
		Details:     map[string][]string{"Unit": {"SEK/kWh"}, "Forms": {"SignalA_v1a"}},
		RegPeriod:   30,
		Description: "provides the current electricity spot price (GET)",
	}

	return &components.UnitAsset{
		Name:    "ComfortController",
		Mission: "flatten_peak_demand",
		Details: map[string][]string{"FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			setpointSvc.SubPath: &setpointSvc,
			priceSvc.SubPath:    &priceSvc,
		},
		Traits: &Traits{
			MinSetPoint: 18.0,
			MaxSetPoint: 22.0,
			MinPrice:    0.50,
			MaxPrice:    3.00,
			Region:      "SE2",
			Period:      3600,
		},
	}
}

//-------------------------------------Instantiate unit assets based on configuration

// newResource creates the unit asset with its runtime state based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner: sys,
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	// Cervice: the thermostat's setpoint service (we PUT to it)
	sProtocols := components.SProtocols(sys.Husk.ProtoPort)
	setpointCervice := &components.Cervice{
		Definition: "setpoint",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
		Mode:       "set",
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: components.Cervices{"setpoint": setpointCervice},
		Traits:      t,
	}
	t.ua = ua
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	go t.run()

	return ua, func() {
		log.Println("Flattener shutting down")
	}
}

//-------------------------------------Core logic

// run is the main control loop: fetch price, calculate setpoint, push to thermostat.
func (t *Traits) run() {
	// Fetch and apply immediately, then tick every Period seconds.
	t.updateSetPoint()

	period := t.Period
	if period <= 0 {
		period = 3600
	}
	ticker := time.NewTicker(time.Duration(period) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.updateSetPoint()
		case <-t.owner.Ctx.Done():
			log.Println("Flattener: stopping control loop")
			return
		}
	}
}

// updateSetPoint fetches the current price, calculates the optimal setpoint,
// and pushes it to all discovered thermostat setpoint services.
func (t *Traits) updateSetPoint() {
	price, err := fetchCurrentPrice(t.Region)
	if err != nil {
		log.Printf("Flattener: could not fetch price: %v\n", err)
		return
	}
	t.currentPrice = price
	t.currentSetPoint = t.priceToSetPoint(price)
	log.Printf("Flattener: price=%.4f SEK/kWh → setpoint=%.1f °C\n", price, t.currentSetPoint)

	cer := t.ua.CervicesMap["setpoint"]

	// Rediscover setpoint services on every run so that heaters which come online
	// after Flattener starts (or are added later) are picked up automatically.
	cer.Nodes = make(map[string][]components.NodeInfo)
	if err := usecases.Search4MultipleServices(cer, t.owner); err != nil {
		log.Printf("Flattener: could not discover setpoint services: %v\n", err)
		return
	}
	if len(cer.Nodes) == 0 {
		log.Println("Flattener: no setpoint services found — nothing to push")
		return
	}
	total := 0
	for _, nodes := range cer.Nodes {
		total += len(nodes)
	}
	log.Printf("Flattener: discovered %d setpoint service(s)\n", total)

	// Build the setpoint form.
	now := time.Now()
	sp := forms.SignalA_v1a{
		Value:     t.currentSetPoint,
		Unit:      "Celsius",
		Timestamp: now,
	}
	sp.NewForm()

	body, err := usecases.Pack(&sp, "application/json")
	if err != nil {
		log.Printf("Flattener: could not pack setpoint form: %v\n", err)
		return
	}

	// Push to every discovered setpoint service.
	for sysNode, nodes := range cer.Nodes {
		for _, ni := range nodes {
			singleCer := &components.Cervice{
				Definition: cer.Definition,
				Protos:     cer.Protos,
				Nodes:      map[string][]components.NodeInfo{sysNode: {ni}},
			}
			if _, err := usecases.SetState(singleCer, t.owner, body); err != nil {
				log.Printf("Flattener: could not push setpoint to %s (%s): %v\n", sysNode, ni.URL, err)
			}
			log.Printf("Flattener: pushed %.1f °C to %s\n", t.currentSetPoint, ni.URL)
		}
	}
}

// priceToSetPoint maps a price (SEK/kWh) to a temperature setpoint (°C) using
// a linear inverse relationship: high price → low setpoint, low price → high setpoint.
func (t *Traits) priceToSetPoint(price float64) float64 {
	if t.MaxPrice <= t.MinPrice {
		return t.MaxSetPoint
	}
	ratio := (price - t.MinPrice) / (t.MaxPrice - t.MinPrice)
	ratio = math.Max(0, math.Min(1, ratio)) // clamp to [0, 1]
	sp := t.MaxSetPoint - ratio*(t.MaxSetPoint-t.MinSetPoint)
	return math.Round(sp*10) / 10 // round to 0.1 °C
}

//-------------------------------------Price fetching

// hourlyPrice is one entry from the elprisetjustnu.se API response.
type hourlyPrice struct {
	SEKPerKWh float64 `json:"SEK_per_kWh"`
	TimeStart string  `json:"time_start"`
}

// fetchCurrentPrice retrieves the spot price for the current hour from elprisetjustnu.se.
func fetchCurrentPrice(region string) (float64, error) {
	now := time.Now()
	url := fmt.Sprintf(
		"https://www.elprisetjustnu.se/api/v1/prices/%d/%02d-%02d_%s.json",
		now.Year(), now.Month(), now.Day(), region,
	)

	resp, err := http.Get(url) // #nosec G107 — URL is constructed from config, not user input
	if err != nil {
		return 0, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %s from price API", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading response: %w", err)
	}

	var prices []hourlyPrice
	if err := json.Unmarshal(body, &prices); err != nil {
		return 0, fmt.Errorf("parsing prices: %w", err)
	}

	currentHour := now.Hour()
	for _, p := range prices {
		t, err := time.Parse(time.RFC3339, p.TimeStart)
		if err != nil {
			continue
		}
		if t.Hour() == currentHour {
			return p.SEKPerKWh, nil
		}
	}

	return 0, fmt.Errorf("no price entry found for hour %d in region %s", currentHour, region)
}

//-------------------------------------Service handlers

// getSetPoint returns the currently calculated setpoint as a SignalA_v1a form.
func (t *Traits) getSetPoint() forms.SignalA_v1a {
	f := forms.SignalA_v1a{
		Value:     t.currentSetPoint,
		Unit:      "Celsius",
		Timestamp: time.Now(),
	}
	f.NewForm()
	return f
}

// getPrice returns the current electricity spot price as a SignalA_v1a form.
func (t *Traits) getPrice() forms.SignalA_v1a {
	f := forms.SignalA_v1a{
		Value:     t.currentPrice,
		Unit:      "SEK/kWh",
		Timestamp: time.Now(),
	}
	f.NewForm()
	return f
}
