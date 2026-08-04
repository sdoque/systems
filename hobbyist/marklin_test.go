package main

import "testing"

// The document gives each command twice — "Lok Geschwindigkeit (0x04, in CAN-ID:
// 0x08)" — because the command and the response bit are read as one 9-bit field.
// The second number is the first doubled, and an identifier built from the
// command must land on it.
func TestIdentifierMatchesTheDocumentedCanIdValues(t *testing.T) {
	tests := []struct {
		name    string
		command uint8
		inCanID uint32 // the "in CAN-ID" value the specification prints
	}{
		{"system", cmdSystem, 0x00},
		{"MFX verify", cmdMFXVerify, 0x06},
		{"speed", cmdLocoSpeed, 0x08},
		{"direction", cmdLocoDirection, 0x0A},
		{"function", cmdLocoFunction, 0x0C},
		{"write config", cmdWriteConfig, 0x10},
		{"accessory", cmdAccessory, 0x16},
		{"ping", cmdPing, 0x30},
		{"status data", cmdStatusData, 0x3A},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := Frame{Command: tc.command}.Identifier()
			// The command sits above the response bit, so the documented value
			// is what the identifier holds from bit 16 upward.
			if got := id >> shiftResponse; got != tc.inCanID {
				t.Errorf("identifier >> 16 = %#x; the specification prints %#x", got, tc.inCanID)
			}
		})
	}
}

// Priority must be zero in protocol 1.0, so a frame built without one is legal
// as it stands.
func TestIdentifierRoundTrip(t *testing.T) {
	original := Frame{Priority: 0, Command: cmdLocoSpeed, Response: true, Hash: 0x4711}
	back := FrameFromIdentifier(original.Identifier(), nil)

	if back.Command != original.Command || back.Hash != original.Hash || !back.Response {
		t.Errorf("round trip gave %+v; want %+v", back, original)
	}
	plain := Frame{Command: cmdLocoSpeed}
	if plain.Identifier()>>shiftPriority != 0 {
		t.Error("a frame built without a priority is not using 0b0000")
	}
}

// The address ranges are what tell one track protocol from another.
func TestKindOf(t *testing.T) {
	tests := []struct {
		uid  uint32
		want DecoderKind
	}{
		{0x0002, DecoderMM2},       // the document's example: Motorola address 2
		{0x3003, DecoderAccessory}, // its other example: MM2 accessory address 3
		{0x03FF, DecoderMM2},
		{0x33FF, DecoderAccessory},
		{0x3400, DecoderUnknown}, // reserved, not accessory
		{0x4000, DecoderMFX},
		{0x7FFF, DecoderMFX},
		{0x8000, DecoderUnknown}, // reserved
	}
	for _, tc := range tests {
		if got := KindOf(tc.uid); got != tc.want {
			t.Errorf("KindOf(%#x) = %q; want %q", tc.uid, got, tc.want)
		}
	}
}

// Speeds run 0 to 1000 on the wire whatever the decoder's step count.
func TestSpeedFrame(t *testing.T) {
	f, err := SpeedFrame(0x4001, 50)
	if err != nil {
		t.Fatalf("SpeedFrame: %v", err)
	}
	if len(f.Data) != 6 {
		t.Fatalf("data length %d; a speed command carries the UID and a 16-bit value", len(f.Data))
	}
	if f.Data[4] != 0x01 || f.Data[5] != 0xF4 { // 500
		t.Errorf("50%% encoded as %#x %#x; want 500 big-endian", f.Data[4], f.Data[5])
	}

	percent, err := SpeedPercent(f.Data)
	if err != nil || percent < 49.9 || percent > 50.1 {
		t.Errorf("SpeedPercent = %v, %v; want 50", percent, err)
	}
	if _, err := SpeedFrame(0x4001, 101); err == nil {
		t.Error("a speed above full scale was accepted")
	}
}

func TestDirectionAndFunctionFrames(t *testing.T) {
	d := DirectionFrame(0x4001, true)
	if len(d.Data) != 5 || d.Data[4] != dirForward {
		t.Errorf("direction frame = %v; want the UID and %d", d.Data, dirForward)
	}
	if DirectionFrame(0x4001, false).Data[4] != dirReverse {
		t.Error("reverse was not encoded as 2")
	}

	f := FunctionFrame(0x4001, 3, true)
	if len(f.Data) != 6 || f.Data[4] != 3 || f.Data[5] != 1 {
		t.Errorf("function frame = %v; want the UID, the function number and its value", f.Data)
	}
}

// A system command names the device it is for, then what to do.
func TestSystemFrame(t *testing.T) {
	f := SystemFrame(sysGo)
	if len(f.Data) != 5 || f.Data[4] != sysGo {
		t.Errorf("system frame = %v; want a UID and the subcommand", f.Data)
	}
	if f.Command != cmdSystem {
		t.Errorf("command = %#x; want %#x", f.Command, cmdSystem)
	}
}
