/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/forms"
)

// TestInitTemplate verifies that initTemplate returns a UnitAsset with the
// expected name and that the "temperature" service sub-path is registered.
func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "sensor_Id" {
		t.Errorf("expected Name %q, got %q", "sensor_Id", ua.Name)
	}

	if _, ok := ua.ServicesMap["temperature"]; !ok {
		t.Error("expected a service registered under sub-path \"temperature\", but none found")
	}
}

// TestReadTemp_GET starts a goroutine that acts as the measurement manager: it
// drains the tray channel and replies with a populated SignalA_v1a form, then
// verifies that the handler returns HTTP 200.
//
// TestReadTemp_Default verifies that any non-GET method is answered with 404.
func TestReadTemp(t *testing.T) {
	t.Run("GET returns 200", func(t *testing.T) {
		tray := make(chan STray, 1)
		tr := &Traits{
			trayChan: tray,
		}

		// Simulate the measurement manager: receive a request and reply with data.
		go func() {
			order := <-tray
			var f forms.SignalA_v1a
			f.NewForm()
			f.Value = 21.5
			f.Unit = "Celsius"
			f.Timestamp = time.Now()
			order.ValueP <- f
		}()

		req := httptest.NewRequest(http.MethodGet, "/temperature", nil)
		w := httptest.NewRecorder()
		tr.readTemp(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}
	})

	t.Run("non-GET returns 404", func(t *testing.T) {
		tray := make(chan STray, 1)
		tr := &Traits{
			trayChan: tray,
		}

		req := httptest.NewRequest(http.MethodDelete, "/temperature", nil)
		w := httptest.NewRecorder()
		tr.readTemp(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", resp.StatusCode)
		}
	})
}

// TestParseDeviceFileSurvivesABadSensor is the defect this test was written for:
// the reader indexed the second line of the device file guarded only by a check
// that the file was non-empty. A CRC failure or a sensor unplugged mid-read
// yields one line, and the index panicked the reader goroutine and took the
// system with it — every two seconds, against hardware this code does not
// control.
func TestParseDeviceFileSurvivesABadSensor(t *testing.T) {
	good := "6a 01 4b 46 7f ff 0c 10 07 : crc=07 YES\n6a 01 4b 46 7f ff 0c 10 07 t=22625\n"

	if got, err := parseDeviceFile([]byte(good)); err != nil {
		t.Fatalf("a good reading was refused: %v", err)
	} else if got < 22.62 || got > 22.63 {
		t.Errorf("t=22625 read as %v, want 22.625", got)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"one line, as a CRC failure leaves it", "6a 01 4b 46 7f ff 0c 10 07 : crc=07 NO"},
		{"empty, as an unplugged sensor leaves it", ""},
		{"a second line with no reading", "crc=07 YES\nnothing here\n"},
		{"a reading that is not a number", "crc=07 YES\n... t=hot\n"},
		// The power-on default. A chip that reset mid-read reports 85 C, which
		// is a plausible number and would drive a control loop.
		{"the 85 C power-on default", "crc=07 YES\n... t=85000\n"},
		{"below what the part can measure", "crc=07 YES\n... t=-60000\n"},
	} {
		if _, err := parseDeviceFile([]byte(tc.raw)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// TestAGetDuringShutdownIsRefusedNotAPanic is the other half of the defect:
// readTemperature closed trayChan when the context was canceled, while the HTTP
// handler sent on it unguarded. main cancels the context and then sleeps two
// seconds with the servers still accepting, so any GET in that window sent on a
// closed channel — which panics and takes the system down, on hardware in the
// field, on an ordinary Ctrl-C.
func TestAGetDuringShutdownIsRefusedNotAPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Traits{trayChan: make(chan STray), ctx: ctx}

	stopped := make(chan struct{})
	go func() {
		tr.readTemperature(ctx)
		close(stopped)
	}()

	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("readTemperature did not return after the context was canceled")
	}

	// The window main leaves open: the reader has gone, the server has not.
	w := httptest.NewRecorder()
	tr.readTemp(w, httptest.NewRequest(http.MethodGet, "/temperature", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("a GET during shutdown returned %d, want 503", w.Code)
	}
}

// TestNoReadingIsNotZeroDegrees is follow-up finding N7: temperature and tStamp
// stay at their zero values until the reader succeeds, and the tray handler
// answered regardless. A thermostat given 0 °C computes an error of twenty and
// holds the valve wide open, and nothing in the reading distinguishes that from
// a genuinely cold room.
//
// The bounds check added earlier made it worse rather than better: a chip stuck
// at its 85 °C power-on value is now refused on every tick, so the asset would
// have served 0.000 for the life of the process while its own log said it was
// discarding bad readings.
func TestNoReadingIsNotZeroDegrees(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &Traits{
		trayChan: make(chan STray),
		name:     "28-00000f030cef",
		ctx:      ctx,
		unit:     celsius(),
	}
	go tr.readTemperature(ctx)

	// Nothing has been read: the reader is looking at a device file that is not
	// there, so tStamp is still zero.
	w := httptest.NewRecorder()
	tr.readTemp(w, httptest.NewRequest(http.MethodGet, "/temperature", nil))

	if w.Code == http.StatusOK {
		t.Fatalf("a sensor that has never read served %q", strings.TrimSpace(w.Body.String()))
	}
	// Not ready, rather than broken: a control loop should come back.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d; want 503 so a consumer retries rather than gives up", w.Code)
	}

	// Once a reading lands, it is served.
	tr.temperature = 21.5
	tr.tStamp = time.Now()

	w = httptest.NewRecorder()
	tr.readTemp(w, httptest.NewRequest(http.MethodGet, "/temperature", nil))
	if w.Code != http.StatusOK {
		t.Errorf("a real reading was refused with %d: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if !strings.Contains(w.Body.String(), "21.5") {
		t.Errorf("the reading did not reach the caller: %s", strings.TrimSpace(w.Body.String()))
	}
}

// A bus that fails once must not cost a sample.
//
// 1-Wire is one data line, often metres of it, with no flow control. The kernel
// driver returns an empty file when it cannot get a clean answer, and on
// AlphaCloud that happened every few minutes — each time costing a reading and
// a log line that looked like a fault.
func TestATransientBusFailureIsRetried(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w1_slave")

	// First read empty, second read good: what a briefly confused bus looks
	// like. The file is rewritten by a watcher so the second attempt differs.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = os.WriteFile(path,
			[]byte("6f 01 4b 46 7f ff 01 10 67 : crc=67 YES\n6f 01 4b 46 7f ff 01 10 67 t=22937\n"),
			0o644)
	}()

	got, err := readOnce(path)
	if err != nil {
		t.Fatalf("a bus that answered on the second attempt still failed: %v", err)
	}
	if got < 22.9 || got > 23.0 {
		t.Errorf("read %.3f C, want about 22.937", got)
	}
}

// And a sensor that is genuinely gone still reports, rather than retrying for
// ever or claiming a temperature.
func TestASensorThatIsGoneIsReported(t *testing.T) {
	_, err := readOnce(filepath.Join(t.TempDir(), "not-there"))
	if err == nil {
		t.Fatal("a missing sensor produced a reading")
	}
}

// Two attempts, not three: reading the file starts a conversion that takes up
// to 750 ms, and the sampler runs every two seconds. A third attempt would
// overrun the period, which is a worse fault than a missed reading.
func TestTheRetryFitsInsideTheSamplingPeriod(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w1_slave")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := readOnce(path); err == nil {
		t.Fatal("an always-empty file produced a reading")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a failed read took %v; the sampler ticks every 2s and each attempt "+
			"costs a conversion, so this must stay well inside that", elapsed)
	}
}
