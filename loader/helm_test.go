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
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

var (
	pad    = caller{name: "gamepad", known: true}
	auto   = caller{name: "driver", known: true}
	other  = caller{name: "painter", known: true}
	nobody = caller{}
)

func TestTheVehicleStartsStopped(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	if err := h.command(pad, time.Now()); err == nil {
		t.Error("a freshly started vehicle accepted a command")
	}
	if _, err := h.take(auto, time.Now()); err == nil {
		t.Error("a system without priority took control of a vehicle that had never been handed over")
	}
	if _, err := h.take(pad, time.Now()); err != nil {
		t.Errorf("the gamepad could not take control at start: %v", err)
	}
}

// The handover: the person takes control, then gives it to the software.
func TestHandover(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	now := time.Now()
	h.take(pad, now)
	if err := h.command(auto, now); err == nil {
		t.Error("a system without control was obeyed")
	}
	if _, err := h.take(auto, now); err == nil {
		t.Error("a system without priority took control from the gamepad")
	}
	if changed, err := h.release(pad); err != nil || !changed {
		t.Fatalf("the gamepad could not release control: %v", err)
	}
	if h.stopped {
		t.Fatal("releasing control stopped the vehicle; it should be free to be taken")
	}
	if _, err := h.take(auto, now); err != nil {
		t.Fatalf("the driver could not take a released vehicle: %v", err)
	}
	if err := h.command(auto, now); err != nil {
		t.Errorf("the driver in control was refused: %v", err)
	}
	if err := h.command(pad, now); err == nil {
		t.Error("the gamepad was obeyed without taking control back")
	}
}

func TestPriorityTakesOver(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	h.take(pad, time.Now())
	h.release(pad)
	h.take(auto, time.Now())
	if changed, err := h.take(pad, time.Now()); err != nil || !changed {
		t.Fatalf("the gamepad could not take control from the driver: %v", err)
	}
	if err := h.command(auto, time.Now()); err == nil {
		t.Error("the driver was still obeyed after the gamepad took over")
	}
}

func TestOnlyThePilotReleases(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	h.take(pad, time.Now())
	if _, err := h.release(auto); err == nil {
		t.Error("a system without control released it")
	}
	if !h.holds(pad) {
		t.Error("the gamepad lost control to someone else's release")
	}
}

// Anyone may stop, a stop takes control from everyone, and the software it
// stopped cannot undo it.
func TestAStopIsNotUndoneByTheSoftwareItStopped(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	h.take(pad, time.Now())
	h.release(pad)
	h.take(auto, time.Now())

	h.stop(other, "stopped")
	if err := h.command(auto, time.Now()); err == nil {
		t.Error("the driver was obeyed after a stop")
	}
	if _, err := h.take(auto, time.Now()); err == nil {
		t.Error("the driver took control back after being stopped")
	}
	_, err := h.take(auto, time.Now())
	if err == nil || !strings.Contains(err.Error(), "painter") {
		t.Errorf("the refusal does not say who stopped it: %v", err)
	}
	if _, err := h.take(pad, time.Now()); err != nil {
		t.Errorf("the gamepad could not take control after a stop: %v", err)
	}
}

// A stop from a system that is not driving still stops: the supervisor case.
func TestASupervisorStopsAVehicleItIsNotDriving(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	h.take(pad, time.Now())
	h.release(pad)
	h.take(auto, time.Now())
	h.stop(pad, "stopped")
	if !h.stopped || h.held {
		t.Error("a stop from the gamepad did not stop a vehicle the driver had")
	}
}

func TestSilenceIsAStopNotAHandover(t *testing.T) {
	h := newHelm([]string{"gamepad"})
	start := time.Now()
	h.take(pad, start)
	if h.silent(start.Add(400*time.Millisecond), 500*time.Millisecond) {
		t.Fatal("stopped before the limit")
	}
	h.command(pad, start.Add(400*time.Millisecond))
	if h.silent(start.Add(800*time.Millisecond), 500*time.Millisecond) {
		t.Fatal("a command did not reset the silence")
	}
	if !h.silent(start.Add(1000*time.Millisecond), 500*time.Millisecond) {
		t.Fatal("half a second of silence did not stop the vehicle")
	}
	if !h.stopped || h.held {
		t.Error("silence left someone in control")
	}
	if !strings.Contains(h.why, "gamepad") {
		t.Errorf("the reason does not name who went silent: %q", h.why)
	}
	if _, err := h.take(auto, time.Now()); err == nil {
		t.Error("a system without priority took control after the pilot went silent")
	}
}

func TestUnidentifiedCallers(t *testing.T) {
	// A loader with a certificate: a caller without one may only stop.
	h := newHelm([]string{"gamepad"})
	if _, err := h.take(nobody, time.Now()); err == nil {
		t.Error("an unidentified caller took control of a certified loader")
	}
	h.take(pad, time.Now())
	h.stop(nobody, "stopped")
	if !h.stopped {
		t.Error("an unidentified caller could not stop the vehicle")
	}

	// A bench with no certificates at all: nobody can be told apart, so an
	// unidentified caller drives.
	bench := newHelm([]string{"gamepad"})
	bench.anonymousPilot = true
	if _, err := bench.take(nobody, time.Now()); err != nil {
		t.Fatalf("an unidentified caller could not take control on a bench with no certificates: %v", err)
	}
	if err := bench.command(nobody, time.Now()); err != nil {
		t.Errorf("the unidentified pilot was refused: %v", err)
	}
}

//-------------------------------------Through the services

func testDrivetrain(t *testing.T, certified bool) *drivetrain {
	t.Helper()
	sys := components.NewSystem("loader", context.Background())
	sys.Husk = &components.Husk{}
	if certified {
		close(usecases.EnsureCertReady(&sys))
	}
	d := &drivetrain{
		cfg: LoaderConfig{MaxWheelRPM: 120, AccelStep: 150, BrakeStep: 500, SafetyStopMs: 500,
			Motors: []MotorSpec{{Name: "FrontLeft", NodeID: 1, Kind: "wheel"}}},
		sys:      &sys,
		fb:       newFeedback(time.Second),
		helm:     newHelm([]string{"gamepad"}),
		setpoint: map[int]float64{},
		last:     map[int]int16{},
	}
	return d
}

func as(r *http.Request, cn string) *http.Request {
	if cn == "" {
		return r
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}}}
	return r
}

func put(t *testing.T, handler func(http.ResponseWriter, *http.Request), cn string, value string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"value": ` + value + `, "version": "SignalA_v1.0"}`
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, as(r, cn))
	return w
}

func TestServicesArbitrate(t *testing.T) {
	d := testDrivetrain(t, true)
	vehicle := &Traits{Name: "Vehicle", Kind: "vehicle", dt: d, encoderIndex: -1}
	wheel := &Traits{Name: "FrontLeft", NodeID: 1, Kind: "wheel", dt: d, encoderIndex: 0}

	if w := put(t, wheel.setpointService, "gamepad", "30"); w.Code != http.StatusConflict {
		t.Fatalf("a setpoint before anyone took control got %d", w.Code)
	}
	if w := put(t, vehicle.controlService, "gamepad", "1"); w.Code != http.StatusOK {
		t.Fatalf("the gamepad taking control got %d: %s", w.Code, w.Body)
	}
	if w := put(t, wheel.setpointService, "gamepad", "30"); w.Code != http.StatusOK {
		t.Fatalf("the gamepad's setpoint got %d: %s", w.Code, w.Body)
	}
	if w := put(t, wheel.setpointService, "driver", "60"); w.Code != http.StatusConflict {
		t.Errorf("the driver's setpoint while the gamepad had control got %d", w.Code)
	}
	if d.setpoint[1] != 30 {
		t.Errorf("setpoint = %v, want the gamepad's 30", d.setpoint[1])
	}

	// Ramp up a little, then stop from a system that is not driving.
	d.writeCycle()
	if d.last[1] == 0 {
		t.Fatal("the wheel did not start moving")
	}
	if w := put(t, vehicle.stopService, "painter", "1"); w.Code != http.StatusOK {
		t.Fatalf("a stop got %d", w.Code)
	}
	if d.last[1] != 0 || d.setpoint[1] != 0 {
		t.Errorf("the stop did not zero the motor at once: last=%d setpoint=%v", d.last[1], d.setpoint[1])
	}
	d.writeCycle()
	if d.last[1] != 0 {
		t.Errorf("the motor moved after a stop: %d", d.last[1])
	}
	if w := put(t, wheel.setpointService, "gamepad", "30"); w.Code != http.StatusConflict {
		t.Errorf("a setpoint after a stop got %d", w.Code)
	}
	if w := put(t, vehicle.stopService, "painter", "0"); w.Code != http.StatusConflict {
		t.Errorf("un-stopping by writing 0 got %d", w.Code)
	}
	if w := put(t, vehicle.controlService, "driver", "1"); w.Code != http.StatusConflict {
		t.Errorf("the driver taking control after a stop got %d", w.Code)
	}
	if w := put(t, vehicle.controlService, "gamepad", "1"); w.Code != http.StatusOK {
		t.Errorf("the gamepad taking control after a stop got %d", w.Code)
	}
}

// A certified loader is not driven by a caller who presents no certificate,
// though that caller may still stop it.
func TestAnUnidentifiedCallerOnlyStopsACertifiedLoader(t *testing.T) {
	d := testDrivetrain(t, true)
	vehicle := &Traits{Name: "Vehicle", Kind: "vehicle", dt: d, encoderIndex: -1}
	if w := put(t, vehicle.controlService, "", "1"); w.Code != http.StatusConflict {
		t.Errorf("an unidentified caller taking control got %d", w.Code)
	}
	put(t, vehicle.controlService, "gamepad", "1")
	if w := put(t, vehicle.stopService, "", "1"); w.Code != http.StatusOK || !d.helm.stopped {
		t.Errorf("an unidentified stop got %d, stopped=%v", w.Code, d.helm.stopped)
	}
}

// With no certificates anywhere, the bench still works from curl.
func TestABenchWithoutCertificatesCanBeDriven(t *testing.T) {
	d := testDrivetrain(t, false)
	vehicle := &Traits{Name: "Vehicle", Kind: "vehicle", dt: d, encoderIndex: -1}
	wheel := &Traits{Name: "FrontLeft", NodeID: 1, Kind: "wheel", dt: d, encoderIndex: 0}
	if w := put(t, vehicle.controlService, "", "1"); w.Code != http.StatusOK {
		t.Fatalf("taking control on a bench got %d: %s", w.Code, w.Body)
	}
	if w := put(t, wheel.setpointService, "", "25"); w.Code != http.StatusOK {
		t.Errorf("a setpoint on a bench got %d: %s", w.Code, w.Body)
	}
}

func TestServicesForTheVehicle(t *testing.T) {
	configured := []components.Service{
		{Definition: "setpoint", SubPath: "setpoint"},
		{Definition: "travel", SubPath: "travel"},
		{Definition: "control", SubPath: "control"},
		{Definition: "stop", SubPath: "stop"},
	}
	v := servicesFor("vehicle", configured)
	if len(v) != 2 || v["control"] == nil || v["stop"] == nil {
		t.Errorf("the vehicle offers %v, want control and stop only", keys(v))
	}
	w := servicesFor("wheel", configured)
	if w["control"] != nil || w["stop"] != nil {
		t.Errorf("a wheel offers %v; control belongs to the vehicle", keys(w))
	}
}

func keys(s components.Services) []string {
	var out []string
	for k := range s {
		out = append(out, k)
	}
	return out
}
