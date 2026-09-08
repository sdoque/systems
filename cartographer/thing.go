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
	unitMetre  = "<http://qudt.org/vocab/unit/M>"
	unitDegree = "<http://qudt.org/vocab/unit/DEG>"
	unitPct    = "<http://qudt.org/vocab/unit/PERCENT>"
)

// CartographerConfig is what the operator may set.
type CartographerConfig struct {
	// The map's extent in metres and how fine its cells are. A hallway needs
	// little area and reasonable resolution; 5 cm is enough to see a doorway.
	WidthMetres  float64 `json:"widthMetres"`
	HeightMetres float64 `json:"heightMetres"`
	Resolution   float64 `json:"resolutionMetres"`

	// PollMs is how often a sweep is asked for.
	PollMs int `json:"pollMs"`

	// MatchRangeMetres is how far out a return is trusted for working out where
	// the vehicle is. Returns beyond it still go into the map.
	MatchRangeMetres float64 `json:"matchRangeMetres"`

	// MinTravel and MinTurn are how far the vehicle must have moved before a
	// sweep is folded into the map. Integrating every sweep from a standing
	// vehicle burns the same evidence into the same cells until they saturate,
	// which makes the map look confident about a view it only ever had once.
	MinTravelMetres  float64 `json:"minTravelMetres"`
	MinTurnDegrees   float64 `json:"minTurnDegrees"`
	MinValidFraction float64 `json:"minValidFraction"`

	// Where the map picture is written, and how often. This is what a person
	// looks at; the map service is what a system consumes.
	PGMPath      string `json:"pgmPath"`
	PGMIntervalS int    `json:"pgmIntervalSeconds"`

	Search SearchConfig `json:"search"`
}

// Traits holds the map and everything that changes it.
type Traits struct {
	cfg   CartographerConfig
	owner *components.System

	scanCervice *components.Cervice
	poseCervice *components.Cervice

	mu sync.RWMutex
	g  *grid
	at pose
	// velocity carried between sweeps, used as the prior when no odometry is
	// available: the best guess about where the vehicle is now is where it was
	// going last time.
	vx, vy, vtheta float64
	lastSweep      time.Time
	integrated     int
	degenerate     int
	usingOdometry  bool
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	mapSvc := components.Service{
		Definition: "map",
		SubPath:    "map",
		Details: map[string][]string{
			"Forms":   {"MapA_v1a"},
			"Unit":    {unitMetre},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   30,
		Description: "the occupancy grid built so far",
	}

	poseSvc := components.Service{
		Definition: "pose",
		SubPath:    "pose",
		Details: map[string][]string{
			"Forms":   {"PoseA_v1a"},
			"Unit":    {unitMetre},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:     2,
		SubscribeAble: true,
		Description:   "where in the map the sensor is believed to be",
	}

	coverage := components.Service{
		Definition: "coverage",
		SubPath:    "coverage",
		Details: map[string][]string{
			"Forms":   {"SignalA_v1a"},
			"Unit":    {unitPct},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   10,
		Description: "the share of the grid that has been observed at all",
	}

	return &components.UnitAsset{
		Name:    "Surveyor",
		Mission: components.MissionAggregation,
		ServicesMap: components.Services{
			mapSvc.SubPath:   &mapSvc,
			poseSvc.SubPath:  &poseSvc,
			coverage.SubPath: &coverage,
		},
		Traits: &CartographerConfig{
			WidthMetres:      60,
			HeightMetres:     60,
			Resolution:       0.05,
			PollMs:           200,
			MatchRangeMetres: 10,
			MinTravelMetres:  0.10,
			MinTurnDegrees:   5,
			MinValidFraction: 0.20,
			PGMPath:          "map.pgm",
			PGMIntervalS:     10,
			Search:           defaultSearch(),
		},
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cfg := CartographerConfig{}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("cartographer: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	t := &Traits{
		cfg:   cfg,
		owner: sys,
		g:     newGrid(cfg.WidthMetres, cfg.HeightMetres, cfg.Resolution),
		scanCervice: &components.Cervice{
			Definition: "scan",
			Protos:     components.SProtocols(sys.Husk.ProtoPort),
			Mode:       "get",
			Nodes:      make(map[string][]components.NodeInfo),
		},
		// Optional. A pose provider — a driver system turning wheel encoders and
		// the articulation angle into odometry — gives the scan matcher a far
		// better starting guess than constant velocity. None exists yet, so
		// this stays empty and the matcher works without it.
		poseCervice: &components.Cervice{
			Definition: "pose",
			Protos:     components.SProtocols(sys.Husk.ProtoPort),
			Mode:       "get",
			Nodes:      make(map[string][]components.NodeInfo),
		},
	}

	t.g.matchRange = cfg.MatchRangeMetres

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: components.Cervices{
			t.scanCervice.Definition: t.scanCervice,
			t.poseCervice.Definition: t.poseCervice,
		},
		Traits: t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	go t.run(sys.Ctx)
	go t.snapshot(sys.Ctx)

	return ua, func() {
		if err := t.g.writePGM(cfg.PGMPath); err != nil {
			log.Printf("cartographer: could not write the final map: %v", err)
			return
		}
		log.Printf("cartographer: final map written to %s", cfg.PGMPath)
	}
}

func applyDefaults(cfg *CartographerConfig) {
	if cfg.WidthMetres <= 0 {
		cfg.WidthMetres = 60
	}
	if cfg.HeightMetres <= 0 {
		cfg.HeightMetres = 60
	}
	if cfg.Resolution <= 0 {
		cfg.Resolution = 0.05
	}
	if cfg.PollMs <= 0 {
		cfg.PollMs = 200
	}
	if cfg.MatchRangeMetres <= 0 {
		cfg.MatchRangeMetres = 10
	}
	if cfg.MinValidFraction <= 0 {
		cfg.MinValidFraction = 0.20
	}
	if cfg.PGMPath == "" {
		cfg.PGMPath = "map.pgm"
	}
	if cfg.PGMIntervalS <= 0 {
		cfg.PGMIntervalS = 10
	}
	if cfg.Search.CoarseStep <= 0 {
		cfg.Search = defaultSearch()
	}
}

//-------------------------------------The mapping loop

func (t *Traits) run(ctx context.Context) {
	tick := time.NewTicker(time.Duration(t.cfg.PollMs) * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			sw, err := t.fetchScan()
			if err != nil {
				// Expected and frequent: the guetteur answers 503 whenever it
				// is blind or its sweep has gone stale, which is exactly the
				// case where continuing to map would be inventing geometry.
				continue
			}
			t.consume(sw)
		}
	}
}

// fetchScan asks the guetteur for its latest sweep, discovering it if need be.
func (t *Traits) fetchScan() (*forms.ScanA_v1a, error) {
	if len(t.scanCervice.Nodes) == 0 {
		if err := usecases.Search4Services(t.scanCervice, t.owner); err != nil {
			return nil, err
		}
	}
	f, err := usecases.GetState(t.scanCervice, t.owner)
	if err != nil {
		t.scanCervice.Nodes = make(map[string][]components.NodeInfo)
		return nil, err
	}
	sw, ok := f.(*forms.ScanA_v1a)
	if !ok {
		return nil, errUnexpectedForm
	}
	// The three arrays are separate on the wire and nothing but this stops a
	// provider from sending them ragged. Indexing them out of step would put an
	// obstacle at the wrong bearing, which is worse than dropping the sweep.
	if err := sw.Check(); err != nil {
		return nil, err
	}
	return sw, nil
}

// odometryPrior asks a pose provider where the vehicle is, if one exists. The
// delta since the last reading is what the scan matcher starts from.
func (t *Traits) odometryPrior() (pose, bool) {
	if len(t.poseCervice.Nodes) == 0 {
		if err := usecases.Search4Services(t.poseCervice, t.owner); err != nil {
			return pose{}, false
		}
		if len(t.poseCervice.Nodes) == 0 {
			return pose{}, false
		}
	}
	f, err := usecases.GetState(t.poseCervice, t.owner)
	if err != nil {
		t.poseCervice.Nodes = make(map[string][]components.NodeInfo)
		return pose{}, false
	}
	p, ok := f.(*forms.PoseA_v1a)
	if !ok {
		return pose{}, false
	}
	return pose{x: p.X, y: p.Y, theta: p.Heading * math.Pi / 180}, true
}

// consume folds one sweep into the map, first working out where it was taken.
func (t *Traits) consume(sw *forms.ScanA_v1a) {
	if len(sw.Angles) == 0 {
		return
	}
	// A sweep that is mostly silence is not evidence of open space, and
	// matching against it puts the vehicle wherever the few returns happen to
	// fit. Better to skip it and say so through the coverage figure.
	if frac := float64(sw.ValidCount()) / float64(len(sw.Angles)); frac < t.cfg.MinValidFraction {
		return
	}

	t.mu.Lock()
	first := t.integrated == 0
	prior := t.at
	dt := 0.0
	if !t.lastSweep.IsZero() {
		dt = sw.Timestamp.Sub(t.lastSweep).Seconds()
	}
	vx, vy, vtheta := t.vx, t.vy, t.vtheta
	g := t.g
	t.mu.Unlock()

	odo, haveOdometry := t.odometryPrior()
	if haveOdometry {
		prior = odo
		t.mu.Lock()
		t.usingOdometry = true
		t.mu.Unlock()
	} else if dt > 0 && dt < 2 {
		// Constant velocity. Crude, and the reason a real odometry source is
		// worth having: it assumes the vehicle kept doing what it was doing.
		prior = pose{prior.x + vx*dt, prior.y + vy*dt, prior.theta + vtheta*dt}
	}

	at, sharp := prior, true
	if !first {
		m := g.match(prior, sw.Angles, sw.Distances, sw.Valid, t.cfg.Search)
		at, sharp = m.at, m.sharp

		if !sharp {
			if haveOdometry {
				// The view cannot pin the vehicle down, so the matcher cannot
				// improve on odometry and trying makes it worse: it would slide
				// the pose along whichever direction the map is indifferent to.
				// Take the odometry and leave it alone.
				at = prior
			} else {
				// Nothing to fall back on. Refusing to map is the honest
				// answer: a corridor mapped from a pose that cannot be known
				// comes out the wrong length, and looks entirely convincing.
				t.mu.Lock()
				t.degenerate++
				n := t.degenerate
				t.mu.Unlock()
				if n == 1 || n%50 == 0 {
					log.Printf("cartographer: the view does not fix the vehicle's position (%d sweeps skipped) — this needs odometry, not a better guess", n)
				}
				return
			}
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	moved := math.Hypot(at.x-t.at.x, at.y-t.at.y)
	turned := math.Abs(wrapAngle(at.theta-t.at.theta)) * 180 / math.Pi
	if first || moved >= t.cfg.MinTravelMetres || turned >= t.cfg.MinTurnDegrees {
		g.integrate(at, sw.Angles, sw.Distances, sw.Valid)
		t.integrated++
	}

	if dt > 0 {
		t.vx = (at.x - t.at.x) / dt
		t.vy = (at.y - t.at.y) / dt
		t.vtheta = wrapAngle(at.theta-t.at.theta) / dt
	}
	t.at = at
	t.lastSweep = sw.Timestamp
}

// snapshot writes the picture a person looks at.
func (t *Traits) snapshot(ctx context.Context) {
	tick := time.NewTicker(time.Duration(t.cfg.PGMIntervalS) * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.mu.RLock()
			n := t.integrated
			err := t.g.writePGM(t.cfg.PGMPath)
			at := t.at
			t.mu.RUnlock()
			if err != nil {
				log.Printf("cartographer: cannot write %s: %v", t.cfg.PGMPath, err)
				continue
			}
			if n > 0 {
				log.Printf("cartographer: %d sweeps mapped, at x=%.2f y=%.2f heading=%.0f°  → %s",
					n, at.x, at.y, at.theta*180/math.Pi, t.cfg.PGMPath)
			}
		}
	}
}

func wrapAngle(a float64) float64 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a < -math.Pi {
		a += 2 * math.Pi
	}
	return a
}

//-------------------------------------The services

func (t *Traits) mapService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.RLock()
	f := t.g.form()
	n := t.integrated
	t.mu.RUnlock()

	if n == 0 {
		http.Error(w, "nothing has been mapped yet", http.StatusServiceUnavailable)
		return
	}
	f.Timestamp = time.Now()
	usecases.HTTPProcessGetRequest(w, r, &f)
}

// poseService answers where the sensor is believed to be — and refuses to
// answer before anything has been mapped, because the starting pose is an
// assumption rather than a measurement and a consumer cannot tell the two apart
// from the numbers alone.
func (t *Traits) poseService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.RLock()
	at, n, last := t.at, t.integrated, t.lastSweep
	t.mu.RUnlock()

	if n == 0 {
		http.Error(w, "no pose yet: nothing has been mapped", http.StatusServiceUnavailable)
		return
	}
	f := forms.PoseA_v1a{}
	f.NewForm()
	f.X, f.Y = at.x, at.y
	f.Heading = at.theta * 180 / math.Pi
	f.Frame = "map"
	f.DistanceUnit = unitMetre
	f.AngleUnit = unitDegree
	f.Timestamp = last
	usecases.HTTPProcessGetRequest(w, r, &f)
}

func (t *Traits) coverageService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.RLock()
	seen := 0
	for _, v := range t.g.cells {
		if v != 0 {
			seen++
		}
	}
	total := len(t.g.cells)
	t.mu.RUnlock()

	f := forms.SignalA_v1a{}
	f.NewForm()
	if total > 0 {
		f.Value = 100 * float64(seen) / float64(total)
	}
	f.Unit = unitPct
	f.Timestamp = time.Now()
	usecases.HTTPProcessGetRequest(w, r, &f)
}
