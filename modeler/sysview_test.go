/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
 ***************************************************************************SDG*/

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sysView stands in for SysView's model server and records what was put.
type sysView struct {
	mu    sync.Mutex
	paths []string
	model string
	fail  bool
}

func (s *sysView) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		http.Error(w, "not now", http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.paths = append(s.paths, r.Method+" "+r.URL.EscapedPath())
	s.model = string(body)
	w.WriteHeader(http.StatusNoContent)
}

// A change is put to SysView under the cloud's name; the pass that follows it
// and finds the same cloud puts nothing.
func TestTheModelIsPutToSysViewOncePerChange(t *testing.T) {
	pkg := samplePackage("host_thermostat", "controller", "setpoint", "temperature")
	system := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pkg)
	}))
	defer system.Close()
	registrar := newMockRegistrar(systemRecordListJSON([]string{system.URL}))
	defer registrar.Close()
	view := &sysView{}
	viewSrv := httptest.NewServer(view)
	defer viewSrv.Close()

	tr := newTestTraits(registrar.URL, "cottage cloud")
	tr.SysViewURL = viewSrv.URL + "/"

	if !tr.rebuild() {
		t.Fatal("a pass that read every system reported itself incomplete")
	}
	tr.rebuild() // the look once more, finding nothing new

	if len(view.paths) != 1 || view.paths[0] != "PUT /models/cottage%20cloud" {
		t.Fatalf("SysView was sent %v; want one PUT /models/cottage%%20cloud", view.paths)
	}
	if !strings.Contains(view.model, "package 'cottage cloud'") {
		t.Error("what was put is not the cloud's model")
	}
}

// SysView closed is not a reason to call the model pushed: when it answers
// again, the next pass puts the model, even if the cloud did not change.
func TestAModelSysViewRefusedIsPutAgain(t *testing.T) {
	system := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, samplePackage("host_thermostat", "controller", "setpoint", "temperature"))
	}))
	defer system.Close()
	registrar := newMockRegistrar(systemRecordListJSON([]string{system.URL}))
	defer registrar.Close()
	view := &sysView{fail: true}
	viewSrv := httptest.NewServer(view)
	defer viewSrv.Close()

	tr := newTestTraits(registrar.URL, "c")
	tr.SysViewURL = viewSrv.URL
	tr.rebuild()
	view.fail = false
	tr.rebuild()
	if len(view.paths) != 1 {
		t.Errorf("SysView was sent %v; the model it refused should have been put again", view.paths)
	}
}

// A system that cannot be read makes the pass incomplete, so the follower
// reads the cloud again rather than leaving the system out.
func TestAnUnreadableSystemMakesThePassIncomplete(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the caller presented no verified certificate", http.StatusUnauthorized)
	}))
	defer refusing.Close()
	registrar := newMockRegistrar(systemRecordListJSON([]string{refusing.URL}))
	defer registrar.Close()
	viewSrv := httptest.NewServer(&sysView{})
	defer viewSrv.Close()

	tr := newTestTraits(registrar.URL, "c")
	tr.SysViewURL = viewSrv.URL
	if tr.rebuild() {
		t.Error("a pass that could not read a system reported itself complete")
	}
}
