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
	var steering *components.UnitAsset
	for _, ua := range assets {
		t, ok := ua.Traits.(*Traits)
		if !ok {
			continue
		}
		if t.encoderIndex >= 0 {
			wheels[t.encoderIndex] = ua
		}
		if t.Kind == "steering" {
			steering = ua
		}
	}
	d.fb.onWheel = func(i int, r wheelReading) {
		ua := wheels[i]
		if ua == nil {
			return
		}
		usecases.Publish(ua, "speed", speedForm(r))
		usecases.Publish(ua, "travel", travelForm(r))
	}
	d.fb.onWaist = func(raw int, at time.Time) {
		if steering != nil {
			usecases.Publish(steering, "waist", waistForm(raw, at))
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

func waistForm(raw int, at time.Time) *forms.SignalA_v1a {
	f := &forms.SignalA_v1a{}
	f.NewForm()
	f.Value = float64(raw)
	f.Timestamp = at
	return f
}

const (
	unitRPM = "<http://qudt.org/vocab/unit/REV-PER-MIN>"
	unitRev = "<http://qudt.org/vocab/unit/REV>"
)
