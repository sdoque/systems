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

// nmea2000.go contains pure NMEA 2000 protocol logic: the CAN frame type,
// PGN extraction, ISO Request builder, and the PGN table with field decoders.
// No build constraints — this file compiles on all platforms so decode
// functions are fully testable without hardware.

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ── CAN frame ────────────────────────────────────────────────────────────────

// canFrame matches the Linux kernel struct can_frame (linux/can.h).
// Defined here (no build constraint) so unit tests compile on any OS.
//
//	canid_t can_id  — 4 bytes (bit 31 = CAN_EFF_FLAG for 29-bit extended IDs)
//	__u8    len     — 1 byte  (DLC)
//	__u8    __pad   — 1 byte
//	__u8    __res0  — 1 byte
//	__u8    __res1  — 1 byte
//	__u8    data[8] — 8 bytes
//	                  ───────
//	                 16 bytes total
type canFrame struct {
	ID   uint32
	DLC  uint8
	Pad  uint8
	Res0 uint8
	Res1 uint8
	Data [8]byte
}

// ── NMEA 2000 constants ───────────────────────────────────────────────────────

const (
	// canEFFFlag marks a 29-bit extended CAN frame in the ID field.
	canEFFFlag uint32 = 0x80000000

	// nmeaSrcAddress is the source address we use when sending ISO Requests.
	// 0x23 (35) is an arbitrary non-reserved address; matches the student code.
	nmeaSrcAddress uint32 = 0x23

	// isoRequestPGN is the NMEA 2000 ISO Request PGN (59904 = 0xEA00).
	// Sending this PGN with a target PGN in the payload asks a device to
	// transmit that PGN once immediately.
	isoRequestPGN uint32 = 59904
)

// ── PGN extraction ────────────────────────────────────────────────────────────

// extractPGN returns the NMEA 2000 PGN encoded in a 29-bit extended CAN ID.
//
// NMEA 2000 uses ISO 11783 address encoding:
//
//	Bits 28–26  Priority  (3 bits)
//	Bit  25     Reserved
//	Bit  24     DP — Data Page
//	Bits 23–16  PF — PDU Format  (8 bits)
//	Bits 15–8   PS — PDU Specific (destination if PF < 240; group ext if PF >= 240)
//	Bits 7–0    SA — Source Address
//
// For PDU Format 2 (PF >= 240) the PS byte is part of the PGN.
// For PDU Format 1 (PF < 240) the PS byte is the destination address (not in PGN).
func extractPGN(canID uint32) uint32 {
	raw := canID & 0x1FFFFFFF
	dp := (raw >> 24) & 0x01
	pf := (raw >> 16) & 0xFF
	ps := (raw >> 8) & 0xFF
	if pf >= 240 { // PDU Format 2 — PS is group extension
		return (dp << 16) | (pf << 8) | ps
	}
	return (dp << 16) | (pf << 8) // PDU Format 1 — PS is destination
}

// ── ISO Request ───────────────────────────────────────────────────────────────

// buildISORequest returns a CAN frame that asks any device on the bus to
// transmit the given PGN.  The request is sent to the global broadcast address
// (0xFF) using PGN 59904 (ISO Request).
func buildISORequest(pgn uint32) canFrame {
	// Build the 29-bit extended CAN ID for PGN 59904, broadcast destination.
	// PGN 59904 = 0xEA00 → DP=0, PF=0xEA (PDU1), PS = destination address.
	const (
		priority = 6
		pf       = 0xEA
		dst      = 0xFF // global broadcast
	)
	id := canEFFFlag |
		(uint32(priority) << 26) |
		(uint32(pf) << 16) |
		(uint32(dst) << 8) |
		nmeaSrcAddress

	// Payload: requested PGN in 3 bytes, LSB first; pad with 0xFF.
	return canFrame{
		ID:  id,
		DLC: 8,
		Data: [8]byte{
			byte(pgn & 0xFF),
			byte((pgn >> 8) & 0xFF),
			byte((pgn >> 16) & 0xFF),
			0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		},
	}
}

// ── PGN table ─────────────────────────────────────────────────────────────────

// fieldInfo describes one numeric field within an NMEA 2000 PGN.
type fieldInfo struct {
	Unit   string
	Decode func(data [8]byte) float64 // returns math.NaN() for "not available"
}

// pgnInfo describes an NMEA 2000 PGN and its decodable fields.
type pgnInfo struct {
	Name   string
	Fields map[string]fieldInfo
}

// pgnTable is the registry of supported NMEA 2000 PGNs.
// Add entries here to support additional signals; no other code changes needed.
//
// Byte layouts follow the NMEA 2000 standard (canboat pgn.json definitions):
//
//	All multi-byte integers are little-endian.
//	UINT16: resolution given per field; 0xFFFF = not available.
//	UINT32: 0xFFFFFFFF = not available.
//	INT16:  0x7FFF = not available.
var pgnTable = map[uint32]pgnInfo{

	// PGN 130306 — Wind Data (single frame, 6 data bytes)
	//   Byte 0:    SID
	//   Bytes 1–2: Wind Speed  UINT16, 0.01 m/s
	//   Bytes 3–4: Wind Angle  UINT16, 0.0001 rad (0 → 2π)
	//   Byte 5:    Reference (4 bits) + reserved
	130306: {
		Name: "Wind Data",
		Fields: map[string]fieldInfo{
			"WindSpeed": {
				Unit: "m/s",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[1:3])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.01
				},
			},
			"WindAngle": {
				Unit: "rad",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[3:5])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.0001
				},
			},
		},
	},

	// PGN 128259 — Vessel Speed (single frame, 6 data bytes)
	//   Byte 0:    SID
	//   Bytes 1–2: Speed Water Referenced  UINT16, 0.01 m/s
	//   Bytes 3–4: Speed Ground Referenced UINT16, 0.01 m/s
	//   Byte 5:    Reference type (2 bits) + reserved
	128259: {
		Name: "Vessel Speed",
		Fields: map[string]fieldInfo{
			"WaterSpeed": {
				Unit: "m/s",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[1:3])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.01
				},
			},
			"GroundSpeed": {
				Unit: "m/s",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[3:5])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.01
				},
			},
		},
	},

	// PGN 127250 — Vessel Heading (single frame, 8 data bytes)
	//   Byte 0:    SID
	//   Bytes 1–2: Heading    UINT16, 0.0001 rad
	//   Bytes 3–4: Deviation  INT16,  0.0001 rad  (0x7FFF = n/a)
	//   Bytes 5–6: Variation  INT16,  0.0001 rad  (0x7FFF = n/a)
	//   Byte 7:    Reference (2 bits) + reserved
	127250: {
		Name: "Vessel Heading",
		Fields: map[string]fieldInfo{
			"Heading": {
				Unit: "rad",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[1:3])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.0001
				},
			},
			"Deviation": {
				Unit: "rad",
				Decode: func(d [8]byte) float64 {
					v := int16(binary.LittleEndian.Uint16(d[3:5]))
					if v == 0x7FFF {
						return math.NaN()
					}
					return float64(v) * 0.0001
				},
			},
		},
	},

	// PGN 129026 — COG & SOG, Rapid Update (single frame, 8 data bytes)
	//   Byte 0:    SID
	//   Byte 1:    COG Reference (2 bits) + reserved (6 bits)
	//   Bytes 2–3: COG  UINT16, 0.0001 rad
	//   Bytes 4–5: SOG  UINT16, 0.01 m/s
	//   Bytes 6–7: reserved
	129026: {
		Name: "COG & SOG Rapid",
		Fields: map[string]fieldInfo{
			"COG": {
				Unit: "rad",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[2:4])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.0001
				},
			},
			"SOG": {
				Unit: "m/s",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint16(d[4:6])
					if v == 0xFFFF {
						return math.NaN()
					}
					return float64(v) * 0.01
				},
			},
		},
	},

	// PGN 128267 — Water Depth (single frame, 8 data bytes)
	//   Byte 0:    SID
	//   Bytes 1–4: Depth   UINT32, 0.01 m
	//   Bytes 5–6: Offset  INT16,  0.001 m  (transducer offset)
	//   Byte 7:    Range
	128267: {
		Name: "Water Depth",
		Fields: map[string]fieldInfo{
			"Depth": {
				Unit: "m",
				Decode: func(d [8]byte) float64 {
					v := binary.LittleEndian.Uint32(d[1:5])
					if v == 0xFFFFFFFF {
						return math.NaN()
					}
					return float64(v) * 0.01
				},
			},
		},
	},
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parsePGN converts a string like "130306" or "0x1FD02" to a uint32 PGN.
func parsePGN(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	base := 10
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		s = s[2:]
		base = 16
	}
	v, err := strconv.ParseUint(s, base, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid PGN %q: %w", s, err)
	}
	return uint32(v), nil
}

// lookupPGNField returns the fieldInfo for the named field of the given PGN,
// or an error if either the PGN or the field is not in pgnTable.
func lookupPGNField(pgn uint32, field string) (fieldInfo, error) {
	pgnDef, ok := pgnTable[pgn]
	if !ok {
		return fieldInfo{}, fmt.Errorf("unsupported PGN %d", pgn)
	}
	fi, ok := pgnDef.Fields[field]
	if !ok {
		return fieldInfo{}, fmt.Errorf("unknown field %q for PGN %d (%s)", field, pgn, pgnDef.Name)
	}
	return fi, nil
}
