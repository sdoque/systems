/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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

// obd2.go contains pure OBD-II logic: the PID table, request builder, and
// response decoder.  No build constraints — this file compiles on all
// platforms so the decode functions are fully testable without hardware.

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// OBD-II addressing constants (ISO 15765-4, SAE J1979).
const (
	obdRequestID      uint32 = 0x7DF // functional broadcast — every ECU listens
	obdResponseIDBase uint32 = 0x7E8 // ECU 1 replies here; ECU 2 → 0x7E9, etc.
)

// canFrame matches the Linux kernel struct can_frame (linux/can.h).
// Defined here (no build constraint) so OBD-II unit tests compile on any OS.
//
//	canid_t can_id  — 4 bytes
//	__u8    len     — 1 byte  (DLC)
//	__u8    __pad   — 1 byte
//	__u8    __res0  — 1 byte
//	__u8    __res1  — 1 byte
//	__u8    data[8] — 8 bytes
//	                  ───────
//	                 16 bytes total (no implicit padding)
type canFrame struct {
	ID   uint32
	DLC  uint8
	Pad  uint8
	Res0 uint8
	Res1 uint8
	Data [8]byte
}

// pidInfo describes one OBD-II Mode 01 PID.
type pidInfo struct {
	Name   string
	Unit   string
	Decode func(a, b byte) float64 // a = Data[3], b = Data[4]
}

// pidTable is the registry of supported OBD-II Mode 01 PIDs.
// Add entries here to support additional signals; no other code changes needed.
var pidTable = map[uint8]pidInfo{
	0x04: {"EngineLoad", "%", func(a, _ byte) float64 { return float64(a) * 100 / 255 }},
	0x05: {"CoolantTemperature", "Celsius", func(a, _ byte) float64 { return float64(a) - 40 }},
	0x0C: {"EngineRPM", "rpm", func(a, b byte) float64 { return (float64(a)*256 + float64(b)) / 4 }},
	0x0D: {"VehicleSpeed", "km/h", func(a, _ byte) float64 { return float64(a) }},
	0x0E: {"TimingAdvance", "degrees", func(a, _ byte) float64 { return float64(a)/2 - 64 }},
	0x0F: {"IntakeAirTemperature", "Celsius", func(a, _ byte) float64 { return float64(a) - 40 }},
	0x10: {"MAFAirFlowRate", "g/s", func(a, b byte) float64 { return (float64(a)*256 + float64(b)) / 100 }},
	0x11: {"ThrottlePosition", "%", func(a, _ byte) float64 { return float64(a) * 100 / 255 }},
	0x2F: {"FuelTankLevel", "%", func(a, _ byte) float64 { return float64(a) * 100 / 255 }},
	0x5C: {"OilTemperature", "Celsius", func(a, _ byte) float64 { return float64(a) - 40 }},
}

// lookupPID returns the pidInfo for pid, or an error if it is not in pidTable.
func lookupPID(pid uint8) (pidInfo, error) {
	info, ok := pidTable[pid]
	if !ok {
		return pidInfo{}, fmt.Errorf("unsupported PID 0x%02X", pid)
	}
	return info, nil
}

// parsePID converts a string like "0x0C" or "12" to a uint8 PID value.
func parsePID(s string) (uint8, error) {
	s = strings.TrimSpace(s)
	base := 10
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		s = s[2:]
		base = 16
	}
	v, err := strconv.ParseUint(s, base, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid PID %q: %w", s, err)
	}
	return uint8(v), nil
}

// buildOBDRequest returns a CAN frame that requests Mode 01 PID pid from all
// ECUs on the bus using the functional broadcast address (0x7DF).
func buildOBDRequest(pid uint8) canFrame {
	return canFrame{
		ID:  obdRequestID,
		DLC: 8,
		Data: [8]byte{
			0x02,        // payload length: 2 bytes follow
			0x01,        // Mode 01 — show live data
			pid,         // the requested PID
			0x00, 0x00, 0x00, 0x00, 0x00, // padding
		},
	}
}

// decodeOBDResponse extracts a float64 value from a Mode 01 CAN response frame.
// Returns an error if the frame is not a valid response for the given PID.
//
// Expected frame layout (SAE J1979):
//
//	Data[0] — number of additional bytes (length)
//	Data[1] — 0x41 (Mode 01 positive response = 0x40 + 0x01)
//	Data[2] — PID echoed back
//	Data[3] — value byte A
//	Data[4] — value byte B (used by two-byte PIDs such as RPM)
func decodeOBDResponse(frame canFrame, pid uint8) (float64, error) {
	if frame.ID < obdResponseIDBase || frame.ID > obdResponseIDBase+7 {
		return 0, fmt.Errorf("unexpected CAN ID 0x%03X (want 0x7E8–0x7EF)", frame.ID)
	}
	if frame.Data[1] != 0x41 {
		return 0, fmt.Errorf("not a Mode 01 positive response (byte 1 = 0x%02X)", frame.Data[1])
	}
	if frame.Data[2] != pid {
		return 0, fmt.Errorf("PID mismatch: got 0x%02X, want 0x%02X", frame.Data[2], pid)
	}
	info, err := lookupPID(pid)
	if err != nil {
		return 0, err
	}
	return info.Decode(frame.Data[3], frame.Data[4]), nil
}
