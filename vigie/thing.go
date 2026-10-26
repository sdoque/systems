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
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	unitDegree   = "<http://qudt.org/vocab/unit/DEG>"
	unitMetre    = "<http://qudt.org/vocab/unit/M>"
	unitPct      = "<http://qudt.org/vocab/unit/PERCENT>"
	unitRadPerS  = "<http://qudt.org/vocab/unit/RAD-PER-SEC>"
	qkLength     = "<http://qudt.org/vocab/quantitykind/Length>"
	qkAngle      = "<http://qudt.org/vocab/quantitykind/Angle>"
	qkAngularVel = "<http://qudt.org/vocab/quantitykind/AngularVelocity>"
)

// VigieConfig is what the operator may set.
type VigieConfig struct {
	// Source is "livox" for the lidar, "simulated" for the tunnel simulator,
	// or "replay" for a recording made with -record. The template ships
	// simulated, so the system runs anywhere.
	Source     string `json:"source"`
	ReplayFile string `json:"replayFile"`

	// LidarIP is the lidar's address. Empty: find it by broadcast.
	LidarIP string `json:"lidarIP"`
	// Configure says whether to tell the lidar where to send its data and to
	// sample. False leaves its settings alone and only listens — for when
	// something else, a ROS driver say, is also configuring it.
	Configure bool `json:"configure"`
	// CheckDataCRC checks the CRC-32 the protocol describes on every point
	// and IMU packet. Livox's own SDK does not; see livox.go.
	CheckDataCRC bool `json:"checkDataCRC"`

	Mount     Mount           `json:"mount"`
	Body      Box             `json:"body"` // the vehicle's own outline, whose returns are ignored
	Slice     SliceConfig     `json:"slice"`
	Clearance ClearanceConfig `json:"clearance"`

	IMUAverageMs int `json:"imuAverageMs"`
	StaleAfterMs int `json:"staleAfterMs"`

	// GyroZeroSeconds is how long the vehicle must stand still at start for
	// the gyroscope's zero to be measured; see motion in process.go.
	GyroZeroSeconds float64 `json:"gyroZeroSeconds"`
}

// Traits is the lidar, and what a service handler is given.
type Traits struct {
	cfg  VigieConfig
	rot  rotation
	pts  *cloud
	imu  *motion
	rec  *recorder
	stat stats

	// saidUnchecked is which data types have been reported as arriving with
	// an empty checksum; read and written by consume alone.
	saidUnchecked [256]bool
}

// stats counts what arrived, so the log can say what is wrong.
type stats struct {
	mu        sync.Mutex
	ok        int
	crcFailed int
	refused   int
	warnedCRC bool
	lastWhy   string
}

//-------------------------------------Instantiate a unit asset template

func initTemplate() *components.UnitAsset {
	get := components.HTTPMethods("GET")
	signal := func(def, unit, kind, what string, period int) *components.Service {
		d := map[string][]string{"Forms": {"SignalA_v1a"}, "Unit": {unit}, "Methods": get}
		if kind != "" {
			d["QuantityKind"] = []string{kind}
		}
		return &components.Service{Definition: def, SubPath: def, Details: d, RegPeriod: period,
			SubscribeAble: true, Description: what}
	}
	scan := &components.Service{
		Definition: "scan",
		SubPath:    "scan",
		Details: map[string][]string{
			"Forms":   {"ScanA_v1a"},
			"Unit":    {unitMetre},
			"Methods": get,
		},
		RegPeriod:     2,
		SubscribeAble: true,
		Description:   "a 360° horizontal scan from a band of the point cloud, in the vehicle frame",
	}
	services := components.Services{scan.SubPath: scan}
	for _, s := range []*components.Service{
		signal("clearance", unitMetre, qkLength, "the nearest return in the corridor ahead; unavailable rather than optimistic when nothing came back from it", 2),
		signal("yawrate", unitRadPerS, qkAngularVel, "the vehicle's rate of turn about the vertical, positive to the left", 2),
		signal("heading", unitDegree, qkAngle, "the yaw rate integrated since start: drifts, and never wraps", 2),
		signal("pitch", unitDegree, qkAngle, "from gravity, positive nose down (ISO 8855)", 5),
		signal("roll", unitDegree, qkAngle, "from gravity, positive right side down (ISO 8855)", 5),
		signal("quality", unitPct, "", "the share of the last window's beams that came back", 5),
	} {
		services[s.SubPath] = s
	}

	cfg := defaultConfig()
	return &components.UnitAsset{
		Name:        "Crowsnest",
		Mission:     components.MissionMeasurement,
		Mobility:    components.MobilityMovable,
		Details:     map[string][]string{"Model": {"Livox Mid-360"}},
		ServicesMap: services,
		Traits:      &cfg,
	}
}

func defaultConfig() VigieConfig {
	return VigieConfig{
		Source:       "simulated",
		Configure:    true,
		CheckDataCRC: true,
		Mount:        Mount{ZMetres: 1.0},
		Slice: SliceConfig{
			MinHeightMetres: 0.3, MaxHeightMetres: 1.5,
			BinDegrees: 1, MinRangeMetres: 0.2, MaxRangeMetres: 40,
			AccumulateMs: 200,
		},
		Clearance:       ClearanceConfig{HalfWidthMetres: 0.45, MinHeightMetres: 0.1, MaxHeightMetres: 1.6},
		IMUAverageMs:    100,
		GyroZeroSeconds: 3,
		StaleAfterMs:    1000,
	}
}

// applyDefaults fills what a configuration left out with the defaults, so an
// older file still runs.
func applyDefaults(c *VigieConfig) {
	d := defaultConfig()
	if c.Source == "" {
		c.Source = d.Source
	}
	if c.Slice.BinDegrees <= 0 {
		c.Slice.BinDegrees = d.Slice.BinDegrees
	}
	if c.Slice.MaxHeightMetres <= c.Slice.MinHeightMetres {
		c.Slice.MinHeightMetres, c.Slice.MaxHeightMetres = d.Slice.MinHeightMetres, d.Slice.MaxHeightMetres
	}
	if c.Slice.MaxRangeMetres <= 0 {
		c.Slice.MaxRangeMetres = d.Slice.MaxRangeMetres
	}
	if c.Slice.AccumulateMs <= 0 {
		c.Slice.AccumulateMs = d.Slice.AccumulateMs
	}
	if c.Clearance.HalfWidthMetres <= 0 {
		c.Clearance = d.Clearance
	}
	if c.GyroZeroSeconds <= 0 {
		c.GyroZeroSeconds = d.GyroZeroSeconds
	}
	if c.IMUAverageMs <= 0 {
		c.IMUAverageMs = d.IMUAverageMs
	}
	if c.StaleAfterMs <= 0 {
		c.StaleAfterMs = d.StaleAfterMs
	}
}

//-------------------------------------Instantiate the unit asset

func newResource(uac usecases.ConfigurableAsset, sys *components.System, recordPath string) (*components.UnitAsset, func()) {
	cfg := defaultConfig()
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("vigie: cannot parse traits: %v", err)
		}
	}
	applyDefaults(&cfg)

	t := &Traits{
		cfg: cfg,
		rot: cfg.Mount.rotation(),
		pts: &cloud{window: time.Duration(cfg.Slice.AccumulateMs) * time.Millisecond},
		imu: &motion{window: time.Duration(cfg.IMUAverageMs) * time.Millisecond,
			zeroFor: time.Duration(cfg.GyroZeroSeconds * float64(time.Second))},
	}
	if recordPath != "" {
		rec, err := newRecorder(recordPath)
		if err != nil {
			log.Fatalf("vigie: cannot record to %s: %v", recordPath, err)
		}
		t.rec = rec
		log.Printf("vigie: recording every datagram to %s", recordPath)
	}

	src, err := t.openSource(sys.Ctx)
	if err != nil {
		log.Fatalf("vigie: %v", err)
	}
	ch := make(chan datagram, 4096)
	go func() {
		if err := src.run(sys.Ctx, ch); err != nil {
			log.Printf("vigie: the %s source stopped: %v", cfg.Source, err)
		}
	}()
	go t.consume(sys.Ctx, ch)
	go t.report(sys.Ctx)

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
		if t.rec != nil {
			if err := t.rec.close(); err != nil {
				log.Printf("vigie: closing the recording: %v", err)
			}
		}
	}
}

// openSource starts the configured source. For the lidar that means finding
// it, saying what it is, and — unless told not to — pointing its data here.
func (t *Traits) openSource(ctx context.Context) (source, error) {
	switch t.cfg.Source {
	case "simulated":
		log.Println("vigie: running against the tunnel SIMULATOR — no lidar is being read")
		return simulatedSource{height: t.cfg.Mount.ZMetres, speed: 0.3}, nil
	case "replay":
		if t.cfg.ReplayFile == "" {
			return nil, errors.New(`source "replay" needs a replayFile`)
		}
		log.Printf("vigie: replaying %s", t.cfg.ReplayFile)
		return replaySource{path: t.cfg.ReplayFile}, nil
	case "livox":
		l, err := connect(t.cfg.LidarIP)
		if err != nil {
			return nil, err
		}
		defer l.conn.Close()
		log.Printf("vigie: the lidar at %v says:\n%s", l.addr.IP, l.describe())
		if t.cfg.Configure {
			host, err := hostAddressFor(l.addr.IP)
			if err != nil {
				return nil, fmt.Errorf("cannot tell which address of this host reaches %v: %w", l.addr.IP, err)
			}
			if err := l.configure(host); err != nil {
				return nil, fmt.Errorf("configuring the lidar: %w", err)
			}
			log.Printf("vigie: the lidar now sends points to %v:%d and the IMU to %v:%d, and samples", host, portHostPoints, host, portHostIMU)
		} else {
			log.Println("vigie: configure is false, so the lidar's settings are left as they are and this system only listens")
		}
		return livoxSource{}, nil
	}
	return nil, fmt.Errorf("unknown source %q: livox, simulated or replay", t.cfg.Source)
}

// connect finds the lidar, by broadcast unless its address is given.
func connect(ip string) (*lidar, error) {
	conn, err := openCommand()
	if err != nil {
		return nil, err
	}
	if ip != "" {
		addr := net.ParseIP(ip)
		if addr == nil {
			conn.Close()
			return nil, fmt.Errorf("lidarIP %q is not an address", ip)
		}
		return &lidar{conn: conn, addr: &net.UDPAddr{IP: addr, Port: portLidarCmd}}, nil
	}
	found, err := discover(conn, 2*time.Second)
	if err != nil {
		conn.Close()
		return nil, err
	}
	for _, f := range found {
		if f.devType == devTypeMid360 {
			log.Printf("vigie: found a Mid-360, serial %s, at %v", f.serial, f.ip)
			port := int(f.cmdPort)
			if port == 0 {
				port = portLidarCmd
			}
			return &lidar{conn: conn, addr: &net.UDPAddr{IP: f.ip, Port: port}}, nil
		}
		log.Printf("vigie: found a Livox device of type %d (not a Mid-360), serial %s, at %v", f.devType, f.serial, f.ip)
	}
	conn.Close()
	return nil, errors.New("no Mid-360 answered the discovery broadcast; is it powered, and is this host on its network (192.168.1.x by default)?")
}

// consume turns datagrams into the window of points and the IMU's motion.
func (t *Traits) consume(ctx context.Context, in <-chan datagram) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-in:
			if t.rec != nil {
				if err := t.rec.write(d); err != nil {
					log.Printf("vigie: recording stopped: %v", err)
					t.rec = nil
				}
			}
			p, err := decodePacket(d.data, t.cfg.CheckDataCRC)
			if !t.count(err) {
				continue
			}
			if p.unchecked && !t.saidUnchecked[p.dataType] {
				t.saidUnchecked[p.dataType] = true
				log.Printf("vigie: the lidar leaves the checksum of its data type %d packets empty; "+
					"they are checked for their structure only", p.dataType)
			}
			if p.dataType == dataIMU {
				for _, s := range p.imu {
					t.imu.add(timedIMU{at: d.at, stamp: p.stamp, gyro: t.rot.apply(s.gyro), acc: t.rot.apply(s.acc)})
				}
				continue
			}
			pts := make([]timedPoint, len(p.points))
			m := t.cfg.Mount
			for i, q := range p.points {
				v := t.rot.apply([3]float64{q.x, q.y, q.z})
				pts[i] = timedPoint{at: d.at, x: v[0] + m.XMetres, y: v[1] + m.YMetres, z: v[2] + m.ZMetres}
			}
			t.pts.add(d.at, pts, p.empty)
		}
	}
}

// count keeps the tally and reports whether the packet is usable.
func (t *Traits) count(err error) bool {
	t.stat.mu.Lock()
	defer t.stat.mu.Unlock()
	switch {
	case err == nil:
		t.stat.ok++
		return true
	case errors.Is(err, errDataCRC):
		t.stat.crcFailed++
		// Said loudly and once. A packet that is well formed but whose
		// checksum never matches is either corrupt or checked differently from
		// how the protocol describes it, and which of the two is a question for
		// a person with a recording, not for this system to guess at.
		if !t.stat.warnedCRC && t.stat.crcFailed >= 200 && t.stat.ok == 0 {
			t.stat.warnedCRC = true
			log.Printf("vigie: %d well-formed packets read and none passed the CRC-32 — the check in livox.go "+
				"may not match this lidar. Record with -record and look before setting checkDataCRC to false.", t.stat.crcFailed)
		}
	default:
		t.stat.refused++
		t.stat.lastWhy = err.Error()
	}
	return false
}

// report logs, every ten seconds, what arrived and what was refused.
func (t *Traits) report(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var prev stats
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.stat.mu.Lock()
			ok, crc, bad, why := t.stat.ok-prev.ok, t.stat.crcFailed-prev.crcFailed, t.stat.refused-prev.refused, t.stat.lastWhy
			prev.ok, prev.crcFailed, prev.refused = t.stat.ok, t.stat.crcFailed, t.stat.refused
			t.stat.mu.Unlock()
			switch {
			case ok+crc+bad == 0:
				log.Println("vigie: nothing has arrived in ten seconds")
			case bad > 0:
				log.Printf("vigie: in ten seconds %d packets, %d failed the CRC, %d refused (last: %s)", ok, crc, bad, why)
			case crc > 0:
				log.Printf("vigie: in ten seconds %d packets, %d failed the CRC", ok, crc)
			}
		}
	}
}

//-------------------------------------The services

func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	switch servicePath {
	case "scan":
		t.scanService(w, r)
	case "clearance":
		t.clearanceService(w, r)
	case "yawrate", "heading", "pitch", "roll":
		t.motionService(w, r, servicePath)
	case "quality":
		t.qualityService(w, r)
	default:
		http.Error(w, "Invalid service path", http.StatusBadRequest)
	}
}

// window returns the points of the current window if it is fresh.
func (t *Traits) window() ([]timedPoint, int, int, time.Time, time.Time, bool) {
	pts, ret, emp, first, last := t.pts.snapshot()
	fresh := !last.IsZero() && time.Since(last) < time.Duration(t.cfg.StaleAfterMs)*time.Millisecond
	return pts, ret, emp, first, last, fresh
}

func (t *Traits) scanService(w http.ResponseWriter, r *http.Request) {
	pts, _, _, first, last, fresh := t.window()
	if !fresh {
		http.Error(w, "no points within the freshness window", http.StatusServiceUnavailable)
		return
	}
	sw := slice(pts, t.cfg.Slice, t.cfg.Body, first, last)
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

func (t *Traits) clearanceService(w http.ResponseWriter, r *http.Request) {
	pts, _, _, _, last, fresh := t.window()
	if !fresh {
		http.Error(w, "no points within the freshness window", http.StatusServiceUnavailable)
		return
	}
	d, ok := clearanceAhead(pts, t.cfg.Clearance, t.cfg.Body)
	if !ok {
		http.Error(w, "blind: nothing came back from the corridor ahead", http.StatusServiceUnavailable)
		return
	}
	t.signal(w, r, d, unitMetre, last)
}

func (t *Traits) motionService(w http.ResponseWriter, r *http.Request, what string) {
	gyro, acc, heading, at, ok := t.imu.averages()
	if !ok || time.Since(at) > time.Duration(t.cfg.StaleAfterMs)*time.Millisecond {
		http.Error(w, "no IMU sample within the freshness window", http.StatusServiceUnavailable)
		return
	}
	if (what == "yawrate" || what == "heading") && !t.imu.zeroKnown() {
		http.Error(w, "the gyroscope's zero is still being measured: the vehicle must stand still", http.StatusServiceUnavailable)
		return
	}
	pitch, roll := tilt(acc)
	switch what {
	case "yawrate":
		t.signal(w, r, gyro[2], unitRadPerS, at)
	case "heading":
		t.signal(w, r, heading, unitDegree, at)
	case "pitch":
		t.signal(w, r, pitch, unitDegree, at)
	case "roll":
		t.signal(w, r, roll, unitDegree, at)
	}
}

// qualityService is the share of beams that came back in the last window.
// Zero rather than unavailable when nothing did: that is itself the reading a
// consumer most needs when clearance has gone quiet.
func (t *Traits) qualityService(w http.ResponseWriter, r *http.Request) {
	_, ret, emp, _, last, _ := t.window()
	v := 0.0
	if ret+emp > 0 {
		v = 100 * float64(ret) / float64(ret+emp)
	}
	if last.IsZero() {
		last = time.Now()
	}
	t.signal(w, r, v, unitPct, last)
}

func (t *Traits) signal(w http.ResponseWriter, r *http.Request, v float64, unit string, at time.Time) {
	f := forms.SignalA_v1a{}
	f.NewForm()
	f.Value = v
	f.Unit = unit
	f.Timestamp = at
	usecases.HTTPProcessGetRequest(w, r, &f)
}
