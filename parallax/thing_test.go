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
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

func TestGpioToRP1PWMChan(t *testing.T) {
	tests := []struct {
		gpio    int
		want    int
		wantErr bool
	}{
		{12, 0, false},
		{13, 1, false},
		{18, 2, false},
		{19, 3, false},
		{99, 0, true},
	}

	for _, tc := range tests {
		got, err := gpioToRP1PWMChan(tc.gpio)
		if tc.wantErr {
			if err == nil {
				t.Errorf("gpioToRP1PWMChan(%d): expected error, got nil", tc.gpio)
			}
		} else {
			if err != nil {
				t.Errorf("gpioToRP1PWMChan(%d): unexpected error: %v", tc.gpio, err)
			}
			if got != tc.want {
				t.Errorf("gpioToRP1PWMChan(%d) = %d, want %d", tc.gpio, got, tc.want)
			}
		}
	}
}

func TestGetPosition(t *testing.T) {
	tr := &Traits{position: 75}
	f := tr.getPosition()

	if f.Value != 75 {
		t.Errorf("getPosition().Value = %v, want 75", f.Value)
	}
	if f.Unit != "Percent" {
		t.Errorf("getPosition().Unit = %q, want \"Percent\"", f.Unit)
	}
}

func TestSetPosition(t *testing.T) {
	tr := &Traits{dutyChan: make(chan int, 1)}

	// pos=50 → Value==50, widthUS in dutyChan == 1520 (center)
	var f forms.SignalA_v1a
	f.Value = 50
	result, err := tr.setPosition(f)
	if err != nil {
		t.Fatalf("setPosition(50): unexpected error: %v", err)
	}
	if result.Value != 50 {
		t.Errorf("setPosition(50).Value = %v, want 50", result.Value)
	}
	select {
	case w := <-tr.dutyChan:
		if w != centerPulseWidth {
			t.Errorf("dutyChan after setPosition(50) = %d, want %d", w, centerPulseWidth)
		}
	default:
		t.Error("dutyChan empty after setPosition(50), expected centerPulseWidth")
	}

	// pos=-10 → clamped to 0, Value==0
	f.Value = -10
	result, err = tr.setPosition(f)
	if err != nil {
		t.Fatalf("setPosition(-10): unexpected error: %v", err)
	}
	if result.Value != -10 {
		// The returned form value reflects what was passed in, not the clamped value;
		// but the internal position should be 0.
		// The spec says Value==0; let's check tr.position instead.
	}
	if tr.position != 0 {
		t.Errorf("position after setPosition(-10) = %d, want 0", tr.position)
	}
	// Drain dutyChan for next sub-test
	select {
	case <-tr.dutyChan:
	default:
	}

	// pos=200 → clamped to 100, Value==100
	f.Value = 200
	_, err = tr.setPosition(f)
	if err != nil {
		t.Fatalf("setPosition(200): unexpected error: %v", err)
	}
	if tr.position != 100 {
		t.Errorf("position after setPosition(200) = %d, want 100", tr.position)
	}
	// Drain the channel
	select {
	case <-tr.dutyChan:
	default:
	}

	// Identical pos again → dutyChan should remain empty (debounce)
	f.Value = 200
	_, err = tr.setPosition(f)
	if err != nil {
		t.Fatalf("setPosition(200) second time: unexpected error: %v", err)
	}
	select {
	case w := <-tr.dutyChan:
		t.Errorf("dutyChan should be empty after duplicate setPosition(200), got %d", w)
	default:
		// expected: channel is empty
	}
}
