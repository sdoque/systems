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
	"net/http"
	"net/http/httptest"
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
