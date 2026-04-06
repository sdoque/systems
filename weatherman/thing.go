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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	serial "go.bug.st/serial"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Station configuration

// StationConfig holds the serial connection parameters for the Davis console.
// The port is the only field the user must set; the others have sensible defaults.
type StationConfig struct {
	Port     string `json:"port"`     // serial device, e.g. "/dev/ttyUSB0" on Linux
	BaudRate int    `json:"baudRate"` // default 19200 (Davis factory default)
	Period   int    `json:"period"`   // polling interval in seconds; default 60
}

// -------------------------------------Measurement cache

// CachedMeasurement holds one measurement value with its timestamp.
type CachedMeasurement struct {
	Value     float64
	Timestamp time.Time
}

// StationCache is a thread-safe measurement store keyed by asset name and service subpath.
type StationCache struct {
	mu   sync.RWMutex
	data map[string]map[string]CachedMeasurement // assetName → subPath → value
}

func newStationCache() *StationCache {
	return &StationCache{data: make(map[string]map[string]CachedMeasurement)}
}

func (c *StationCache) update(assetName string, measurements map[string]float64, ts time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data[assetName] == nil {
		c.data[assetName] = make(map[string]CachedMeasurement)
	}
	for k, v := range measurements {
		c.data[assetName][k] = CachedMeasurement{Value: v, Timestamp: ts}
	}
}

func (c *StationCache) get(assetName, subPath string) *CachedMeasurement {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m, ok := c.data[assetName]; ok {
		if v, ok := m[subPath]; ok {
			return &v
		}
	}
	return nil
}

// -------------------------------------Unit asset Traits

// Traits is the runtime state for a module unit asset.
type Traits struct {
	assetName string
	cache     *StationCache
}

// -------------------------------------Template

// initTemplate returns the template asset that seeds systemconfig.json on first run.
func initTemplate() *components.UnitAsset {
	return &components.UnitAsset{
		Name:        "VantagePro2",
		Mission:     "provide_weather_data",
		Details:     map[string][]string{},
		ServicesMap: components.Services{},
		Traits: &StationConfig{
			Port:     "/dev/ttyUSB0",
			BaudRate: 19200,
			Period:   60,
		},
	}
}

// -------------------------------------Asset instantiation entry point

// newResources parses the station config, reads the first LOOP packet to discover
// which optional sensors are installed, builds one UnitAsset per module, and starts
// the background polling goroutine.
func newResources(uac usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	if len(uac.Traits) == 0 {
		log.Fatal("weatherman: no station configuration found in systemconfig.json")
	}
	var cfg StationConfig
	if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
		log.Fatalf("weatherman: unmarshal station config: %v", err)
	}
	if cfg.BaudRate == 0 {
		cfg.BaudRate = 19200
	}
	if cfg.Period == 0 {
		cfg.Period = 60
	}

	log.Printf("weatherman: connecting to Davis console on %s at %d baud\n", cfg.Port, cfg.BaudRate)

	reading, err := readLOOP(cfg.Port, cfg.BaudRate)
	if err != nil {
		log.Fatalf("weatherman: could not read from Davis console: %v\n", err)
	}

	// ConsoleModule and ISSModule are always present.
	// SolarModule is created only when UV or solar radiation data is available.
	modules := []moduleInfo{consoleModuleInfo, issModuleInfo}
	if reading.HasUV || reading.HasSolar {
		modules = append(modules, solarModuleInfo)
		log.Println("weatherman: solar/UV sensor detected, registering SolarModule")
	}

	cache := newStationCache()
	ts := time.Now()

	var assets []*components.UnitAsset
	for _, info := range modules {
		cache.update(info.assetName, extractModuleMeasurements(info.assetName, reading), ts)
		assets = append(assets, newModuleAsset(info, uac.Name, sys, cache))
		log.Printf("weatherman: registered asset %q\n", info.assetName)
	}

	go pollStation(sys.Ctx, cfg, cache, modules)

	return assets, func() {
		log.Println("weatherman: disconnecting from Davis Vantage Pro2")
	}
}

// newModuleAsset creates a UnitAsset for one Davis module.
// stationName (the configured asset name) is stored as FunctionalLocation.
func newModuleAsset(info moduleInfo, stationName string, sys *components.System, cache *StationCache) *components.UnitAsset {
	t := &Traits{assetName: info.assetName, cache: cache}

	services := make(components.Services)
	for _, spec := range info.services {
		s := &components.Service{
			Definition:  spec.definition,
			SubPath:     spec.subPath,
			Details:     map[string][]string{"Unit": {spec.unit}, "Forms": {"SignalA_v1a"}},
			RegPeriod:   30,
			Description: spec.description,
		}
		services[spec.subPath] = s
	}

	ua := &components.UnitAsset{
		Name:    info.assetName,
		Mission: "provide_weather_data",
		Owner:   sys,
		Details: map[string][]string{
			"FunctionalLocation": {stationName},
		},
		ServicesMap: services,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua
}

// -------------------------------------Background poller

// pollStation reads the Davis console at each tick and refreshes the cache.
func pollStation(ctx context.Context, cfg StationConfig, cache *StationCache, modules []moduleInfo) {
	ticker := time.NewTicker(time.Duration(cfg.Period) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reading, err := readLOOP(cfg.Port, cfg.BaudRate)
			if err != nil {
				log.Printf("weatherman: poll error: %v\n", err)
				continue
			}
			ts := time.Now()
			for _, info := range modules {
				cache.update(info.assetName, extractModuleMeasurements(info.assetName, reading), ts)
			}
			log.Println("weatherman: data refreshed")
		}
	}
}

// -------------------------------------Davis serial protocol

// readLOOP opens the serial port, wakes the console, sends the LOOP 1 command,
// reads the 99-byte packet, verifies the CRC, and returns parsed measurements.
// The port is opened and closed on each call so disconnects are handled gracefully.
func readLOOP(portName string, baudRate int) (*LOOPReading, error) {
	mode := &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	port, err := serial.Open(portName, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", portName, err)
	}
	defer port.Close()

	if err := wakeConsole(port); err != nil {
		return nil, fmt.Errorf("wake console: %w", err)
	}

	// Extend timeout for data transfer.
	port.SetReadTimeout(5 * time.Second)

	if _, err := port.Write([]byte("LOOP 1\n")); err != nil {
		return nil, fmt.Errorf("send LOOP command: %w", err)
	}

	// The console responds with an ACK (0x06) followed by the 99-byte packet.
	ack := make([]byte, 1)
	if _, err := io.ReadFull(port, ack); err != nil {
		return nil, fmt.Errorf("read ACK: %w", err)
	}
	if ack[0] != 0x06 {
		return nil, fmt.Errorf("expected ACK (0x06), got 0x%02x", ack[0])
	}

	packet := make([]byte, loopPacketSize)
	if _, err := io.ReadFull(port, packet); err != nil {
		return nil, fmt.Errorf("read LOOP packet: %w", err)
	}

	return parseLOOP(packet)
}

// wakeConsole sends newline characters and waits for the console to acknowledge.
// The Davis console may be in sleep mode; it wakes within ~1.2 seconds of a "\n".
func wakeConsole(port serial.Port) error {
	port.SetReadTimeout(1500 * time.Millisecond)
	buf := make([]byte, 2)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := port.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write wake: %w", err)
		}
		n, _ := port.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' || b == '\r' {
				return nil
			}
		}
	}
	return fmt.Errorf("Davis console did not respond to wake-up after 3 attempts")
}
