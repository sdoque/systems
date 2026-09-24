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
	"errors"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

//-------------------------------------Control and stop

// controlService takes and releases control. GET answers 1 if the caller has
// it, so a pilot can find out whether it still does without trying to drive.
func (t *Traits) controlService(w http.ResponseWriter, r *http.Request) {
	c := t.dt.callerOf(r)
	switch r.Method {
	case http.MethodGet:
		respond(w, t.dt.controlForm(c))
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		d := t.dt
		d.mu.Lock()
		var changed bool
		if sig.Value != 0 {
			previous := d.helm.owner
			wasHeld := d.helm.held
			changed, err = d.helm.take(c, time.Now())
			if changed {
				d.clearSetpointsLocked()
				// A steering fault stopped the vehicle; whoever takes it again
				// has decided the chain and the motor are fit to drive.
				if d.waist.fault != "" {
					log.Printf("loader: %s takes control, clearing the steering fault: %s", c, d.waist.fault)
					d.waist.fault = ""
				}
				if wasHeld {
					log.Printf("loader: %s takes control from %s", c, previous)
				} else {
					log.Printf("loader: %s takes control", c)
				}
			}
		} else {
			changed, err = d.helm.release(c)
			if changed {
				d.clearSetpointsLocked()
				log.Printf("loader: %s releases control; the vehicle is free to be taken", c)
			}
		}
		d.mu.Unlock()
		if err != nil {
			refuse(w, err)
			return
		}
		respond(w, d.controlForm(c))
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// stopService stops the vehicle for anyone who asks. It does not un-stop:
// that is done by taking control, which only a system with priority may do.
func (t *Traits) stopService(w http.ResponseWriter, r *http.Request) {
	c := t.dt.callerOf(r)
	switch r.Method {
	case http.MethodGet:
		respond(w, t.dt.stopForm())
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		if sig.Value == 0 {
			refuse(w, &refusal{"a stop is cleared by taking control, not by un-stopping"})
			return
		}
		d := t.dt
		d.mu.Lock()
		// Repeated stops are normal — a pad sends one every cycle while the
		// buttons are held — and only the first is news.
		already := d.helm.stopped
		d.helm.stop(c, "stopped")
		d.haltLocked()
		d.mu.Unlock()
		if !already {
			log.Printf("loader: STOP from %s", c)
		}
		respond(w, d.stopForm())
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

func (d *drivetrain) controlForm(c caller) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	d.mu.Lock()
	if d.helm.holds(c) {
		f.Value = 1
	}
	d.mu.Unlock()
	f.Timestamp = time.Now()
	return f
}

func (d *drivetrain) stopForm() *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	d.mu.Lock()
	if d.helm.stopped {
		f.Value = 1
	}
	d.mu.Unlock()
	f.Timestamp = time.Now()
	return f
}

// refuse answers a request the helm will not grant.
//
// 409 and not 403, deliberately. A consumer built on this framework reads 401
// and 403 as a stale credential and goes to fetch a new token, which would be
// wrong here: the caller's credential is fine, it is the vehicle's state that
// says no. 409 is a refusal a consumer keeps its binding through.
func refuse(w http.ResponseWriter, err error) {
	var r *refusal
	if errors.As(err, &r) {
		http.Error(w, r.reason, http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func respond(w http.ResponseWriter, f forms.Form) {
	body, err := usecases.Pack(f, "application/json")
	if err != nil {
		log.Printf("loader: packing response: %v", err)
		http.Error(w, "cannot encode the response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		log.Printf("loader: writing response: %v", err)
	}
}

func copyDetails(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

//-------------------------------------Driving the vehicle

// velocityService takes the front axle's speed, in m/s, negative in reverse.
// The loader turns it into four wheel speeds through the kinematics, using the
// articulation the waist actually has.
func (t *Traits) velocityService(w http.ResponseWriter, r *http.Request) {
	d := t.dt
	switch r.Method {
	case http.MethodGet:
		d.mu.Lock()
		v := d.velocity
		d.mu.Unlock()
		respond(w, signal(v, unitMPerS))
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		c := d.callerOf(r)
		v := math.Max(-d.cfg.MaxSpeed, math.Min(d.cfg.MaxSpeed, sig.Value))
		d.mu.Lock()
		if err := d.helm.command(c, time.Now()); err != nil {
			d.mu.Unlock()
			refuse(w, err)
			return
		}
		d.velocity, d.byVelocity = v, true
		d.mu.Unlock()
		respond(w, signal(v, unitMPerS))
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// curvatureService takes the curvature of the front axle's path, in 1/m,
// positive to the left. It needs a calibrated waist and a known effort
// direction: an angle loop on an uncalibrated sensor is a guess driving a
// motor with nothing to stop it.
//
// Refused with 503, not 409, when the waist cannot do it: the caller still has
// control, and a pilot that read 409 as losing it would stop driving for a
// reason that has nothing to do with who is driving.
func (t *Traits) curvatureService(w http.ResponseWriter, r *http.Request) {
	d := t.dt
	switch r.Method {
	case http.MethodGet:
		d.mu.Lock()
		k := d.curvature
		d.mu.Unlock()
		respond(w, signal(k, unitPerM))
	case http.MethodPut:
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		c := d.callerOf(r)
		limit := d.curvatureLimit()
		k := math.Max(-limit, math.Min(limit, sig.Value))
		d.mu.Lock()
		if err := d.helm.command(c, time.Now()); err != nil {
			d.mu.Unlock()
			refuse(w, err)
			return
		}
		_, fresh := d.waistNowLocked(time.Now())
		switch {
		case !d.cfg.Waist.calibrated():
			d.mu.Unlock()
			http.Error(w, "the waist sensor is not calibrated, so there is no angle to steer to; steer by effort on Steering/setpoint", http.StatusServiceUnavailable)
			return
		case d.cfg.Waist.EffortTurnsLeft == 0:
			d.mu.Unlock()
			http.Error(w, "effortTurnsLeft is not set, so the loader does not know which way the steering motor turns the waist", http.StatusServiceUnavailable)
			return
		case !fresh:
			d.mu.Unlock()
			http.Error(w, "no fresh reading from the waist sensor", http.StatusServiceUnavailable)
			return
		}
		d.curvature, d.byAngle = k, true
		d.mu.Unlock()
		respond(w, signal(k, unitPerM))
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

// articulationService is the waist's measured angle in degrees, positive to
// the left.
func (t *Traits) articulationService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	d := t.dt
	d.mu.Lock()
	raw, fresh := d.waistNowLocked(time.Now())
	deg, ok := d.articulationLocked(raw, fresh)
	calibrated := d.cfg.Waist.calibrated()
	d.mu.Unlock()
	if !ok {
		msg := "no fresh reading from the waist sensor"
		if !calibrated {
			msg = "the waist sensor is not calibrated; waist, on the Steering motor, gives its raw count"
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	usecases.HTTPProcessGetRequest(w, r, signal(deg, unitDeg))
}

func (t *Traits) speedLimitService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	usecases.HTTPProcessGetRequest(w, r, signal(t.dt.cfg.MaxSpeed, unitMPerS))
}

func (t *Traits) curvatureLimitService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	usecases.HTTPProcessGetRequest(w, r, signal(t.dt.curvatureLimit(), unitPerM))
}

// distanceService is how far the wheel has rolled, in meters.
func (t *Traits) distanceService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method is not supported.", http.StatusNotFound)
		return
	}
	reading, fresh := t.dt.fb.wheel(t.encoderIndex)
	if !fresh {
		http.Error(w, "no recent encoder frame for this wheel", http.StatusServiceUnavailable)
		return
	}
	usecases.HTTPProcessGetRequest(w, r, distanceForm(reading, t.dt.cfg.Geometry.WheelCircumference))
}

func signal(v float64, unit string) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = v
	f.Unit = unit
	f.Timestamp = time.Now()
	return f
}

//-------------------------------------Following the measurements

// publishFeedback makes the measured services followable in fact as well as in
// name.
//
// speed, travel and waist have always declared themselves subscribable, and
// nothing ever handed their publishers a sample, so a consumer that subscribed
// got a connection and no values. Every encoder frame and every waist reply is
// now a sample.
func (d *drivetrain) publishFeedback(assets []*components.UnitAsset) {
	wheels := make(map[int]*components.UnitAsset)
	var steering, vehicle *components.UnitAsset
	for _, ua := range assets {
		t, ok := ua.Traits.(*Traits)
		if !ok {
			continue
		}
		if t.encoderIndex >= 0 {
			wheels[t.encoderIndex] = ua
		}
		switch t.Kind {
		case "steering":
			steering = ua
		case "vehicle":
			vehicle = ua
		}
	}
	circumference := d.cfg.Geometry.WheelCircumference
	waist := d.cfg.Waist
	d.fb.onWheel = func(i int, r wheelReading) {
		ua := wheels[i]
		if ua == nil {
			return
		}
		usecases.Publish(ua, "speed", speedForm(r))
		usecases.Publish(ua, "travel", travelForm(r))
		usecases.Publish(ua, "distance", distanceForm(r, circumference))
	}
	d.fb.onWaist = func(raw int, at time.Time) {
		if steering != nil {
			usecases.Publish(steering, "waist", waistForm(raw, at))
		}
		if vehicle != nil && waist.calibrated() {
			f := signal(waist.degrees(raw), unitDeg)
			f.Timestamp = at
			usecases.Publish(vehicle, "articulation", f)
		}
	}
}

func speedForm(r wheelReading) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = r.rpm
	f.Unit = unitRPM
	f.Timestamp = r.at
	return f
}

// travelForm carries the time the encoder frame arrived, not the time it was
// asked for. A follower is sent the last sample again on every heartbeat, and
// the timestamp is the only way it can tell a wheel standing still from an
// encoder that has stopped reporting.
func travelForm(r wheelReading) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = r.revolutions
	f.Unit = unitRev
	f.Timestamp = r.at
	return f
}

// distanceForm is the wheel's travel in meters. Like travelForm it carries the
// time the frame arrived.
func distanceForm(r wheelReading, circumference float64) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = r.revolutions * circumference
	f.Unit = unitMetre
	f.Timestamp = r.at
	return f
}

func waistForm(raw int, at time.Time) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = float64(raw)
	f.Timestamp = at
	return f
}

const (
	unitRPM   = "<http://qudt.org/vocab/unit/REV-PER-MIN>"
	unitRev   = "<http://qudt.org/vocab/unit/REV>"
	unitMetre = "<http://qudt.org/vocab/unit/M>"
	unitMPerS = "<http://qudt.org/vocab/unit/M-PER-SEC>"
	unitPerM  = "<http://qudt.org/vocab/unit/PER-M>"
	unitDeg   = "<http://qudt.org/vocab/unit/DEG>"
)
