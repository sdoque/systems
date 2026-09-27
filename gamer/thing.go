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
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	unitMPerS   = "<http://qudt.org/vocab/unit/M-PER-SEC>"
	unitPerM    = "<http://qudt.org/vocab/unit/PER-M>"
	unitPercent = "<http://qudt.org/vocab/unit/PERCENT>"
)

// How the left stick steers.
const (
	// steerByEffort pushes the waist motor directly, as a percentage of its
	// effort. It is how the vehicle is steered before the waist sensor is
	// calibrated, and the loader's guard keeps it inside its limits.
	steerByEffort = "effort"
	// steerByCurvature asks for a curvature and lets the loader's angle loop
	// steer the waist to it. It needs a calibrated waist.
	steerByCurvature = "curvature"
)

// PadConfig is what the operator may set. The defaults are for a PlayStation
// pad on Linux, whose drivers (hid-sony, hid-playstation) number the controls
// as used here; an Xbox pad under xpad numbers them the same way.
type PadConfig struct {
	Device    string `json:"device"`
	CommandHz int    `json:"commandHz"`

	// MaxSpeed is full stick, in m/s of the front axle.
	MaxSpeed float64 `json:"maxSpeedMetresPerSecond"`

	// Steering is "effort" or "curvature"; see steerByEffort. Effort until
	// the waist is calibrated, curvature after.
	Steering string `json:"steering"`
	// MaxSteeringPercent is full stick when steering by effort: "push this
	// hard", with the operator closing the loop by eye.
	MaxSteeringPercent float64 `json:"maxSteeringPercent"`
	// MaxCurvature is full stick when steering by curvature, in 1/m. The
	// loader clamps it to what the waist can do.
	MaxCurvature float64 `json:"maxCurvature"`
	// SteeringNodeID picks the loader's steering motor, when steering by
	// effort.
	SteeringNodeID int `json:"steeringNodeID"`

	DeadZone float64 `json:"deadZone"`

	SpeedAxis int `json:"speedAxis"` // right stick, vertical
	SteerAxis int `json:"steerAxis"` // left stick, horizontal

	// StopButtons stop the vehicle; any one of them is enough.
	StopButtons []int `json:"stopButtons"`
	// StopChord stops the vehicle when all of it is pressed.
	StopChord []int `json:"stopChord"`
	// TakeChord, held for HoldSeconds, asks for control; ReleaseChord, held
	// as long, gives it back.
	TakeChord    []int   `json:"takeChord"`
	ReleaseChord []int   `json:"releaseChord"`
	HoldSeconds  float64 `json:"holdSeconds"`

	// StopRepeats is how many cycles a stop is still sent after the buttons
	// are let go. One would do if every request arrived; a few cover the one
	// that does not.
	StopRepeats int `json:"stopRepeats"`

	// Vehicle picks the loader out of the cloud.
	Vehicle map[string][]string `json:"vehicle"`
}

// Traits is the gamepad asset's state.
type Traits struct {
	cfg   PadConfig
	pad   *pad
	owner *components.System

	velocity *sender
	steer    *sender
	control  *sender
	stop     *sender
	replies  chan reply

	mu        sync.Mutex
	inControl bool
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	engaged := components.Service{
		Definition: "engaged",
		SubPath:    "engaged",
		Details: map[string][]string{
			"Forms":   {"SignalA_v1a"},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   30,
		Description: "1 while this pad has control of the vehicle, 0 otherwise (GET)",
	}

	return &components.UnitAsset{
		Name:     "Gamepad",
		Mission:  components.MissionControl,
		Details:  map[string][]string{"Model": {"gamepad"}},
		Mobility: components.MobilityFixed, // bound to the input device on this host
		ServicesMap: components.Services{
			engaged.SubPath: &engaged,
		},
		Traits: &PadConfig{
			Device:             "/dev/input/js0",
			CommandHz:          20,
			MaxSpeed:           1.0,
			Steering:           steerByEffort,
			MaxSteeringPercent: 50,
			MaxCurvature:       0.5,
			SteeringNodeID:     5,
			DeadZone:           0.10,
			SpeedAxis:          4,
			SteerAxis:          0,
			StopButtons:        []int{0, 1, 2, 3}, // cross, circle, triangle, square
			StopChord:          []int{4, 5, 6, 7}, // L1 R1 L2 R2
			TakeChord:          []int{4, 5},       // L1 R1
			ReleaseChord:       []int{6, 7},       // L2 R2
			HoldSeconds:        5,
			StopRepeats:        5,
			Vehicle:            map[string][]string{"Model": {"artitrax"}},
		},
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cfg := PadConfig{}
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], &cfg); err != nil {
			log.Fatalf("gamer: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	t := &Traits{
		cfg:     cfg,
		pad:     &pad{path: cfg.Device},
		owner:   sys,
		replies: make(chan reply, 16),
	}

	protos := components.SProtocols(sys.Husk.ProtoPort)
	cervices := make(components.Cervices)
	velocityCer := setCervice("velocity", "velocity", cfg.Vehicle, nil, protos)
	t.velocity = newSender("velocity", velocityCer, sys, unitMPerS, t.replies, commandReply)
	var steerCer *components.Cervice
	if cfg.Steering == steerByCurvature {
		steerCer = setCervice("curvature", "steer", cfg.Vehicle, nil, protos)
		t.steer = newSender("curvature", steerCer, sys, unitPerM, t.replies, commandReply)
	} else {
		steerCer = setCervice("setpoint", "steer", cfg.Vehicle,
			map[string][]string{"NodeID": {strconv.Itoa(cfg.SteeringNodeID)}}, protos)
		t.steer = newSender("steering effort", steerCer, sys, unitPercent, t.replies, commandReply)
	}
	controlCer := setCervice("control", "control", cfg.Vehicle, nil, protos)
	stopCer := setCervice("stop", "stop", cfg.Vehicle, nil, protos)
	cervices["velocity"], cervices["steer"] = velocityCer, steerCer
	cervices["control"], cervices["stop"] = controlCer, stopCer
	t.control = newSender("control", controlCer, sys, "", t.replies, controlReply)
	t.stop = newSender("stop", stopCer, sys, "", t.replies, stopReply)
	log.Printf("gamer: steering by %s; full stick is %s", cfg.Steering, t.fullSteer())

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Mobility:    configuredAsset.Mobility,
		TetheredTo:  configuredAsset.TetheredTo,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		CervicesMap: cervices,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	for _, s := range t.allSenders() {
		go s.run(sys.Ctx)
	}
	go t.pad.run(sys.Ctx)
	go t.run(sys.Ctx)

	return ua, func() {
		log.Println("gamer: shut down")
	}
}

func applyDefaults(cfg *PadConfig) {
	if cfg.Device == "" {
		cfg.Device = "/dev/input/js0"
	}
	if cfg.CommandHz <= 0 {
		cfg.CommandHz = 20
	}
	if cfg.MaxSpeed <= 0 {
		cfg.MaxSpeed = 1.0
	}
	if cfg.Steering != steerByCurvature {
		cfg.Steering = steerByEffort
	}
	if cfg.MaxSteeringPercent <= 0 {
		cfg.MaxSteeringPercent = 50
	}
	if cfg.MaxCurvature <= 0 {
		cfg.MaxCurvature = 0.5
	}
	if cfg.SteeringNodeID <= 0 {
		cfg.SteeringNodeID = 5
	}
	if cfg.DeadZone <= 0 || cfg.DeadZone >= 1 {
		cfg.DeadZone = 0.10
	}
	if len(cfg.StopButtons) == 0 && len(cfg.StopChord) == 0 {
		// A pad with no way to stop the vehicle is not one to start.
		cfg.StopButtons = []int{0, 1, 2, 3}
		cfg.StopChord = []int{4, 5, 6, 7}
	}
	if len(cfg.TakeChord) == 0 {
		cfg.TakeChord = []int{4, 5}
	}
	if len(cfg.ReleaseChord) == 0 {
		cfg.ReleaseChord = []int{6, 7}
	}
	if cfg.HoldSeconds <= 0 {
		cfg.HoldSeconds = 5
	}
	if cfg.StopRepeats <= 0 {
		cfg.StopRepeats = 5
	}
}

// setCervice is the quest for one of the loader's services. The vehicle's
// details pick the loader; the extra ones pick a motor within it.
func setCervice(definition, ref string, vehicle, extra map[string][]string, protos []string) *components.Cervice {
	details := make(map[string][]string)
	for k, v := range vehicle {
		details[k] = v
	}
	for k, v := range extra {
		details[k] = v
	}
	return &components.Cervice{
		IReferentce: ref,
		Definition:  definition,
		Protos:      protos,
		Nodes:       make(map[string][]components.NodeInfo),
		Mode:        "set",
		Details:     details,
	}
}

func (t *Traits) newPilot() *pilot {
	return &pilot{
		stopButtons:  t.cfg.StopButtons,
		stopChord:    t.cfg.StopChord,
		takeChord:    t.cfg.TakeChord,
		releaseChord: t.cfg.ReleaseChord,
		holdFor:      time.Duration(t.cfg.HoldSeconds * float64(time.Second)),
		repeats:      t.cfg.StopRepeats,
		deadZone:     t.cfg.DeadZone,
		speedAxis:    t.cfg.SpeedAxis,
		steerAxis:    t.cfg.SteerAxis,
	}
}

func (t *Traits) allSenders() []*sender {
	return []*sender{t.velocity, t.steer, t.control, t.stop}
}

func (t *Traits) fullSteer() string {
	if t.cfg.Steering == steerByCurvature {
		return fmt.Sprintf("a curvature of %.2f /m (a %.1f m radius)", t.cfg.MaxCurvature, 1/t.cfg.MaxCurvature)
	}
	return fmt.Sprintf("%.0f%% of the waist motor's effort", t.cfg.MaxSteeringPercent)
}

//-------------------------------------The control loop

func (t *Traits) run(ctx context.Context) {
	p := t.newPilot()
	tick := time.NewTicker(time.Second / time.Duration(t.cfg.CommandHz))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.shutdown(p)
			return
		case r := <-t.replies:
			applyReply(p, r)
			continue
		case <-tick.C:
		}

		a := p.step(t.pad.snapshot(), time.Now())

		t.mu.Lock()
		t.inControl = p.inControl
		t.mu.Unlock()

		if a.stop {
			t.stop.offer(1, a.epoch)
		}
		if a.take {
			t.control.offer(1, a.epoch)
		}
		if a.release {
			t.control.offer(0, a.epoch)
		}
		if a.drive {
			t.velocity.offer(a.speed*t.cfg.MaxSpeed, a.epoch)
			t.steer.offer(t.steerValue(a.steer), a.epoch)
		}
	}
}

// steerValue turns the stick, a fraction positive to the left, into what the
// loader is sent.
func (t *Traits) steerValue(fraction float64) float64 {
	if t.cfg.Steering == steerByCurvature {
		return fraction * t.cfg.MaxCurvature
	}
	return fraction * t.cfg.MaxSteeringPercent
}

// shutdown stops the vehicle if this pad was driving it. Closing the program
// that has control is a pilot leaving the controls; closing one that does not
// have control says nothing about whoever is driving, and is left alone.
//
// Here rather than in the cleanup, which runs only after the system's shutdown
// pause: stopping the program is a stop, now.
func (t *Traits) shutdown(p *pilot) {
	if !p.inControl {
		return
	}
	done := make(chan error, 1)
	go func() { done <- t.stop.put(1) }()
	select {
	case err := <-done:
		if err != nil {
			log.Printf("gamer: had control on the way out, and the vehicle did not confirm the stop: %v", err)
			return
		}
		log.Println("gamer: had control on the way out — the vehicle is stopped")
	case <-time.After(time.Second):
		log.Println("gamer: had control on the way out, and the vehicle did not confirm the stop within a second")
	}
}

//-------------------------------------Replies

type replyKind int

const (
	commandReply replyKind = iota
	controlReply
	stopReply
)

// reply is what the loader said to one request, and the epoch it was made in.
type reply struct {
	kind  replyKind
	value float64 // what was sent
	epoch int
	err   error
}

func applyReply(p *pilot, r reply) {
	var refused *usecases.ProviderRefusal
	isRefusal := errors.As(r.err, &refused) && refused.StatusCode == http.StatusConflict
	switch r.kind {
	case commandReply:
		// 409 is the loader saying this pad no longer has control: someone
		// stopped the vehicle or took over. Anything else — a 503 because the
		// waist is not calibrated, say — is not about control, and the sender
		// has already logged it.
		if isRefusal {
			p.refused(r.epoch, refused.Detail)
		}
	case controlReply:
		switch {
		case r.err == nil && r.value != 0:
			p.granted(r.epoch)
		case r.err == nil:
			p.released(r.epoch)
		case isRefusal:
			log.Printf("gamer: the vehicle refused: %s", refused.Detail)
		}
	}
	// Anything else — an unreachable loader, a stop that failed — the sender
	// has already logged. The pilot's belief does not change on a failure to
	// talk: a pad that believes it has control keeps sending, and the loader's
	// own silence limit is what stops a vehicle nobody can reach.
}

//-------------------------------------One sender per service

// sender delivers one service's commands. It holds only the newest value: if a
// request is still in flight when the next cycle comes, the older pending value
// is replaced rather than queued, so a slow network produces a vehicle that
// follows the stick late, never one that replays where the stick used to be.
type sender struct {
	name    string
	cer     *components.Cervice
	sys     *components.System
	unit    string
	kind    replyKind
	replies chan<- reply
	next    chan job

	lastErr time.Time
}

type job struct {
	value float64
	epoch int
}

func newSender(name string, cer *components.Cervice, sys *components.System, unit string, replies chan<- reply, kind replyKind) *sender {
	return &sender{name: name, cer: cer, sys: sys, unit: unit, kind: kind, replies: replies, next: make(chan job, 1)}
}

func (s *sender) offer(v float64, epoch int) {
	j := job{value: v, epoch: epoch}
	for {
		select {
		case s.next <- j:
			return
		default:
		}
		select {
		case <-s.next:
		default:
		}
	}
}

func (s *sender) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.next:
			err := s.put(j.value)
			if err != nil && time.Since(s.lastErr) > 5*time.Second {
				// At twenty requests a second an unreachable loader would fill
				// the journal; once every five seconds per service still says
				// it is happening.
				log.Printf("gamer: %s: %v", s.name, err)
				s.lastErr = time.Now()
			}
			select {
			case s.replies <- reply{kind: s.kind, value: j.value, epoch: j.epoch, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *sender) put(v float64) error {
	f := forms.SignalA_v1a{}
	f.NewForm()
	f.Value = v
	f.Unit = s.unit
	f.Timestamp = time.Now()
	body, err := usecases.Pack(&f, "application/json")
	if err != nil {
		return err
	}
	_, err = usecases.SetState(s.cer, s.sys, body)
	return err
}

//-------------------------------------Service handlers

func (t *Traits) engaged(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.Lock()
	inControl := t.inControl
	t.mu.Unlock()

	f := forms.SignalA_v1a{}
	f.NewForm()
	if inControl {
		f.Value = 1
	}
	f.Timestamp = time.Now()
	usecases.HTTPProcessGetRequest(w, r, &f)
}
