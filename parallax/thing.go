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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	rpio "github.com/stianeikeland/go-rpio/v4"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// -------------------------------------Define the unit asset

// Traits holds the configurable and runtime state for one servo unit asset.
type Traits struct {
	GpioPin     int        `json:"gpioPin"` // BCM GPIO pin number (default: 18)
	position    int        `json:"-"`
	dutyChan    chan int   `json:"-"`
	lastWidthUS int        `json:"-"`
	backend     pwmBackend `json:"-"`
}

// pwmBackend abstracts the hardware difference between Raspberry Pi 4 (BCM PWM via
// go-rpio) and Raspberry Pi 5 (RP1 hardware PWM via the kernel sysfs interface).
type pwmBackend interface {
	setDuty(widthUS int) error
	close()
}

// ----- Pi 5 / RP1 sysfs backend ---------------------------------------------------

type sysfsBackend struct {
	pwmPath  string
	chipPath string
	ch       int
}

func (s *sysfsBackend) setDuty(widthUS int) error {
	return pwmWrite(filepath.Join(s.pwmPath, "duty_cycle"), int64(widthUS)*1000)
}

func (s *sysfsBackend) close() {
	_ = pwmEnable(s.pwmPath, false)
	_ = os.WriteFile(filepath.Join(s.chipPath, "unexport"), []byte(strconv.Itoa(s.ch)), 0o644)
}

// ----- Pi 4 / BCM go-rpio backend -------------------------------------------------

type rpioBackend struct {
	pin rpio.Pin
}

func (r *rpioBackend) setDuty(widthUS int) error {
	r.pin.DutyCycle(uint32(widthUS), 20_000)
	return nil
}

func (r *rpioBackend) close() {
	_ = rpio.Close()
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
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
		Details: map[string][]string{"Model": {"standardServo", "halfCircle"}, "FunctionalLocation": {"Kitchen"}},
		ServicesMap: components.Services{
			rotation.SubPath: &rotation,
		},
		Traits: &Traits{GpioPin: 18},
	}
}

//-------------------------------------Instantiate the unit assets based on configuration

// newResource creates the unit asset with its channels and PWM backend selected at
// runtime: Raspberry Pi 5 uses the RP1 sysfs interface; all other platforms (Pi 4
// and earlier) use go-rpio with direct BCM register access.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		GpioPin:  18, // default; overridden by JSON config
		dutyChan: make(chan int, 1),
	}
	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Fatalf("trait configuration error: %v", err)
		}
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

	const periodNS = int64(20_000_000) // 50 Hz

	platform := detectPlatform()
	log.Printf("platform detected: %s — selecting PWM backend for GPIO %d", platform, t.GpioPin)

	var backend pwmBackend
	var cleanup func()

	switch platform {
	case "pi5":
		b, cf, err := newSysfsBackend(t.GpioPin, periodNS)
		if err != nil {
			log.Fatalf("PWM (Pi 5 sysfs): %v\n  Enable with: dtoverlay=pwm-2chan in /boot/firmware/config.txt", err)
		}
		backend = b
		cleanup = cf
	default: // Pi 4 and earlier
		b, cf, err := newRpioBackend(t.GpioPin)
		if err != nil {
			log.Fatalf("PWM (Pi 4 rpio): %v\n  Run as root or add the user to the gpio group", err)
		}
		backend = b
		cleanup = cf
	}
	t.backend = backend

	// Drive duty-cycle updates from the channel
	go func() {
		for widthUS := range t.dutyChan {
			if err := t.backend.setDuty(widthUS); err != nil {
				log.Printf("PWM duty update failed: %v", err)
			} else {
				fmt.Printf("PWM duty updated: %d µs\n", widthUS)
			}
		}
	}()

	return ua, func() {
		log.Println("disconnecting from servo (PWM off)")
		cleanup()
	}
}

// detectPlatform reads /proc/device-tree/model to distinguish Pi 5 (RP1 PWM) from
// Pi 4 and earlier (BCM PWM). Returns "pi5" or "pi4".
func detectPlatform() string {
	b, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return "pi4" // not a Pi or model file unavailable — fall back to BCM path
	}
	model := strings.ToLower(strings.TrimRight(string(b), "\x00\n"))
	if strings.Contains(model, "raspberry pi 5") {
		return "pi5"
	}
	return "pi4"
}

// newSysfsBackend initialises the RP1 sysfs PWM channel for Raspberry Pi 5.
// Requires dtoverlay=pwm-2chan in /boot/firmware/config.txt.
func newSysfsBackend(gpioPin int, periodNS int64) (*sysfsBackend, func(), error) {
	chipPath, err := findPWMChipPath()
	if err != nil {
		return nil, nil, err
	}
	ch, err := gpioToRP1PWMChan(gpioPin)
	if err != nil {
		return nil, nil, err
	}
	pwmPath, err := exportPWM(chipPath, ch)
	if err != nil {
		return nil, nil, err
	}
	if err := pwmEnable(pwmPath, false); err != nil {
		return nil, nil, err
	}
	if err := pwmWrite(filepath.Join(pwmPath, "period"), periodNS); err != nil {
		return nil, nil, err
	}
	if err := pwmWrite(filepath.Join(pwmPath, "duty_cycle"), int64(1_520_000)); err != nil { // neutral: 1520 µs
		return nil, nil, err
	}
	if err := pwmEnable(pwmPath, true); err != nil {
		return nil, nil, err
	}
	b := &sysfsBackend{pwmPath: pwmPath, chipPath: chipPath, ch: ch}
	cleanup := func() {
		_ = pwmEnable(pwmPath, false)
		_ = os.WriteFile(filepath.Join(chipPath, "unexport"), []byte(strconv.Itoa(ch)), 0o644)
	}
	return b, cleanup, nil
}

// newRpioBackend initialises BCM hardware PWM for Raspberry Pi 4 and earlier via
// the go-rpio library (requires /dev/mem access: run as root or gpio group).
func newRpioBackend(gpioPin int) (*rpioBackend, func(), error) {
	if err := rpio.Open(); err != nil {
		return nil, nil, fmt.Errorf("rpio.Open: %w", err)
	}
	pin := rpio.Pin(gpioPin)
	pin.Output()
	pin.Mode(rpio.Pwm)
	pin.Freq(1_000_000)        // 1 MHz base → 1 µs resolution
	pin.DutyCycle(620, 20_000) // neutral: 620 µs at 50 Hz
	b := &rpioBackend{pin: pin}
	return b, func() { _ = rpio.Close() }, nil
}

// --- helpers: sysfs PWM on RP1 ---

// gpioToRP1PWMChan maps a BCM GPIO number to its RP1 PWM channel on Raspberry Pi 5.
// The RP1 chip exposes four channels on GPIO 12, 13, 18, and 19.
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
		return 0, errors.New("GPIO not on RP1 PWM0 (use 12, 13, 18, or 19)")
	}
}

func findPWMChipPath() (string, error) {
	candidates, _ := filepath.Glob("/sys/class/pwm/pwmchip*")
	for _, c := range candidates {
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
	return "", errors.New("no /sys/class/pwm/pwmchip* found (enable dtoverlay=pwm-2chan and reboot)")
}

func exportPWM(chipPath string, ch int) (string, error) {
	pwmPath := filepath.Join(chipPath, "pwm"+strconv.Itoa(ch))
	if _, err := os.Stat(pwmPath); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(chipPath, "export"), []byte(strconv.Itoa(ch)), 0o644); err != nil {
			return "", err
		}
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(pwmPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// After the kernel creates pwm<ch>/, udev asynchronously chgrp's the
	// files inside to the 'gpio' group. Until that rule fires the files are
	// root:root 644, and a write to 'enable' returns EACCES. This is
	// invisible when parallax is started manually (the user types slower
	// than udev) but reliably trips when launched back-to-back by a tmux
	// script. Poll 'enable' for write access for up to 1 s so udev has time
	// to catch up.
	enablePath := filepath.Join(pwmPath, "enable")
	for i := 0; i < 100; i++ {
		f, err := os.OpenFile(enablePath, os.O_WRONLY, 0)
		if err == nil {
			_ = f.Close()
			break
		}
		if !os.IsPermission(err) {
			break // some other error — let the caller's write surface it
		}
		time.Sleep(10 * time.Millisecond)
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

//-------------------------------------Service handlers

func (t *Traits) rotation(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		positionForm := t.getPosition()
		usecases.HTTPProcessGetRequest(w, r, &positionForm)
	case "PUT":
		sig, err := usecases.HTTPProcessSetRequest(w, r)
		if err != nil {
			log.Println("Error with the setting request of the position ", err)
		}
		confirmation, err := t.setPosition(sig)
		if err != nil {
			log.Println("Error setting the position ", err)
		}
		// return the confirmation of the set operation
		bestContentType := "application/json" // we know what we sent, so we can respond in the same format
		responseData, err := usecases.Pack(&confirmation, bestContentType)
		if err != nil {
			log.Printf("Error packing response: %v", err)
		}
		w.Header().Set("Content-Type", bestContentType)
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(responseData)
		if err != nil {
			log.Printf("Error while writing response: %v", err)
		}
	default:
		http.Error(w, "Method is not supported.", http.StatusNotFound)
	}
}

//-------------------------------------Unit asset's resource functions

// timing constants for the PWM (pulse width modulation)
// pulse widths: (620 µs, 1520 µs, 2420 µs) → (0°, 90°, 180°)
const (
	minPulseWidth    = 620
	centerPulseWidth = 1520
	maxPulseWidth    = 2420
)

// getPosition returns the current servo position in percent.
func (t *Traits) getPosition() (f forms.SignalA_v1a) {
	f.NewForm()
	f.Value = float64(t.position)
	f.Unit = "Percent"
	f.Timestamp = time.Now()
	return f
}

// setPosition updates the PWM pulse width for a requested position in [0–100]%.
func (t *Traits) setPosition(f forms.SignalA_v1a) (forms.SignalA_v1a, error) {
	pos := int(f.Value)
	if pos < 0 {
		pos = 0
	} else if pos > 100 {
		pos = 100
	}
	if t.position != pos {
		log.Printf("servo position changing to %d%%", pos)
	}
	t.position = pos

	widthUS := minPulseWidth + (t.position*(maxPulseWidth-minPulseWidth))/100

	// Debounce: skip if the duty hasn't changed
	if widthUS == t.lastWidthUS {
		f.Timestamp = time.Now()
		return f, nil
	}
	t.lastWidthUS = widthUS

	// Non-blocking send with latest-wins behavior
	select {
	case t.dutyChan <- widthUS:
	default:
		select {
		case <-t.dutyChan:
		default:
		}
		t.dutyChan <- widthUS
	}
	f.Timestamp = time.Now()
	return f, nil
}
