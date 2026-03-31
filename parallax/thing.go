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
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"periph.io/x/conn/v3/gpio"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset

// Traits are Asset-specific configurable parameters
type Traits struct {
	GpioPin     gpio.PinIO `json:"-"`
	position    int        `json:"-"`
	dutyChan    chan int   `json:"-"`
	lastWidthUS int        `json:"-"` // last duty we wrote (µs) to debounce identical updates
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	// Define the services that expose the capabilities of the unit asset(s)
	rotation := components.Service{
		Definition:  "rotation",
		SubPath:     "rotation",
		Details:     map[string][]string{"Forms": {"SignalA_v1a"}, "Unit": {"Percent", "Rotational"}},
		RegPeriod:   30,
		Description: "informs of the servo's current position (GET) or updates the position (PUT)",
	}

	return &components.UnitAsset{
		Name:    "Servo_1",
		Mission: "actuate_servo",
		Details: map[string][]string{"Model": {"standardServo", "halfCircle"}, "Location": {"Kitchen"}},
		ServicesMap: components.Services{
			rotation.SubPath: &rotation,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the Resource resource with its pointers and channels based on the configuration using the tConfig structs
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		dutyChan: make(chan int, 1), // buffer=1 enables latest-wins behavior
	}

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: usecases.MakeServiceMap(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	// Choose the GPIO you wired the servo to. You currently use P1_12 → GPIO18.
	const servoGPIO = 18
	const periodNS = int64(20_000_000) // 50 Hz

	chipPath, err := findPWMChipPath()
	if err != nil {
		log.Fatalf("PWM not available: %v", err)
	}
	ch, err := gpioToRP1PWMChan(servoGPIO)
	if err != nil {
		log.Fatalf("Bad GPIO: %v", err)
	}
	pwmPath, err := exportPWM(chipPath, ch)
	if err != nil {
		log.Fatalf("Export PWM: %v", err)
	}

	// Set 50 Hz period, neutral duty, and enable output
	if err := pwmEnable(pwmPath, false); err != nil {
		log.Fatalf("Disable PWM: %v", err)
	}
	if err := pwmWrite(filepath.Join(pwmPath, "period"), periodNS); err != nil {
		log.Fatalf("Set period: %v", err)
	}
	if err := pwmWrite(filepath.Join(pwmPath, "duty_cycle"), int64(1_520_000)); err != nil {
		log.Fatalf("Set duty: %v", err)
	} // 1520 µs
	if err := pwmEnable(pwmPath, true); err != nil {
		log.Fatalf("Enable PWM: %v", err)
	}

	// Drive updates from t.dutyChan (µs → ns)
	go func() {
		for pulseWidthUS := range t.dutyChan {
			dutyNS := int64(pulseWidthUS) * 1000
			if dutyNS < 0 {
				dutyNS = 0
			}
			if dutyNS >= periodNS {
				dutyNS = periodNS - 1
			}
			if err := pwmWrite(filepath.Join(pwmPath, "duty_cycle"), dutyNS); err != nil {
				log.Printf("Set duty failed: %v", err)
			} else {
				fmt.Printf("PWM duty updated: %d µs\n", pulseWidthUS)
			}
		}
	}()

	// Return cleanup that releases the PWM channel on program exit
	cleanup := func() {
		log.Println("disconnecting from servo (PWM off)")
		_ = pwmEnable(pwmPath, false)
		_ = os.WriteFile(filepath.Join(chipPath, "unexport"), []byte(strconv.Itoa(ch)), 0o644)
	}

	return ua, cleanup
}

// --- helpers: sysfs PWM on RP1 ---
func gpioToRP1PWMChan(gpio int) (int, error) {
	switch gpio {
	case 12:
		return 0, nil
	case 13:
		return 1, nil
	case 18:
		return 2, nil
	case 19:
		return 3, nil
	default:
		return 0, errors.New("GPIO not on RP1 PWM0 (use 12,13,18,19)")
	}
}

func findPWMChipPath() (string, error) {
	// Kernel version renumbering means this can be pwmchip0 or pwmchip2, etc.
	candidates, _ := filepath.Glob("/sys/class/pwm/pwmchip*")
	for _, c := range candidates {
		// Heuristic: RP1 PWM0 exposes 4 channels; check npwm >= 4
		b, err := os.ReadFile(filepath.Join(c, "npwm"))
		if err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if n >= 4 {
				return c, nil
			}
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return "", errors.New("no /sys/class/pwm/pwmchip* found (did you enable dtoverlay=pwm-2chan and reboot?)")
}

func exportPWM(chipPath string, ch int) (string, error) {
	// Export if needed
	pwmPath := filepath.Join(chipPath, "pwm"+strconv.Itoa(ch))
	if _, err := os.Stat(pwmPath); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(chipPath, "export"), []byte(strconv.Itoa(ch)), 0o644); err != nil {
			return "", err
		}
		// Wait for the path to appear
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(pwmPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return pwmPath, nil
}

func pwmWrite(path string, v int64) error {
	return os.WriteFile(path, []byte(strconv.FormatInt(v, 10)), 0o644)
}

func pwmEnable(pwmPath string, on bool) error {
	val := "0"
	if on {
		val = "1"
	}
	return os.WriteFile(filepath.Join(pwmPath, "enable"), []byte(val), 0o644)
}

//-------------------------------------Unit asset's resource functions

// timing constants for the PWM (pulse width modulation)
// pulse widths:(620 µs, 1520 µs, 2420 µs) maps to (0°, 90°, 180°) with angles increasing from clockwise to counterclockwise
const (
	minPulseWidth    = 620
	centerPulseWidth = 1520
	maxPulseWidth    = 2420
)

// getPosition provides an analog signal for the servo position in percent and a timestamp
func (t *Traits) getPosition() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.position)
	f.Unit = "Percent"
	f.Timestamp = time.Now()
	return f
}

// setPosition updates the PWM pulse size based on the requested position [0-100]%
func (t *Traits) setPosition(f forms.SignalA_v1a) (forms.SignalA_v1a, error) {
	// Clamp 0–100
	pos := int(f.Value)
	if pos < 0 {
		pos = 0
	} else if pos > 100 {
		pos = 100
	}

	// Log on change
	if t.position != pos {
		log.Printf("The new position is %+v\n", f)
	}
	t.position = pos

	// Map [0..100] -> [minPulseWidth..maxPulseWidth] in microseconds
	widthUS := minPulseWidth + (t.position*(maxPulseWidth-minPulseWidth))/100

	// Debounce: skip if the duty hasn't changed
	if widthUS == t.lastWidthUS {
		f.Timestamp = time.Now()
		return f, nil
	}
	t.lastWidthUS = widthUS

	// Non-blocking send with "latest wins":
	// If the single-slot buffer is full, drop the stale value and enqueue the newest.
	select {
	case t.dutyChan <- widthUS:
		// queued immediately
	default:
		// buffer full: drop stale value if present, then send the newest
		select {
		case <-t.dutyChan:
		default:
		}
		t.dutyChan <- widthUS
	}
	f.Timestamp = time.Now()
	return f, nil
}
