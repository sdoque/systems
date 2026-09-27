/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	serial "go.bug.st/serial"
)

// fakePort is a serial port made of two buffers. Only Read and Write are used
// by the framing; anything else would panic on the nil interface, which is the
// point.
type fakePort struct {
	serial.Port
	in  bytes.Buffer
	out bytes.Buffer
}

func (f *fakePort) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakePort) Write(p []byte) (int, error) { return f.out.Write(p) }

// The standard check value for CRC-16-CCITT from zero (XMODEM). The guide gives
// the algorithm as code rather than as a check value; this pins the code.
func TestCRCCheckValue(t *testing.T) {
	if got := crc16([]byte("123456789")); got != 0x31C3 {
		t.Errorf("crc16(\"123456789\") = 0x%04X, want 0x31C3", got)
	}
}

// A written frame is laid out as the guide's table 5 says, and reads back.
func TestFrameRoundTrip(t *testing.T) {
	port := &fakePort{}
	s := &serialSource{port: port}
	if err := s.writeFrame(cmdStream, u32(5)); err != nil {
		t.Fatal(err)
	}
	frame := port.out.Bytes()
	if frame[0] != 0xAA {
		t.Errorf("start byte 0x%02X", frame[0])
	}
	flags := binary.LittleEndian.Uint16(frame[1:3])
	if got := flags >> 6; got != 5 {
		t.Errorf("payload length %d, want 5 (the id byte and four data bytes)", got)
	}
	if flags&1 != 1 {
		t.Error("the write bit is not set on a write")
	}
	if frame[3] != cmdStream || binary.LittleEndian.Uint32(frame[4:8]) != 5 {
		t.Errorf("id and data are % X", frame[3:8])
	}

	port.in.Write(frame)
	id, payload, err := s.readFrame()
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if id != cmdStream || binary.LittleEndian.Uint32(payload) != 5 {
		t.Errorf("read back id %d payload % X", id, payload)
	}
}

func TestACorruptFrameIsRefused(t *testing.T) {
	port := &fakePort{}
	s := &serialSource{port: port}
	s.writeFrame(cmdDistanceDataCm, []byte{1, 2, 3, 4})
	frame := port.out.Bytes()
	frame[5] ^= 0xFF
	port.in.Write(frame)
	if _, _, err := s.readFrame(); err == nil {
		t.Error("a frame with a flipped byte was accepted")
	}
	if s.framesBad != 1 {
		t.Errorf("framesBad = %d, want 1", s.framesBad)
	}
}

// Noise before the start byte is skipped.
func TestReadFrameHuntsForTheStartByte(t *testing.T) {
	port := &fakePort{}
	s := &serialSource{port: port}
	s.writeFrame(cmdDistanceDataCm, []byte{1, 2, 3, 4})
	frame := append([]byte{0x00, 0x13, 0x37}, port.out.Bytes()...)
	port.in.Write(frame)
	if id, _, err := s.readFrame(); err != nil || id != cmdDistanceDataCm {
		t.Errorf("id %d, err %v", id, err)
	}
}

func distancePayload(cm, centideg int16) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b[0:2], uint16(cm))
	binary.LittleEndian.PutUint16(b[2:4], uint16(centideg))
	return b
}

// With 27 set to bits 0 and 8, command 44 is first-return-raw in cm then yaw in
// hundredths of a degree, both int16.
func TestDistanceDataIsCentimetersThenYaw(t *testing.T) {
	angle, dist, ok := parseDistanceData(distancePayload(1234, -4550), 50)
	if !ok {
		t.Fatal("a valid payload was rejected")
	}
	if math.Abs(dist-12.34) > 1e-9 {
		t.Errorf("distance %v m, want 12.34 (1234 cm)", dist)
	}
	if math.Abs(angle-(-45.5)) > 1e-9 {
		t.Errorf("angle %v°, want -45.5", angle)
	}
}

// The rule the guetteur rests on: nothing coming back is never a distance.
func TestALostSignalIsNotADistance(t *testing.T) {
	for _, cm := range []int16{lostSignalValue, 0, -1} {
		angle, dist, ok := parseDistanceData(distancePayload(cm, 1000), 50)
		if !ok {
			t.Errorf("%d cm: the point was dropped; it should be kept as a non-return", cm)
		}
		if dist != 0 {
			t.Errorf("%d cm was reported as a distance of %v m", cm, dist)
		}
		if angle != 10 {
			t.Errorf("%d cm: angle %v, want 10", cm, angle)
		}
	}
}

func TestBeyondRangeIsNotADistance(t *testing.T) {
	if _, dist, _ := parseDistanceData(distancePayload(3000, 0), 20); dist != 0 {
		t.Errorf("30 m with a 20 m maximum was reported as %v m", dist)
	}
}

func TestAShortPayloadIsRejected(t *testing.T) {
	if _, _, ok := parseDistanceData([]byte{1, 2, 3}, 50); ok {
		t.Error("a three-byte payload was accepted")
	}
}

// The sector limits are written as the guide's ranges require: low negative,
// high positive.
func TestSectorLimitsAreSignedAndClamped(t *testing.T) {
	port := &fakePort{}
	s := &serialSource{port: port}
	if err := s.setSector(90); err != nil {
		t.Fatal(err)
	}
	var got []float32
	for port.out.Len() > 0 {
		port.in.Write(nextFrame(&port.out))
		_, payload, err := s.readFrame()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(payload)))
	}
	if len(got) != 2 || got[0] != -45 || got[1] != 45 {
		t.Errorf("sector 90° wrote %v, want [-45 45]", got)
	}
}

// nextFrame takes one whole frame off the front of a buffer.
func nextFrame(b *bytes.Buffer) []byte {
	head := b.Bytes()[:3]
	n := int(binary.LittleEndian.Uint16(head[1:3])>>6) + 3 + 2
	return b.Next(n)
}
