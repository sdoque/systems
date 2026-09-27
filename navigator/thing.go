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
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	unitMetre  = "<http://qudt.org/vocab/unit/M>"
	unitDegree = "<http://qudt.org/vocab/unit/DEG>"
)

// NavConfig is what the operator may set.
type NavConfig struct {
	// PlanningResolution is the cell size the route is planned on, coarser
	// than the map's: 10 cm is fine enough for a corridor and keeps a plan
	// well under a second on a Raspberry Pi.
	PlanningResolution float64 `json:"planningResolutionMetres"`

	// Clearance is how far the vehicle's middle keeps from anything: half its
	// width, and a margin. The Artitrax is about 0.75 m wide.
	Clearance float64 `json:"clearanceMetres"`

	// MaxCurvature is used when the loader does not say. The loader publishes
	// its own, from its geometry and steering limit, and that one is preferred.
	MaxCurvature float64 `json:"maxCurvature"`

	// Vehicle picks the loader out of the cloud, for its curvature limit.
	Vehicle map[string][]string `json:"vehicle"`

	// ReplanSeconds is how often a standing goal is planned again from where
	// the vehicle now is, against the map as it now is.
	ReplanSeconds int `json:"replanSeconds"`

	// ArriveMetres is how near the goal counts as there.
	ArriveMetres float64 `json:"arriveMetres"`

	// PicturePath is where the map with the route drawn on it is written,
	// after every plan: what a person looks at to see what was planned.
	PicturePath string `json:"picturePath"`
}

// Traits is the navigator's state.
type Traits struct {
	cfg   NavConfig
	owner *components.System

	mapCer, poseCer, limitCer *components.Cervice

	mu      sync.Mutex
	goal    *goal
	path    *forms.PathA_v1a
	limitOK bool // the loader's limit has been heard at least once
}

// goal is a destination, as asked for and as planned to.
type goal struct {
	askedX, askedY float64
	x, y           float64 // where the route actually ends
	frame          string
	why            string // why it is not where it was asked, if it is not
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	goalSvc := components.Service{
		Definition: "goal",
		SubPath:    "goal",
		Details: map[string][]string{
			"Forms":   {"PoseA_v1a", "PolarA_v1a"},
			"Methods": components.HTTPMethods("GET", "PUT", "DELETE"),
		},
		RegPeriod: 30,
		Description: "PUT a destination — a PoseA in the map frame, or a PolarA: a distance and a bearing " +
			"from the vehicle — and a route is planned to it; GET says where the route ends; DELETE drops it",
	}
	pathSvc := components.Service{
		Definition: "path",
		SubPath:    "path",
		Details: map[string][]string{
			"Forms":   {"PathA_v1a"},
			"Unit":    {unitMetre},
			"Methods": components.HTTPMethods("GET"),
		},
		RegPeriod:   10,
		Description: "the planned route, in the map frame it was planned in",
	}
	return &components.UnitAsset{
		Name:     "Planner",
		Mission:  components.MissionControl,
		Mobility: components.MobilityMovable,
		ServicesMap: components.Services{
			goalSvc.SubPath: &goalSvc,
			pathSvc.SubPath: &pathSvc,
		},
		Traits: &NavConfig{
			PlanningResolution: 0.1,
			Clearance:          0.4,
			MaxCurvature:       0.5,
			Vehicle:            map[string][]string{"Model": {"artitrax"}},
			ReplanSeconds:      5,
			ArriveMetres:       0.5,
			PicturePath:        "plan.ppm",
		},
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cfg := NavConfig{}
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("navigator: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	protos := components.SProtocols(sys.Husk.ProtoPort)
	get := func(def string, details map[string][]string) *components.Cervice {
		return &components.Cervice{
			Definition: def, Protos: protos, Mode: "get",
			Nodes: make(map[string][]components.NodeInfo), Details: details,
		}
	}
	t := &Traits{
		cfg:      cfg,
		owner:    sys,
		mapCer:   get("map", nil),
		poseCer:  get("pose", nil),
		limitCer: get("curvatureLimit", cfg.Vehicle),
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Mobility:    uac.Mobility,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: components.Cervices{
			"map":            t.mapCer,
			"pose":           t.poseCer,
			"curvatureLimit": t.limitCer,
		},
		Traits: t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}
	go t.replan(sys.Ctx)
	return ua, func() { log.Println("navigator: shut down") }
}

func applyDefaults(cfg *NavConfig) {
	if cfg.PlanningResolution <= 0 {
		cfg.PlanningResolution = 0.1
	}
	if cfg.Clearance <= 0 {
		cfg.Clearance = 0.4
	}
	if cfg.MaxCurvature <= 0 {
		cfg.MaxCurvature = 0.5
	}
	if cfg.Vehicle == nil {
		cfg.Vehicle = map[string][]string{"Model": {"artitrax"}}
	}
	if cfg.ReplanSeconds <= 0 {
		cfg.ReplanSeconds = 5
	}
	if cfg.ArriveMetres <= 0 {
		cfg.ArriveMetres = 0.5
	}
	if cfg.PicturePath == "" {
		cfg.PicturePath = "plan.ppm"
	}
}

//-------------------------------------Reading a destination

// here is where the vehicle is, as the cartographer says.
type here struct {
	x, y, th float64 // th in radians, positive counter-clockwise
	frame    string
}

// destination turns what was PUT into a point in the map.
//
// A PoseA is a point in the map already, and must be in this map: a goal from
// a previous run of the cartographer names somewhere else. A PolarA is a
// distance and a direction from where the vehicle is now, and its frame says
// which way the direction is measured — from straight ahead ("vehicle"), or
// from the map's x axis ("map@…"), in both cases counter-clockwise. A compass
// frame is refused: nothing here knows where north is.
func destination(f forms.Form, at here) (float64, float64, error) {
	switch g := f.(type) {
	case *forms.PoseA_v1a:
		if g.Frame != "" && g.Frame != forms.PolarMap && g.Frame != at.frame {
			return 0, 0, fmt.Errorf("that destination is in %s, and the map is now %s: post it again in this map", g.Frame, at.frame)
		}
		if g.DistanceUnit != "" && g.DistanceUnit != unitMetre {
			return 0, 0, fmt.Errorf("a destination in %s; it must be in meters", g.DistanceUnit)
		}
		return g.X, g.Y, nil

	case *forms.PolarA_v1a:
		if err := g.Check(); err != nil {
			return 0, 0, err
		}
		if g.MagnitudeUnit != "" && g.MagnitudeUnit != unitMetre {
			return 0, 0, fmt.Errorf("a distance in %s; it must be in meters", g.MagnitudeUnit)
		}
		dir, err := g.Radians()
		if err != nil {
			return 0, 0, err
		}
		switch {
		case g.Frame == forms.PolarVehicle:
			dir += at.th // from straight ahead, so turn by the vehicle's heading
		case g.Frame == forms.PolarMap || g.Frame == at.frame:
			// already measured from the map's x axis
		case strings.HasPrefix(g.Frame, forms.PolarMap+"@"):
			return 0, 0, fmt.Errorf("that bearing is in %s, and the map is now %s", g.Frame, at.frame)
		default:
			return 0, 0, fmt.Errorf("a bearing in %q cannot be used: nothing here knows where north is; "+
				"give it from straight ahead (%q) or in the map", g.Frame, forms.PolarVehicle)
		}
		return at.x + g.Magnitude*math.Cos(dir), at.y + g.Magnitude*math.Sin(dir), nil
	}
	return 0, 0, fmt.Errorf("a destination must be a PoseA_v1.0 or a PolarA_v1.0, not %s", f.FormVersion())
}

//-------------------------------------Asking the other systems

func (t *Traits) whereAmI() (here, error) {
	f, err := t.fetch(t.poseCer)
	if err != nil {
		return here{}, fmt.Errorf("no pose from the cartographer: %w", err)
	}
	p, ok := f.(*forms.PoseA_v1a)
	if !ok {
		return here{}, fmt.Errorf("the pose came as %s", f.FormVersion())
	}
	th := p.Heading
	if p.AngleUnit == "" || p.AngleUnit == unitDegree {
		th *= math.Pi / 180
	}
	return here{x: p.X, y: p.Y, th: th, frame: p.Frame}, nil
}

func (t *Traits) theMap() (*forms.MapA_v1a, error) {
	f, err := t.fetch(t.mapCer)
	if err != nil {
		return nil, fmt.Errorf("no map from the cartographer: %w", err)
	}
	m, ok := f.(*forms.MapA_v1a)
	if !ok {
		return nil, fmt.Errorf("the map came as %s", f.FormVersion())
	}
	return m, nil
}

// curvature is the loader's own limit when it answers, and the configured one
// when it does not — said once, so a vehicle planned with a guessed limit is
// visible in the log.
func (t *Traits) curvature() float64 {
	f, err := t.fetch(t.limitCer)
	if sig, ok := f.(*forms.SignalA_v1a); err == nil && ok && sig.Value > 0 {
		t.mu.Lock()
		t.limitOK = true
		t.mu.Unlock()
		return sig.Value
	}
	t.mu.Lock()
	first := !t.limitOK
	t.limitOK = true // said once
	t.mu.Unlock()
	if first {
		log.Printf("navigator: the loader's curvature limit is not available (%v); planning with the configured %.2f /m", err, t.cfg.MaxCurvature)
	}
	return t.cfg.MaxCurvature
}

func (t *Traits) fetch(cer *components.Cervice) (forms.Form, error) {
	if len(cer.Nodes) == 0 {
		if err := usecases.Search4Services(cer, t.owner); err != nil {
			return nil, err
		}
	}
	f, err := usecases.GetState(cer, t.owner)
	if err != nil {
		var refused *usecases.ProviderRefusal
		if !errors.As(err, &refused) {
			cer.Nodes = make(map[string][]components.NodeInfo)
		}
		return nil, err
	}
	return f, nil
}

//-------------------------------------Planning

// planTo plans from where the vehicle is to (gx, gy), and keeps the result.
func (t *Traits) planTo(gx, gy float64, at here) (*goal, error) {
	m, err := t.theMap()
	if err != nil {
		return nil, err
	}
	if m.Frame != at.frame {
		return nil, fmt.Errorf("the map (%s) and the pose (%s) are from different runs of the cartographer", m.Frame, at.frame)
	}
	v := Vehicle{MaxCurvature: t.curvature(), Clearance: t.cfg.Clearance}
	start := time.Now()
	p, err := plan(m, t.cfg.PlanningResolution, v, at.x, at.y, at.th, gx, gy)
	if err != nil {
		return nil, err
	}
	g := &goal{askedX: gx, askedY: gy, x: p.GoalX, y: p.GoalY, frame: at.frame}
	if p.Adjusted {
		g.why = p.Why
		// The nearest reachable point can be where the vehicle already is — a
		// destination behind the end wall of a corridor, say. A one-pose route
		// would read as "arrived" to anyone following it, so it is refused.
		if math.Hypot(p.GoalX-at.x, p.GoalY-at.y) <= t.cfg.ArriveMetres {
			return nil, fmt.Errorf("%s, and the nearest point that can be reached is where the vehicle already is", p.Why)
		}
	}
	path := &forms.PathA_v1a{X: p.X, Y: p.Y, Frame: at.frame, DistanceUnit: unitMetre, AngleUnit: unitDegree, Timestamp: time.Now()}
	path.NewForm()
	for _, h := range p.Heading {
		path.Heading = append(path.Heading, h*180/math.Pi)
	}

	t.mu.Lock()
	t.goal, t.path = g, path
	t.mu.Unlock()

	if err := writePicture(t.cfg.PicturePath, m, p, at); err != nil {
		log.Printf("navigator: cannot write %s: %v", t.cfg.PicturePath, err)
	}
	log.Printf("navigator: %.1f m route to (%.2f, %.2f), %d states in %v",
		pathLength(p), p.GoalX, p.GoalY, p.Expanded, time.Since(start).Round(time.Millisecond))
	if p.Adjusted {
		log.Printf("navigator: not the destination asked for (%.2f, %.2f): %s — planned to the nearest point that can be reached",
			gx, gy, p.Why)
	}
	return g, nil
}

// replan plans a standing goal again, from where the vehicle now is and
// against the map as it now is, and drops it when the vehicle has arrived or
// the map it was set in is gone.
func (t *Traits) replan(ctx context.Context) {
	tick := time.NewTicker(time.Duration(t.cfg.ReplanSeconds) * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		t.mu.Lock()
		g := t.goal
		t.mu.Unlock()
		if g == nil {
			continue
		}
		at, err := t.whereAmI()
		if err != nil {
			continue
		}
		switch {
		case at.frame != g.frame:
			log.Printf("navigator: the cartographer has started a new map (%s); the destination was in %s and is dropped",
				at.frame, g.frame)
			t.drop()
		case math.Hypot(at.x-g.x, at.y-g.y) <= t.cfg.ArriveMetres:
			log.Printf("navigator: arrived at (%.2f, %.2f)", g.x, g.y)
			t.drop()
		default:
			if _, err := t.planTo(g.askedX, g.askedY, at); err != nil {
				log.Printf("navigator: replanning failed: %v", err)
			}
		}
	}
}

func (t *Traits) drop() {
	t.mu.Lock()
	t.goal, t.path = nil, nil
	t.mu.Unlock()
}

func pathLength(p *Plan) float64 {
	l := 0.0
	for i := 1; i < len(p.X); i++ {
		l += math.Hypot(p.X[i]-p.X[i-1], p.Y[i]-p.Y[i-1])
	}
	return l
}

//-------------------------------------Service handlers

func (t *Traits) goalService(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		t.mu.Lock()
		g := t.goal
		t.mu.Unlock()
		if g == nil {
			http.Error(w, "no destination", http.StatusServiceUnavailable)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, poseForm(g))

	case http.MethodPut:
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			mediaType = "application/json"
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "cannot read the request", http.StatusBadRequest)
			return
		}
		f, err := usecases.Unpack(body, mediaType)
		if err != nil {
			http.Error(w, "malformed destination: "+err.Error(), http.StatusBadRequest)
			return
		}
		at, err := t.whereAmI()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		gx, gy, err := destination(f, at)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		g, err := t.planTo(gx, gy, at)
		if err != nil {
			http.Error(w, "no route: "+err.Error(), http.StatusConflict)
			return
		}
		if g.why != "" {
			// Said in the response as well as the log: the person who typed
			// the destination is the one who needs to know it was not used.
			w.Header().Set("Warning", fmt.Sprintf(`299 navigator "%s; planned to the nearest point that can be reached"`, g.why))
		}
		respond(w, poseForm(g))

	case http.MethodDelete:
		t.drop()
		log.Println("navigator: destination dropped")
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (t *Traits) pathService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.Lock()
	p := t.path
	t.mu.Unlock()
	if p == nil {
		http.Error(w, "no route: no destination has been set", http.StatusServiceUnavailable)
		return
	}
	usecases.HTTPProcessGetRequest(w, r, p)
}

func poseForm(g *goal) *forms.PoseA_v1a {
	f := &forms.PoseA_v1a{X: g.x, Y: g.y, Frame: g.frame, DistanceUnit: unitMetre, AngleUnit: unitDegree, Timestamp: time.Now()}
	f.NewForm()
	return f
}

func respond(w http.ResponseWriter, f forms.Form) {
	body, err := usecases.Pack(f, "application/json")
	if err != nil {
		http.Error(w, "cannot encode the response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		log.Printf("navigator: writing response: %v", err)
	}
}
