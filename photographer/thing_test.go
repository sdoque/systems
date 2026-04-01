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
	"testing"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Name != "PiCam" {
		t.Errorf("expected Name %q, got %q", "PiCam", ua.Name)
	}

	if ua.Mission != "capture_photographe" {
		t.Errorf("expected Mission %q, got %q", "capture_photographe", ua.Mission)
	}

	if _, ok := ua.ServicesMap["photograph"]; !ok {
		t.Error("expected ServicesMap to have an entry for \"photograph\"")
	}
}
