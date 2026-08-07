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

// Märklin's CAN protocol, as spoken between a Central Station, its Gleisbox and
// anything else on the bus.
//
// Checked against Märklin's own document — "Kommunikationsprotokoll GUI <-> GFP
// über CAN", CAN_CS2_Protokoll_1-0, version 1.0 of 26.9.2008. Section numbers
// below refer to it.
//
// Two things that version does not define, and which are therefore still
// unverified here: the DCC address range, and any feedback (S88) command. Both
// arrived in later protocol revisions, so a modern Central Station may well
// speak them — but not on the authority of this file.

package main

import (
	"encoding/binary"
	"fmt"
)

// The 29-bit extended identifier is a structure rather than an opaque number
// (§1.2): priority 2+2 bits, command 8, response 1, hash 16.
//
//	bits 25-28  priority — must be 0b0000 in protocol 1.0 (§1.3.1)
//	bits 17-24  command
//	bit  16     response flag — set on an answer to a command
//	bits 0-15   hash, which resolves collisions
//
// The document gives each command twice: "Lok Geschwindigkeit (0x04, in CAN-ID:
// 0x08)". The second number is the command and the response bit read as one
// 9-bit field, so it is simply the first doubled. The constants below are the
// command itself, which is why they are shifted by 17 rather than 16.
const (
	shiftPriority = 25
	shiftCommand  = 17
	shiftResponse = 16

	maskPriority = 0x0F
	maskCommand  = 0xFF
	maskHash     = 0xFFFF
)

// Commands, from the section headings of the protocol document.
const (
	cmdSystem        = 0x00 // §2 — the subcommand is the fifth payload byte
	cmdLocoDiscovery = 0x01 // §3.1
	cmdMFXBind       = 0x02 // §3.2
	cmdMFXVerify     = 0x03 // §3.3
	cmdLocoSpeed     = 0x04 // §3.4
	cmdLocoDirection = 0x05 // §3.5
	cmdLocoFunction  = 0x06 // §3.6
	cmdReadConfig    = 0x07
	cmdWriteConfig   = 0x08
	cmdAccessory     = 0x0B // Zubehör schalten
	cmdPing          = 0x18 // Softwarestand / Ping
	cmdStatusData    = 0x1D // Statusdaten Konfiguration
)

// System subcommands (§2), carried in the fifth payload byte after the target
// device's UID.
const (
	sysStop           = 0x00 // §2.1 — track power off, settings kept
	sysGo             = 0x01 // §2.2
	sysHalt           = 0x02 // §2.3
	sysLocoEmergency  = 0x03 // §2.4 Lok Nothalt
	sysLocoCycleEnd   = 0x04 // §2.5 Lok Zyklus Stop
	sysMFXRegisterCtr = 0x09 // §2.6 mfx Neuanmeldezähler
	sysOverload       = 0x0A // Überlast
	sysStatus         = 0x0B
)

// Direction values carried in a direction command (§3.5).
const (
	dirUnchanged = 0x00 // also what an invalid value means
	dirForward   = 0x01
	dirReverse   = 0x02
	dirToggle    = 0x03
)

// SpeedMax is the full-scale value a speed command carries (§3.4). Speeds are
// handled as 10-bit values throughout the system and the range used should run
// from 0 to 1000, independent of what is actually sent to the locomotive over
// the rails — the station scales its speed steps onto roughly this thousandth
// scale, 14 steps at 72 and 126 steps at 8 both landing on 1008.
//
// It is a fraction of the decoder's maximum rather than a physical speed, which
// is why the service reports percent and states this as its range.
const SpeedMax = 1000

// Decoder address ranges (§1.4.3). The existing track protocols are embedded in
// the lower two bytes of the Loc-ID, and a UID's position gives away which
// protocol it speaks.
const (
	rangeMM2Low  = 0x0000 // MM1,2 locomotives and function decoders
	rangeMM2High = 0x03FF
	rangeAccLow  = 0x3000 // MM1,2 accessory decoders — 0x3400 upward is reserved
	rangeAccHigh = 0x33FF
	rangeMFXLow  = 0x4000
	rangeMFXHigh = 0x7FFF

	// Protocol 1.0 lists 0xC000-0xFFFF as reserved rather than as DCC; later
	// revisions put DCC here. Kept because a modern station uses it, but it is
	// the one range this file cannot cite.
	rangeDCCLow  = 0xC000
	rangeDCCHigh = 0xFFFF
)

// Frame is one CAN message, in the shape the protocol gives it.
type Frame struct {
	Priority uint8
	Command  uint8
	Response bool
	Hash     uint16
	Data     []byte
}

// Identifier packs the frame's fields into the 29-bit extended CAN identifier.
func (f Frame) Identifier() uint32 {
	id := uint32(f.Priority&maskPriority) << shiftPriority
	id |= uint32(f.Command&maskCommand) << shiftCommand
	if f.Response {
		id |= 1 << shiftResponse
	}
	return id | uint32(f.Hash)
}

// FrameFromIdentifier unpacks an identifier received from the bus.
func FrameFromIdentifier(id uint32, data []byte) Frame {
	return Frame{
		Priority: uint8((id >> shiftPriority) & maskPriority),
		Command:  uint8((id >> shiftCommand) & maskCommand),
		Response: (id>>shiftResponse)&1 == 1,
		Hash:     uint16(id & maskHash),
		Data:     data,
	}
}

// DecoderKind names the protocol a locomotive's decoder speaks, which its
// address range gives away.
type DecoderKind string

const (
	DecoderMM2       DecoderKind = "MM2"
	DecoderMFX       DecoderKind = "MFX"
	DecoderDCC       DecoderKind = "DCC"
	DecoderAccessory DecoderKind = "accessory"
	DecoderUnknown   DecoderKind = "unknown"
)

// KindOf reports which protocol a UID belongs to.
func KindOf(uid uint32) DecoderKind {
	address := uid & 0xFFFF
	switch {
	case address >= rangeMFXLow && address <= rangeMFXHigh:
		return DecoderMFX
	case address >= rangeDCCLow && address <= rangeDCCHigh:
		return DecoderDCC
	case address >= rangeAccLow && address <= rangeAccHigh:
		return DecoderAccessory
	case address >= rangeMM2Low && address <= rangeMM2High:
		return DecoderMM2
	default:
		return DecoderUnknown
	}
}

// SpeedFrame commands a locomotive to a speed, given as a fraction of full
// scale. The caller works in percent; the wire works in thousandths.
func SpeedFrame(uid uint32, percent float64) (Frame, error) {
	if percent < 0 || percent > 100 {
		return Frame{}, fmt.Errorf("speed %.1f%% is outside 0 to 100", percent)
	}
	value := uint16(percent/100*SpeedMax + 0.5)

	data := make([]byte, 6)
	binary.BigEndian.PutUint32(data[0:4], uid)
	binary.BigEndian.PutUint16(data[4:6], value)
	return Frame{Command: cmdLocoSpeed, Data: data}, nil
}

// DirectionFrame commands a locomotive's direction of travel.
func DirectionFrame(uid uint32, forward bool) Frame {
	direction := byte(dirReverse)
	if forward {
		direction = dirForward
	}
	data := make([]byte, 5)
	binary.BigEndian.PutUint32(data[0:4], uid)
	data[4] = direction
	return Frame{Command: cmdLocoDirection, Data: data}
}

// FunctionFrame switches one of a locomotive's functions — its light, its horn —
// on or off.
func FunctionFrame(uid uint32, function uint8, on bool) Frame {
	value := byte(0)
	if on {
		value = 1
	}
	data := make([]byte, 6)
	binary.BigEndian.PutUint32(data[0:4], uid)
	data[4] = function
	data[5] = value
	return Frame{Command: cmdLocoFunction, Data: data}
}

// SystemFrame carries a stop, go or halt to the whole layout.
func SystemFrame(subcommand byte) Frame {
	data := make([]byte, 5)
	data[4] = subcommand
	return Frame{Command: cmdSystem, Data: data}
}

// SpeedPercent reads a speed reported on the bus back into percent of full scale.
func SpeedPercent(data []byte) (float64, error) {
	if len(data) < 6 {
		return 0, fmt.Errorf("a speed frame carries 6 bytes, not %d", len(data))
	}
	raw := binary.BigEndian.Uint16(data[4:6])
	// Clamped, because the frame comes off a bus this code does not control. A
	// malformed or corrupted speed word of 0xFFFF is 6553.5 %, which SpeedFrame
	// would refuse on the way out but which is served to a consumer on the way
	// in — a percentage that cannot exist, presented as a reading.
	if raw > SpeedMax {
		return 100, fmt.Errorf("the frame reports %d of a full scale of %d: clamped to 100%%", raw, SpeedMax)
	}
	return float64(raw) / SpeedMax * 100, nil
}

// UIDOf reads the locomotive a frame concerns.
func UIDOf(data []byte) (uint32, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("a frame naming a locomotive carries at least 4 bytes, not %d", len(data))
	}
	return binary.BigEndian.Uint32(data[0:4]), nil
}
