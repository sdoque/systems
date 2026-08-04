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
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LocoFile reads the locomotive list from a file the operator exports from the
// Central Station.
//
// The station holds this list and will also stream it over the CAN config-data
// channel, which is the eventual source. A file works today, needs no verified
// protocol, and separates two questions that are independent: *which*
// locomotives exist, and *how* to command them. The wire format for the former
// can be settled later without touching anything below it.
type LocoFile struct {
	Path string
}

// locoRecord is the file's shape: what the station knows, in the least ceremony
// that carries it.
type locoRecord struct {
	UID       string            `json:"uid"`  // hexadecimal, e.g. "0x4001"
	Name      string            `json:"name"` // the station's name, e.g. "421 393-0"
	Functions map[string]string `json:"functions"`
}

// Locomotives reads and validates the list.
func (l LocoFile) Locomotives() ([]Locomotive, error) {
	data, err := os.ReadFile(l.Path)
	if err != nil {
		return nil, fmt.Errorf("reading the locomotive list: %w", err)
	}

	var records []locoRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("reading the locomotive list: %w", err)
	}

	locomotives := make([]Locomotive, 0, len(records))
	for i, r := range records {
		loco, err := r.locomotive()
		if err != nil {
			return nil, fmt.Errorf("locomotive %d in %s: %w", i, l.Path, err)
		}
		locomotives = append(locomotives, loco)
	}
	return locomotives, nil
}

func (r locoRecord) locomotive() (Locomotive, error) {
	uid, err := parseUID(r.UID)
	if err != nil {
		return Locomotive{}, err
	}
	if strings.TrimSpace(r.Name) == "" {
		return Locomotive{}, fmt.Errorf("no name, so nothing an operator could recognise it by")
	}

	functions := make([]Function, 0, len(r.Functions))
	for number, name := range r.Functions {
		n, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(number), "f"), 10, 8)
		if err != nil {
			return Locomotive{}, fmt.Errorf("function key %q is not a number", number)
		}
		functions = append(functions, Function{Number: uint8(n), Name: name})
	}

	return Locomotive{UID: uid, Name: r.Name, Kind: KindOf(uid), Functions: functions}, nil
}

// parseUID accepts the hexadecimal the station uses, with or without its prefix.
func parseUID(text string) (uint32, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("no uid")
	}
	uid, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(trimmed), "0x"), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("uid %q is not hexadecimal: %w", text, err)
	}
	return uint32(uid), nil
}

// silentBus stands in until the CAN transport is connected. It refuses rather
// than pretending: a command that goes nowhere must not look like one that
// arrived.
type silentBus struct{}

func (silentBus) Send(Frame) error {
	return fmt.Errorf("not connected to the layout: no CAN interface is open")
}
