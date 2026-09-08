/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	unitDegree = "<http://qudt.org/vocab/unit/DEG>"
	unitMetre  = "<http://qudt.org/vocab/unit/M>"
	unitPct    = "<http://qudt.org/vocab/unit/PERCENT>"

	qkLength = "<http://qudt.org/vocab/quantitykind/Length>"
	qkAngle  = "<http://qudt.org/vocab/quantitykind/Angle>"
)

// GuetteurConfig is what the operator may set.
type GuetteurConfig struct {
	// Source is "serial" for the sensor or "simulated" for the corridor
	// simulator. The template ships simulated so the system runs anywhere,
	// including on a laptop with no sensor attached.
	Source   string `json:"source"`
	Port     string `json:"port"`
	BaudRate int    `json:"baudRate"`

	SweepHz        float64 `json:"sweepHz"`
	PointsPerSweep int     `json:"pointsPerSweep"`
	SectorDegrees  float64 `json:"sectorDegrees"`
	MaxRange       float64 `json:"maxRangeMetres"`

	// ForwardSectorDegrees is the arc, centred straight ahead, that clearance
	// reduces to a single number.
	ForwardSectorDegrees float64 `json:"forwardSectorDegrees"`

	// StaleAfterMs is how old the newest sweep may be before this system stops
	// answering. The file-based integration on the vehicle uses 1.5 s for the
	// same purpose and for the same reason.
	StaleAfterMs int `json:"staleAfterMs"`
}

// Traits is the sensor, and what a service handler is given.
type Traits struct {
	cfg GuetteurConfig
	src source

	mu     sync.RWMutex
	latest sweep
	got    bool
	sector float64
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	scan := components.Service{
		Definition: "scan",
		SubPath:    "scan",
		Details: map[string][]string{
			"Forms":   {"ScanA_v1a"},
			"Unit":    {unitMetre},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:     2,
		SubscribeAble: true,
		Description:   "the latest complete sweep: angle, distance and whether each point was a return",
	}

	clearance := components.Service{
		Definition: "clearance",
		SubPath:    "clearance",
		Details: map[string][]string{
			"Forms":        {"SignalA_v1a"},
			"Unit":         {unitMetre},
			"QuantityKind": {qkLength},
			"Methods":      components.HTTPMethods("GET"),
		},
		RegPeriod:     2,
		SubscribeAble: true,
		Description:   "the nearest return within the forward sector; unavailable rather than optimistic when the sensor is blind",
	}

	scansector := components.Service{
		Definition: "scansector",
		SubPath:    "scansector",
		Details: map[string][]string{
			"Forms":        {"SignalA_v1a"},
			"Unit":         {unitDegree},
			"QuantityKind": {qkAngle},
			"Methods":      components.HTTPMethods("GET", "PUT"),
		},
		RegPeriod:   30,
		Description: "the width of the scanned arc, centred ahead (GET) or sets it (PUT)",
	}

	quality := components.Service{
		Definition: "quality",
		SubPath:    "quality",
		Details: map[string][]string{
			"Forms":   {"SignalA_v1a"},
			"Unit":    {unitPct},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:     5,
		SubscribeAble: true,
		Description:   "the share of the last sweep that came back as actual returns",
	}

	return &components.UnitAsset{
		Name:     "Lookout",
		Mission:  components.MissionMeasurement,
		Mobility: components.MobilityMovable,
		Details:  map[string][]string{"Model": {"LightWare SF45/B"}},
		ServicesMap: components.Services{
			scan.SubPath:       &scan,
			clearance.SubPath:  &clearance,
			scansector.SubPath: &scansector,
			quality.SubPath:    &quality,
		},
		Traits: &GuetteurConfig{
			Source:               "simulated",
			Port:                 "/dev/ttyUSB0",
			BaudRate:             921600,
			SweepHz:              5,
			PointsPerSweep:       160,
			SectorDegrees:        160,
			MaxRange:             50,
			ForwardSectorDegrees: 40,
			StaleAfterMs:         1500,
		},
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cfg := GuetteurConfig{}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("guetteur: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	src, err := newSource(cfg)
	if err != nil {
		log.Fatalf("guetteur: %v", err)
	}

	t := &Traits{cfg: cfg, src: src, sector: cfg.SectorDegrees}

	ch, err := src.sweeps(sys.Ctx)
	if err != nil {
		src.close()
		log.Fatalf("guetteur: cannot start %s source: %v", cfg.Source, err)
	}
	go t.collect(sys.Ctx, ch)

	if cfg.Source == "simulated" {
		log.Println("guetteur: running against the corridor SIMULATOR — no sensor is being read")
	} else {
		log.Printf("guetteur: reading %s at %d baud", cfg.Port, cfg.BaudRate)
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Mobility:    uac.Mobility,
		TetheredTo:  uac.TetheredTo,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	return ua, func() {
		src.close()
		log.Println("guetteur: sensor closed")
	}
}

func applyDefaults(cfg *GuetteurConfig) {
	if cfg.Source == "" {
		cfg.Source = "simulated"
	}
	if cfg.BaudRate <= 0 {
		cfg.BaudRate = 921600
	}
	if cfg.SweepHz <= 0 {
		cfg.SweepHz = 5
	}
	if cfg.PointsPerSweep < 2 {
		cfg.PointsPerSweep = 160
	}
	if cfg.SectorDegrees <= 0 {
		cfg.SectorDegrees = 160
	}
	if cfg.MaxRange <= 0 {
		cfg.MaxRange = 50
	}
	if cfg.ForwardSectorDegrees <= 0 {
		cfg.ForwardSectorDegrees = 40
	}
	if cfg.StaleAfterMs <= 0 {
		cfg.StaleAfterMs = 1500
	}
}

// collect keeps only the newest sweep. Nothing here queues: a consumer asking
// what is ahead wants what is ahead now, and a backlog of old geometry on a
// moving vehicle is worse than no answer.
func (t *Traits) collect(ctx context.Context, ch <-chan sweep) {
	for {
		select {
		case <-ctx.Done():
			return
		case sw, open := <-ch:
			if !open {
				log.Println("guetteur: the sweep source has stopped")
				return
			}
			t.mu.Lock()
			t.latest = sw
			t.got = true
			t.mu.Unlock()
		}
	}
}

// current returns the newest sweep and whether it is fresh enough to act on.
func (t *Traits) current() (sweep, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.got {
		return sweep{}, false
	}
	if time.Since(t.latest.taken) > time.Duration(t.cfg.StaleAfterMs)*time.Millisecond {
		return t.latest, false
	}
	return t.latest, true
}

//-------------------------------------The services

func (t *Traits) scanService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	sw, fresh := t.current()
	if !fresh {
		http.Error(w, "no sweep within the freshness window", http.StatusServiceUnavailable)
		return
	}
	f := forms.ScanA_v1a{}
	f.NewForm()
	f.Angles = sw.angles
	f.Distances = sw.distances
	f.Valid = sw.valid
	f.AngleUnit = unitDegree
	f.DistanceUnit = unitMetre
	f.Timestamp = sw.taken
	f.SweepDuration = sw.duration
	usecases.HTTPProcessGetRequest(w, r, &f)
}

// clearanceService answers with the nearest return in the forward sector.
//
// When the sector holds no returns at all it answers 503 and no body. That is
// the whole argument for this system: a rangefinder that sees nothing reports
// either its maximum range or an error, and if either reaches a consumer as a
// distance the vehicle drives into what it cannot see. There is no value that
// safely means "blind" — SignalA carries a float and every float is a distance
// somebody will act on — so the only honest answer is to not answer, and let
// the quality service say why.
func (t *Traits) clearanceService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	sw, fresh := t.current()
	if !fresh {
		http.Error(w, "no sweep within the freshness window", http.StatusServiceUnavailable)
		return
	}
	d, ok := nearestInSector(sw, t.cfg.ForwardSectorDegrees)
	if !ok {
		http.Error(w, "blind: no valid returns in the forward sector", http.StatusServiceUnavailable)
		return
	}
	f := forms.SignalA_v1a{}
	f.NewForm()
	f.Value = d
	f.Unit = unitMetre
	f.Timestamp = sw.taken
	usecases.HTTPProcessGetRequest(w, r, &f)
}

func (t *Traits) qualityService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.RLock()
	sw, got := t.latest, t.got
	t.mu.RUnlock()

	f := forms.SignalA_v1a{}
	f.NewForm()
	f.Unit = unitPct
	f.Timestamp = time.Now()
	if got && len(sw.valid) > 0 {
		valid := 0
		for _, ok := range sw.valid {
			if ok {
				valid++
			}
		}
		f.Value = 100 * float64(valid) / float64(len(sw.valid))
		f.Timestamp = sw.taken
	}
	// Zero rather than unavailable, deliberately: "none of the sweep came back"
	// is itself a reading, and it is the reading a consumer most needs when
	// clearance has gone quiet.
	usecases.HTTPProcessGetRequest(w, r, &f)
}

func (t *Traits) scansectorService(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		t.mu.RLock()
		width := t.sector
		t.mu.RUnlock()
		f := forms.SignalA_v1a{}
		f.NewForm()
		f.Value = width
		f.Unit = unitDegree
		f.Timestamp = time.Now()
		usecases.HTTPProcessGetRequest(w, r, &f)
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		width := math.Max(1, math.Min(sig.Value, t.cfg.SectorDegrees))
		if err := t.src.setSector(width); err != nil {
			log.Printf("guetteur: cannot set sector: %v", err)
			http.Error(w, "the sensor refused the sector", http.StatusServiceUnavailable)
			return
		}
		t.mu.Lock()
		t.sector = width
		t.mu.Unlock()

		confirmation := forms.SignalA_v1a{}
		confirmation.NewForm()
		confirmation.Value = width
		confirmation.Unit = unitDegree
		confirmation.Timestamp = time.Now()
		body, err := usecases.Pack(&confirmation, "application/json")
		if err != nil {
			log.Printf("guetteur: packing response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			log.Printf("guetteur: writing response: %v", err)
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// nearestInSector reduces a sweep to the closest thing in front of the sensor.
// It reports ok=false when the sector holds no returns, which the caller must
// not confuse with a large distance.
func nearestInSector(sw sweep, sectorDegrees float64) (float64, bool) {
	half := sectorDegrees / 2
	best := math.Inf(1)
	for i, a := range sw.angles {
		if a < -half || a > half {
			continue
		}
		if i >= len(sw.valid) || i >= len(sw.distances) || !sw.valid[i] {
			continue
		}
		if sw.distances[i] < best {
			best = sw.distances[i]
		}
	}
	if math.IsInf(best, 1) {
		return 0, false
	}
	return best, true
}
