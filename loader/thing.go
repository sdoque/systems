/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// The motors are driven by Magellan motion-control ICs reached over CAN at
// 0x600 + node ID. These opcodes and the initialization order below are taken
// from the artitrax can_dds bridge, which is the tested reference; the delays
// are its delays and are not decoration — a motor that is not given them does
// not come up.
const (
	motorBase             = 0x600
	cmdUpdate             = 0x1A
	cmdReset              = 0x39
	cmdSetCurrentFoldback = 0x41
	cmdSetOperatingMode   = 0x65
	cmdSetVelocity        = 0x77
)

// rpmToCommand converts a wheel speed to the controller's own integer scale:
// 120 RPM is 0x7800, so one RPM is 256 counts. fullScale is the clamp that
// keeps a command inside int16 and inside what the drive will accept.
const (
	rpmToCommand = 0x7800 / 120.0 // 256 counts per RPM
	fullScale    = 0x7800         // 30720
)

// LoaderConfig is what the operator may set. Every field has a working default
// so the generated file needs no editing to drive a motor.
type LoaderConfig struct {
	Interface    string      `json:"canInterface"`
	CommandHz    int         `json:"commandHz"`
	SafetyStopMs int         `json:"safetyStopMs"`
	MaxWheelRPM  float64     `json:"maxWheelRPM"`
	AccelStep    int         `json:"accelStep"`
	BrakeStep    int         `json:"brakeStep"`
	Motors       []MotorSpec `json:"motors"`
}

// MotorSpec names one motor on the bus. Kind decides how a setpoint is read:
// a wheel is commanded in RPM, the steering in percent of full effort, because
// the steering motor takes an effort and not an angle — reaching an angle needs
// a loop closed against the can1 sensor, which this system does not yet have.
type MotorSpec struct {
	Name   string `json:"name"`
	NodeID int    `json:"nodeID"`
	Kind   string `json:"kind"` // "wheel" or "steering"
}

// drivetrain is the state the five assets share: one CAN socket, one setpoint
// per motor, and the loop that keeps writing them.
type drivetrain struct {
	cfg LoaderConfig
	fd  int

	mu        sync.Mutex
	setpoint  map[int]float64 // node ID -> RPM (wheel) or percent (steering)
	last      map[int]int16   // node ID -> last raw command, for rate limiting
	commanded time.Time       // when a client last set anything
}

// Traits is one motor, and what a service handler is given.
type Traits struct {
	Name   string
	NodeID int
	Kind   string
	unit   string
	dt     *drivetrain
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	setpoint := components.Service{
		Definition: "setpoint",
		SubPath:    "setpoint",
		Details: map[string][]string{
			"Forms":   {"SignalA_v1a"},
			"Unit":    {"<http://qudt.org/vocab/unit/REV-PER-MIN>"},
			"Methods": components.HTTPMethods("GET", "PUT"),
		},
		RegPeriod:   30,
		Description: "reports the speed this motor is being commanded to (GET) or commands it (PUT)",
	}

	return &components.UnitAsset{
		Name:     "Drivetrain",
		Mission:  components.MissionActuation,
		Mobility: components.MobilityMovable,
		Details:  map[string][]string{"Model": {"artitrax"}, "FunctionalLocation": {"Loader"}},
		ServicesMap: components.Services{
			setpoint.SubPath: &setpoint,
		},
		Traits: &LoaderConfig{
			Interface: "can0",
			// Above ten, because can_dds — and the hardware behind it — treats a
			// command older than 100 ms as stale and zeroes the outputs. Twenty
			// leaves margin for a missed cycle.
			CommandHz: 20,
			// The vehicle stops if nothing has commanded it for this long. Every
			// other actuator in this cloud holds its last state when the
			// controller goes quiet, which is right for a heater and wrong for
			// something with wheels.
			SafetyStopMs: 2000,
			MaxWheelRPM:  120,
			AccelStep:    30,
			BrakeStep:    100,
			Motors: []MotorSpec{
				{Name: "FrontLeft", NodeID: 1, Kind: "wheel"},
				{Name: "FrontRight", NodeID: 2, Kind: "wheel"},
				{Name: "BackLeft", NodeID: 3, Kind: "wheel"},
				{Name: "BackRight", NodeID: 4, Kind: "wheel"},
				{Name: "Steering", NodeID: 5, Kind: "steering"},
			},
		},
	}
}

//-------------------------------------Instantiate the unit assets

// newResource turns the one configured asset into one unit asset per motor, the
// same expansion the busdriver does for signals: the operator describes the
// vehicle once and gets five addressable motors.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) ([]*components.UnitAsset, func()) {
	cfg := LoaderConfig{}
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], &cfg); err != nil {
			log.Fatalf("loader: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	fd, err := openCAN(cfg.Interface)
	if err != nil {
		log.Fatalf("loader: cannot open %s: %v", cfg.Interface, err)
	}
	log.Printf("loader: opened %s", cfg.Interface)

	dt := &drivetrain{
		cfg:      cfg,
		fd:       fd,
		setpoint: make(map[int]float64),
		last:     make(map[int]int16),
	}

	if err := dt.initMotors(); err != nil {
		closeCAN(fd)
		log.Fatalf("loader: motor initialization failed: %v", err)
	}

	var assets []*components.UnitAsset
	for _, m := range cfg.Motors {
		t := &Traits{Name: m.Name, NodeID: m.NodeID, Kind: m.Kind, dt: dt}
		t.unit = "<http://qudt.org/vocab/unit/REV-PER-MIN>"
		if m.Kind == "steering" {
			t.unit = "<http://qudt.org/vocab/unit/PERCENT>"
		}

		details := make(map[string][]string)
		for k, v := range configuredAsset.Details {
			details[k] = v
		}
		details["Unit"] = []string{t.unit}
		details["NodeID"] = []string{fmt.Sprintf("%d", m.NodeID)}

		ua := &components.UnitAsset{
			Name:        m.Name,
			Mission:     configuredAsset.Mission,
			Mobility:    configuredAsset.Mobility,
			TetheredTo:  configuredAsset.TetheredTo,
			Owner:       sys,
			Details:     details,
			ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
			Traits:      t,
		}
		ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
			serving(t, w, r, servicePath)
		}
		assets = append(assets, ua)
	}

	go dt.run(sys.Ctx)

	return assets, func() {
		dt.stopAll()
		closeCAN(fd)
		log.Println("loader: motors stopped and CAN closed")
	}
}

func applyDefaults(cfg *LoaderConfig) {
	if cfg.Interface == "" {
		cfg.Interface = "can0"
	}
	if cfg.CommandHz <= 0 {
		cfg.CommandHz = 20
	}
	if cfg.SafetyStopMs <= 0 {
		cfg.SafetyStopMs = 2000
	}
	if cfg.MaxWheelRPM <= 0 {
		cfg.MaxWheelRPM = 120
	}
	if cfg.AccelStep <= 0 {
		cfg.AccelStep = 30
	}
	if cfg.BrakeStep <= 0 {
		cfg.BrakeStep = 100
	}
}

//-------------------------------------The CAN conversation

// initMotors runs the three-step bring-up on every motor. The delays are the
// reference implementation's and the motors need them.
func (d *drivetrain) initMotors() error {
	for _, m := range d.cfg.Motors {
		id := uint32(motorBase + m.NodeID)
		if err := sendCAN(d.fd, id, []byte{0x00, cmdReset}); err != nil {
			return fmt.Errorf("reset node %d: %w", m.NodeID, err)
		}
		time.Sleep(500 * time.Millisecond)
		if err := sendCAN(d.fd, id, []byte{0x00, cmdSetCurrentFoldback, 0x00, 0x00, 0x98, 0x8F}); err != nil {
			return fmt.Errorf("current foldback node %d: %w", m.NodeID, err)
		}
		time.Sleep(500 * time.Millisecond)
		if err := sendCAN(d.fd, id, []byte{0x00, cmdSetOperatingMode, 0x00, 0x03}); err != nil {
			return fmt.Errorf("operating mode node %d: %w", m.NodeID, err)
		}
		time.Sleep(50 * time.Millisecond)
		log.Printf("loader: %s (node %d) initialized", m.Name, m.NodeID)
	}
	return nil
}

// setVelocity writes one raw command and tells the drive to act on it.
func (d *drivetrain) setVelocity(nodeID int, raw int16) error {
	id := uint32(motorBase + nodeID)
	u := uint16(raw)
	if err := sendCAN(d.fd, id, []byte{0x00, cmdSetVelocity, byte(u >> 8), byte(u)}); err != nil {
		return err
	}
	return sendCAN(d.fd, id, []byte{0x00, cmdUpdate})
}

// run keeps the motors fed. Nothing here reads a request: the services move a
// setpoint, and this loop is what puts it on the wire, often enough that the
// drives never see a stale command.
func (d *drivetrain) run(ctx context.Context) {
	tick := time.NewTicker(time.Second / time.Duration(d.cfg.CommandHz))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-tick.C:
			d.writeCycle()
		}
	}
}

// writeCycle sends every motor its rate-limited command, or zero if no client
// has said anything recently.
func (d *drivetrain) writeCycle() {
	d.mu.Lock()
	stale := !d.commanded.IsZero() &&
		time.Since(d.commanded) > time.Duration(d.cfg.SafetyStopMs)*time.Millisecond
	targets := make(map[int]int16, len(d.cfg.Motors))
	for _, m := range d.cfg.Motors {
		desired := int16(0)
		if !stale {
			desired = d.rawFor(m)
		}
		limited := rateLimit(d.last[m.NodeID], desired,
			int16(d.cfg.AccelStep), int16(d.cfg.BrakeStep))
		d.last[m.NodeID] = limited
		targets[m.NodeID] = limited
	}
	if stale {
		// Announced once: commanded is cleared so the next cycle is quiet.
		log.Printf("loader: no command for %d ms — stopping", d.cfg.SafetyStopMs)
		d.commanded = time.Time{}
		for k := range d.setpoint {
			d.setpoint[k] = 0
		}
	}
	fd := d.fd
	d.mu.Unlock()

	if fd == 0 {
		return
	}
	for node, raw := range targets {
		if err := d.setVelocity(node, raw); err != nil {
			log.Printf("loader: node %d: %v", node, err)
		}
	}
}

// rawFor converts a motor's setpoint into the controller's integer scale.
// Caller holds the lock.
func (d *drivetrain) rawFor(m MotorSpec) int16 {
	sp := d.setpoint[m.NodeID]
	var raw float64
	if m.Kind == "steering" {
		raw = sp / 100.0 * fullScale // percent of full effort
	} else {
		raw = sp * rpmToCommand
	}
	return clampToScale(raw)
}

func clampToScale(v float64) int16 {
	if math.IsNaN(v) {
		return 0
	}
	if v > fullScale {
		v = fullScale
	}
	if v < -fullScale {
		v = -fullScale
	}
	return int16(v)
}

// rateLimit walks the command towards what was asked for, no faster than the
// configured step. Braking gets its own, larger step: coming to a stop should
// not be slower than setting off.
func rateLimit(last, desired, accel, brake int16) int16 {
	signsDiffer := (desired > 0 && last < 0) || (desired < 0 && last > 0)
	towardsZero := (last > 0 && desired < last && desired >= 0) ||
		(last < 0 && desired > last && desired <= 0)
	step := int32(accel)
	if signsDiffer || towardsZero {
		step = int32(brake)
	}
	l, dv := int32(last), int32(desired)
	if dv > l+step {
		return int16(l + step)
	}
	if dv < l-step {
		return int16(l - step)
	}
	return desired
}

// stopAll zeroes every motor immediately, bypassing the rate limiter. Used on
// shutdown, where the point is that the wheels stop.
func (d *drivetrain) stopAll() {
	d.mu.Lock()
	for k := range d.setpoint {
		d.setpoint[k] = 0
	}
	for k := range d.last {
		d.last[k] = 0
	}
	motors := d.cfg.Motors
	fd := d.fd
	d.mu.Unlock()
	if fd == 0 {
		return
	}
	for _, m := range motors {
		if err := d.setVelocity(m.NodeID, 0); err != nil {
			log.Printf("loader: stopping node %d: %v", m.NodeID, err)
		}
	}
}

//-------------------------------------Service handlers

func (t *Traits) setpointService(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		f := t.get()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case "PUT":
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			log.Printf("loader: %s: bad set request: %v", t.Name, err)
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		confirmation := t.set(sig)
		body, err := usecases.Pack(&confirmation, "application/json")
		if err != nil {
			log.Printf("loader: packing response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			log.Printf("loader: writing response: %v", err)
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) get() (f forms.SignalA_v1a) {
	f.NewForm()
	t.dt.mu.Lock()
	f.Value = t.dt.setpoint[t.NodeID]
	t.dt.mu.Unlock()
	f.Unit = t.unit
	f.Timestamp = time.Now()
	return f
}

// set records what was asked for and clamps it. The value reaches a motor on
// the next cycle of the loop, not here, so a request never blocks on the bus.
func (t *Traits) set(sig forms.SignalA_v1a) forms.SignalA_v1a {
	v := sig.Value
	limit := t.dt.cfg.MaxWheelRPM
	if t.Kind == "steering" {
		limit = 100
	}
	if v > limit {
		v = limit
	}
	if v < -limit {
		v = -limit
	}

	t.dt.mu.Lock()
	t.dt.setpoint[t.NodeID] = v
	t.dt.commanded = time.Now()
	t.dt.mu.Unlock()

	if v != sig.Value {
		log.Printf("loader: %s asked for %.2f, clamped to %.2f", t.Name, sig.Value, v)
	}
	return t.get()
}
