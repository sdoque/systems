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
	"strconv"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

const (
	unitAccel   = "<http://qudt.org/vocab/unit/M-PER-SEC2>"
	unitRadPerS = "<http://qudt.org/vocab/unit/RAD-PER-SEC>"
	unitMicroT  = "<http://qudt.org/vocab/unit/MicroT>"
	unitDegC    = "<http://qudt.org/vocab/unit/DEG_C>"

	qkAccel       = "<http://qudt.org/vocab/quantitykind/Acceleration>"
	qkAngularVel  = "<http://qudt.org/vocab/quantitykind/AngularVelocity>"
	qkMagFluxDens = "<http://qudt.org/vocab/quantitykind/MagneticFluxDensity>"
	qkTemperature = "<http://qudt.org/vocab/quantitykind/ThermodynamicTemperature>"
)

// pollPeriod is how often the chip is read. It samples at 112.5 Hz behind a
// 50 Hz filter, so reading at 100 Hz loses little and folds nothing back.
const pollPeriod = 10 * time.Millisecond

// SpikerConfig is an asset's traits: which bus the chip is on, how it is set
// up, and how it sits on the vehicle.
type SpikerConfig struct {
	// Bus is "i2c" or "spi". The SparkFun breakout's Qwiic connectors are
	// I2C; SPI is wired to the pads on its back.
	Bus string `json:"bus"`
	// Device is the kernel's device: /dev/i2c-1 for the Pi's I2C pins,
	// /dev/spidev0.0 for its SPI pins with chip select 0.
	Device string `json:"device"`
	// Address is the chip's I2C address: 0x69, or 0x68 with the breakout's
	// address jumper closed. Not used on SPI.
	Address string `json:"address"`
	// SPISpeedHz is the SPI clock. The chip takes up to 7 MHz; 1 MHz forgives
	// jumper wires and still reads a sample in a quarter of a millisecond.
	SPISpeedHz int `json:"spiSpeedHz"`

	// The full scales: ±2, 4, 8 or 16 g, and ±250, 500, 1000 or 2000 °/s.
	// A smaller range resolves finer and saturates sooner.
	AccelRangeG   int `json:"accelRangeG"`
	GyroRangeDegS int `json:"gyroRangeDegS"`

	// AverageMs is how long the samples of one reading are averaged over. It
	// is also how often a subscriber can hear of a change. The mean of the
	// angular velocity over the window is the turn in that window divided by
	// its length, so a consumer that integrates it loses nothing.
	AverageMs int `json:"averageMs"`
	// GyroZeroSeconds is how long the vehicle stands still at start while the
	// gyroscope's zero is measured. Zero skips the measurement.
	GyroZeroSeconds float64 `json:"gyroZeroSeconds"`
	// StaleAfterMs is how old a reading may be before it is not served.
	StaleAfterMs int `json:"staleAfterMs"`

	Mount Mount `json:"mount"`
}

// Mount is how the chip is turned on the vehicle: the rotation from the
// chip's axes, as TDK draws them (datasheet §15, figure 12), to the vehicle's,
// ISO 8855 (x forward, y left, z up). All zero is a chip lying flat with its x
// forward. Where on the vehicle it sits is not modeled: an accelerometer away
// from the center of rotation also feels the turn, which is for a consumer
// that needs it to correct.
type Mount struct {
	RollDegrees  float64 `json:"rollDegrees"`
	PitchDegrees float64 `json:"pitchDegrees"`
	YawDegrees   float64 `json:"yawDegrees"`
}

// rotation is the chip-to-vehicle rotation, Rz(yaw)·Ry(pitch)·Rx(roll), as
// the vigie turns its lidar.
type rotation [3][3]float64

func (m Mount) rotation() rotation {
	r, p, y := m.RollDegrees*math.Pi/180, m.PitchDegrees*math.Pi/180, m.YawDegrees*math.Pi/180
	cr, sr, cp, sp, cy, sy := math.Cos(r), math.Sin(r), math.Cos(p), math.Sin(p), math.Cos(y), math.Sin(y)
	return rotation{
		{cy * cp, cy*sp*sr - sy*cr, cy*sp*cr + sy*sr},
		{sy * cp, sy*sp*sr + cy*cr, sy*sp*cr - cy*sr},
		{-sp, cp * sr, cp * cr},
	}
}

func (r rotation) apply(v [3]float64) [3]float64 {
	return [3]float64{
		r[0][0]*v[0] + r[0][1]*v[1] + r[0][2]*v[2],
		r[1][0]*v[0] + r[1][1]*v[1] + r[1][2]*v[2],
		r[2][0]*v[0] + r[2][1]*v[1] + r[2][2]*v[2],
	}
}

func defaultConfig() SpikerConfig {
	return SpikerConfig{
		Bus:             "i2c",
		Device:          "/dev/i2c-1",
		Address:         "0x69",
		SPISpeedHz:      1000000,
		AccelRangeG:     4,
		GyroRangeDegS:   500,
		AverageMs:       100,
		GyroZeroSeconds: 3,
		StaleAfterMs:    1000,
	}
}

// applyDefaults fills what a configuration left out, so an older file still
// runs.
func applyDefaults(c *SpikerConfig) {
	d := defaultConfig()
	if c.Bus == "" {
		c.Bus = d.Bus
	}
	if c.Device == "" {
		c.Device = d.Device
		if c.Bus == "spi" {
			c.Device = "/dev/spidev0.0"
		}
	}
	if c.Address == "" {
		c.Address = d.Address
	}
	if c.SPISpeedHz <= 0 {
		c.SPISpeedHz = d.SPISpeedHz
	}
	if c.AccelRangeG == 0 {
		c.AccelRangeG = d.AccelRangeG
	}
	if c.GyroRangeDegS == 0 {
		c.GyroRangeDegS = d.GyroRangeDegS
	}
	if c.AverageMs <= 0 {
		c.AverageMs = d.AverageMs
	}
	if c.StaleAfterMs <= 0 {
		c.StaleAfterMs = d.StaleAfterMs
	}
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	get := components.HTTPMethods("GET")
	service := func(def, form, unit, kind, what string, heartbeat int, threshold, finest float64) *components.Service {
		return &components.Service{
			Definition: def,
			SubPath:    def,
			Details: map[string][]string{
				"Forms":        {form},
				"Unit":         {unit},
				"QuantityKind": {kind},
				"Methods":      get,
			},
			RegPeriod: 30,
			// Followable: a consumer hears when the value moves by the
			// threshold, measured for a vector as how far it moved. The
			// thresholds are a few times the noise of a 100 ms average; the
			// finest is about the noise, below which a change is not one.
			SubscribeAble:    true,
			Heartbeat:        heartbeat,
			Threshold:        threshold,
			FastestHeartbeat: 1,
			FinestThreshold:  finest,
			Description:      what + " (GET), or streamed as it changes (GET with Accept: text/event-stream)",
		}
	}
	services := components.Services{}
	for _, s := range []*components.Service{
		service("acceleration", "VectorA_v1a", unitAccel, qkAccel,
			"the specific force in the vehicle frame, m/s²: standing level it reads 9.81 up, not zero", 10, 0.05, 0.01),
		service("angularvelocity", "VectorA_v1a", unitRadPerS, qkAngularVel,
			"the rate of turn about each axis of the vehicle frame, rad/s, positive counter-clockwise", 10, 0.005, 0.001),
		service("magneticfield", "VectorA_v1a", unitMicroT, qkMagFluxDens,
			"the magnetic flux density in the vehicle frame, µT, uncalibrated: not yet a compass", 10, 0.5, 0.15),
		service("dietemperature", "SignalA_v1a", unitDegC, qkTemperature,
			"the temperature of the sensor's die, not of the air around it", 30, 0.1, 0.01),
	} {
		services[s.SubPath] = s
	}

	cfg := defaultConfig()
	return &components.UnitAsset{
		Name:        "imu",
		Mission:     components.MissionMeasurement,
		Mobility:    components.MobilityMovable,
		Details:     map[string][]string{"Model": {"ICM-20948"}},
		ServicesMap: services,
		Traits:      &cfg,
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// sampler is a chip that can be read: the ICM-20948, or a test's stand-in.
type sampler interface {
	sample() (rawSample, error)
}

// Traits is one chip at run time.
type Traits struct {
	cfg SpikerConfig
	rot rotation
	ua  *components.UnitAsset

	mu   sync.Mutex
	now  reading // the last window's means
	zero gyroZero

	failures  int // reads that failed since the last report
	lastError error
}

// reading is the means of one window, in the vehicle frame.
type reading struct {
	at     time.Time
	accel  [3]float64
	gyro   [3]float64
	mag    [3]float64
	tempC  float64
	magOK  bool // at least one good magnetometer sample in the window
	gyroOK bool // the gyroscope's zero is known, so the rate can be served
}

// newResource opens the bus, starts the chip and begins sampling.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	cfg := defaultConfig()
	if len(uac.Traits) > 0 {
		if err := json.Unmarshal(uac.Traits[0], &cfg); err != nil {
			log.Fatalf("spiker: %s: cannot parse traits: %v", uac.Name, err)
		}
	}
	applyDefaults(&cfg)

	b, err := openBus(cfg)
	if err != nil {
		log.Fatalf("spiker: %s: %v", uac.Name, err)
	}
	chip, err := startICM20948(b, cfg.Bus == "spi", cfg.AccelRangeG, cfg.GyroRangeDegS)
	if err != nil {
		b.close()
		log.Fatalf("spiker: %s: no ICM-20948 on %s %s: %v\n%s", uac.Name, cfg.Bus, where(cfg), err, hint(cfg))
	}
	log.Printf("spiker: %s: ICM-20948 on %s %s, ±%d g, ±%d °/s, the magnetometer answering",
		uac.Name, cfg.Bus, where(cfg), cfg.AccelRangeG, cfg.GyroRangeDegS)

	t := newTraits(cfg)
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
	t.ua = ua

	go t.run(sys.Ctx, chip, uac.Name)

	return ua, func() {
		log.Printf("closing the %s bus of %s\n", cfg.Bus, uac.Name)
		b.close()
	}
}

func newTraits(cfg SpikerConfig) *Traits {
	return &Traits{
		cfg:  cfg,
		rot:  cfg.Mount.rotation(),
		zero: gyroZero{duration: time.Duration(cfg.GyroZeroSeconds * float64(time.Second))},
	}
}

// openBus opens what the configuration names.
func openBus(cfg SpikerConfig) (bus, error) {
	switch cfg.Bus {
	case "i2c":
		addr, err := strconv.ParseUint(cfg.Address, 0, 8)
		if err != nil || (addr != 0x68 && addr != 0x69) {
			return nil, fmt.Errorf("the I2C address %q is not one an ICM-20948 answers at: 0x69, or 0x68 with the address jumper closed", cfg.Address)
		}
		return openI2C(cfg.Device, uint16(addr))
	case "spi":
		return openSPI(cfg.Device, uint32(cfg.SPISpeedHz))
	}
	return nil, fmt.Errorf("the bus is %q; it is \"i2c\" or \"spi\"", cfg.Bus)
}

func where(cfg SpikerConfig) string {
	if cfg.Bus == "i2c" {
		return cfg.Device + " at " + cfg.Address
	}
	return cfg.Device
}

// hint says what to check, because "no such device" names the symptom and
// not the switch that is off.
func hint(cfg SpikerConfig) string {
	if cfg.Bus == "spi" {
		return "Check that SPI is enabled (raspi-config, or dtparam=spi=on in config.txt), that this user is in the spi group, and the wiring to the pads on the breakout's back."
	}
	return "Check that I2C is enabled (raspi-config, or dtparam=i2c_arm=on in config.txt), that this user is in the i2c group, and the Qwiic cable; i2cdetect -y 1 should show 69 (or 68)."
}

//-------------------------------------Sampling

// run reads the chip on its clock, averages each window, and publishes it.
func (t *Traits) run(ctx context.Context, chip sampler, name string) {
	tick := time.NewTicker(pollPeriod)
	defer tick.Stop()
	report := time.NewTicker(10 * time.Second)
	defer report.Stop()

	var w window
	for {
		select {
		case <-ctx.Done():
			return
		case <-report.C:
			t.reportFailures(name)
		case now := <-tick.C:
			s, err := chip.sample()
			if err != nil {
				t.mu.Lock()
				t.failures++
				t.lastError = err
				t.mu.Unlock()
				continue
			}
			t.mu.Lock()
			t.zero.add(now, s.gyro, name)
			t.mu.Unlock()
			w.add(now, s)
			if now.Sub(w.start) >= time.Duration(t.cfg.AverageMs)*time.Millisecond {
				t.store(w.mean())
				w = window{}
			}
		}
	}
}

// reportFailures logs the reads that failed in the last ten seconds, once:
// one bad read on a physical bus is weather, a hundred a second is news, and
// neither needs a line per read.
func (t *Traits) reportFailures(name string) {
	t.mu.Lock()
	n, err := t.failures, t.lastError
	t.failures = 0
	t.mu.Unlock()
	if n > 0 {
		log.Printf("spiker: %s: %d reads failed in the last ten seconds, the last with: %v", name, n, err)
	}
}

// window gathers the samples of one average.
type window struct {
	start   time.Time
	n, nMag int
	tSum    float64 // seconds after start, summed, for the mean time
	accel   [3]float64
	gyro    [3]float64
	mag     [3]float64
	tempC   float64
}

func (w *window) add(at time.Time, s rawSample) {
	if w.n == 0 {
		w.start = at
	}
	w.n++
	w.tSum += at.Sub(w.start).Seconds()
	for k := 0; k < 3; k++ {
		w.accel[k] += s.accel[k]
		w.gyro[k] += s.gyro[k]
	}
	w.tempC += s.tempC
	if s.magGood {
		w.nMag++
		for k := 0; k < 3; k++ {
			w.mag[k] += s.mag[k]
		}
	}
}

// mean is the window's average, in the chip's axes, stamped at the mean time
// of its samples: an average is of the middle of the window, not its end.
func (w *window) mean() (rawSample, time.Time) {
	var m rawSample
	n := float64(w.n)
	for k := 0; k < 3; k++ {
		m.accel[k] = w.accel[k] / n
		m.gyro[k] = w.gyro[k] / n
		if w.nMag > 0 {
			m.mag[k] = w.mag[k] / float64(w.nMag)
		}
	}
	m.tempC = w.tempC / n
	m.magGood = w.nMag > 0
	return m, w.start.Add(time.Duration(w.tSum / n * float64(time.Second)))
}

// store turns a window's mean into the vehicle frame, takes off the
// gyroscope's zero, keeps it as the reading served, and publishes it.
func (t *Traits) store(m rawSample, at time.Time) {
	t.mu.Lock()
	zero, known := t.zero.value()
	r := reading{at: at, tempC: m.tempC, magOK: m.magGood, gyroOK: known}
	var gyro [3]float64
	for k := 0; k < 3; k++ {
		gyro[k] = m.gyro[k] - zero[k]
	}
	r.accel = t.rot.apply(m.accel)
	r.gyro = t.rot.apply(gyro)
	r.mag = t.rot.apply(m.mag)
	t.now = r
	t.mu.Unlock()

	t.publish(r)
}

// publish hands the reading to whoever follows each service. What is not
// known is not published: a rate before its zero is measured, a field with no
// good magnetometer sample in the window.
func (t *Traits) publish(r reading) {
	if t.ua == nil {
		return
	}
	usecases.Publish(t.ua, "acceleration", vectorForm(r.accel, unitAccel, r.at))
	if r.gyroOK {
		usecases.Publish(t.ua, "angularvelocity", vectorForm(r.gyro, unitRadPerS, r.at))
	}
	if r.magOK {
		usecases.Publish(t.ua, "magneticfield", vectorForm(r.mag, unitMicroT, r.at))
	}
	usecases.Publish(t.ua, "dietemperature", signalForm(r.tempC, unitDegC, r.at))
}

func vectorForm(v [3]float64, unit string, at time.Time) *forms.VectorA_v1a {
	var f forms.VectorA_v1a
	f.NewForm()
	f.X, f.Y, f.Z = v[0], v[1], v[2]
	f.Unit = unit
	f.Frame = forms.VectorVehicle
	f.Timestamp = at
	return &f
}

func signalForm(v float64, unit string, at time.Time) *forms.SignalA_v1a {
	var f forms.SignalA_v1a
	f.NewForm()
	f.Value = v
	f.Unit = unit
	f.Timestamp = at
	return &f
}

//-------------------------------------The gyroscope's zero

// gyroZero is what the gyroscope reads when nothing turns.
//
// It is not zero, and integrated it is a heading that turns by itself: the
// Mid-360's read 1.3°/s about the vertical. So it is measured first, the
// vehicle standing still, and subtracted from every reading after. If the
// samples spread more than a still gyroscope's would, the vehicle moved; the
// measurement starts again, and the rate is not served until it succeeds.
type gyroZero struct {
	duration time.Duration
	known    bool
	zero     [3]float64

	start time.Time
	n     float64
	sum   [3]float64
	sq    [3]float64
}

// stillSpread is the most a still gyroscope's readings spread, one standard
// deviation in rad/s. The ICM-20948's noise at a 50 Hz bandwidth is under
// 0.1°/s; five times that is a vehicle that moved.
const stillSpread = 0.5 * math.Pi / 180

func (z *gyroZero) value() ([3]float64, bool) {
	if z.duration <= 0 {
		return [3]float64{}, true
	}
	return z.zero, z.known
}

func (z *gyroZero) add(at time.Time, gyro [3]float64, name string) {
	if z.duration <= 0 || z.known {
		return
	}
	if z.n == 0 {
		z.start = at
	}
	z.n++
	for k := 0; k < 3; k++ {
		z.sum[k] += gyro[k]
		z.sq[k] += gyro[k] * gyro[k]
	}
	if at.Sub(z.start) < z.duration {
		return
	}
	var mean, spread [3]float64
	still := true
	for k := 0; k < 3; k++ {
		mean[k] = z.sum[k] / z.n
		spread[k] = math.Sqrt(math.Max(0, z.sq[k]/z.n-mean[k]*mean[k]))
		still = still && spread[k] < stillSpread
	}
	z.n, z.sum, z.sq = 0, [3]float64{}, [3]float64{}
	if !still {
		log.Printf("spiker: %s: moved while the gyroscope's zero was measured (spread %.2f, %.2f, %.2f °/s); measuring again",
			name, spread[0]*180/math.Pi, spread[1]*180/math.Pi, spread[2]*180/math.Pi)
		return
	}
	z.zero, z.known = mean, true
	log.Printf("spiker: %s: the gyroscope's zero is %.3f, %.3f, %.3f °/s about the chip's x, y, z; it is subtracted from every reading",
		name, mean[0]*180/math.Pi, mean[1]*180/math.Pi, mean[2]*180/math.Pi)
}

//-------------------------------------Service handlers

// serving handles the resources services. NOTE: it expects those names from the request URL path
func serving(t *Traits, w http.ResponseWriter, r *http.Request, servicePath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	t.mu.Lock()
	now := t.now
	t.mu.Unlock()

	// 503 rather than a number for what is not known, so a consumer comes
	// back: a zero acceleration or rate is a reading somebody will act on.
	if now.at.IsZero() || time.Since(now.at) > time.Duration(t.cfg.StaleAfterMs)*time.Millisecond {
		http.Error(w, "no recent reading from the sensor", http.StatusServiceUnavailable)
		return
	}
	switch servicePath {
	case "acceleration":
		usecases.HTTPProcessGetRequest(w, r, vectorForm(now.accel, unitAccel, now.at))
	case "angularvelocity":
		if !now.gyroOK {
			http.Error(w, "the gyroscope's zero is still being measured; it needs the vehicle to stand still", http.StatusServiceUnavailable)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, vectorForm(now.gyro, unitRadPerS, now.at))
	case "magneticfield":
		if !now.magOK {
			http.Error(w, "no good magnetometer reading in the last window", http.StatusServiceUnavailable)
			return
		}
		usecases.HTTPProcessGetRequest(w, r, vectorForm(now.mag, unitMicroT, now.at))
	case "dietemperature":
		usecases.HTTPProcessGetRequest(w, r, signalForm(now.tempC, unitDegC, now.at))
	default:
		http.Error(w, "Invalid service request [Do not modify the services subpath in the configuration file]", http.StatusBadRequest)
	}
}
