/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
	"testing"

	"github.com/sdoque/mbaigo/components"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "PiCam" {
		t.Errorf("expected Name %q, got %q", "PiCam", ua.Name)
	}

	if ua.Mission != "capture_video" {
		t.Errorf("expected Mission %q, got %q", "capture_video", ua.Mission)
	}

	if _, ok := ua.ServicesMap["start"]; !ok {
		t.Error("expected ServicesMap to have an entry for \"start\"")
	}
}

func TestStartStreamURL(t *testing.T) {
	ctx := context.Background()
	sys := components.NewSystem("filmer", ctx)
	sys.Husk = &components.Husk{
		Host:      &components.HostingDevice{IPAddresses: []string{"192.168.1.10"}},
		ProtoPort: map[string]int{"http": 20162},
	}
	tr := &Traits{owner: &sys, name: "cam1"}

	got := tr.StartStreamURL()
	want := "http://192.168.1.10:20162/filmer/cam1/stream"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
