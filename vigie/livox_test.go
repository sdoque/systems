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
	"errors"
	"math"
	"net"
	"os"
	"testing"
)

// The two checksums against their standard check values: the CRC of the nine
// ASCII digits "123456789".
func TestTheChecksumsAreTheStandardOnes(t *testing.T) {
	if got := crc16CCITTFalse([]byte("123456789")); got != 0x29B1 {
		t.Errorf("CRC-16/CCITT-FALSE = 0x%04X, want 0x29B1", got)
	}
	if got := crc32Livox([]byte("123456789")); got != 0xCBF43926 {
		t.Errorf("CRC-32 = 0x%08X, want 0xCBF43926", got)
	}
}

func TestAFrameSurvivesTheRoundTrip(t *testing.T) {
	in := frame{seq: 7, cmd: cmdQueryParams, cmdType: cmdTypeRequest, sender: senderHost, data: queryParamsData([]uint16{keySerialNumber})}
	b := in.encode()
	if b[0] != 0xAA || int(binary.LittleEndian.Uint16(b[2:])) != len(b) {
		t.Fatalf("header % X", b[:4])
	}
	out, err := decodeFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.seq != 7 || out.cmd != cmdQueryParams || !bytes.Equal(out.data, in.data) {
		t.Errorf("%+v", out)
	}
}

// A frame with no data carries a zero CRC-32, as the protocol says.
func TestAnEmptyFrameHasAZeroDataCRC(t *testing.T) {
	b := frame{seq: 1, cmd: cmdDiscovery}.encode()
	if len(b) != 24 || binary.LittleEndian.Uint32(b[20:]) != 0 {
		t.Errorf("% X", b)
	}
	if _, err := decodeFrame(b); err != nil {
		t.Error(err)
	}
}

func TestACorruptFrameIsRefused(t *testing.T) {
	good := frame{seq: 1, cmd: cmdSetParams, data: []byte{1, 2, 3}}.encode()
	for _, at := range []int{5, 25} { // in the header, in the data
		b := append([]byte(nil), good...)
		b[at] ^= 0x40
		if _, err := decodeFrame(b); err == nil {
			t.Errorf("a flipped bit at byte %d was accepted", at)
		}
	}
}

// The destination settings: the address, then the host's port and the lidar's
// own, little-endian.
func TestADestinationIsPackedAsTheSDKPacksIt(t *testing.T) {
	b, err := hostIPCfg(net.ParseIP("192.168.1.50"), 56301, 56300)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{192, 168, 1, 50, 0xED, 0xDB, 0xEC, 0xDB}
	if !bytes.Equal(b, want) {
		t.Errorf("% X, want % X", b, want)
	}
}

func TestParameterPayloads(t *testing.T) {
	set := setParamsData([]keyValue{{keyWorkTargetMode, []byte{stateSampling}}})
	if want := []byte{1, 0, 0, 0, 0x1A, 0, 1, 0, 1}; !bytes.Equal(set, want) {
		t.Errorf("set: % X, want % X", set, want)
	}
	q := queryParamsData([]uint16{keySerialNumber, keyCurWorkState})
	if want := []byte{2, 0, 0, 0, 0x00, 0x80, 0x06, 0x80}; !bytes.Equal(q, want) {
		t.Errorf("query: % X, want % X", q, want)
	}
	ack := []byte{0, 2, 0, 0x06, 0x80, 1, 0, 0x01, 0x00, 0x80, 3, 0, 'S', 'N', 0}
	kvs, err := parseQueryAck(ack)
	if err != nil || len(kvs) != 2 || kvs[0].value[0] != 0x01 || cString(kvs[1].value) != "SN" {
		t.Errorf("%+v %v", kvs, err)
	}
	if _, err := parseQueryAck([]byte{0x01, 0, 0}); err == nil {
		t.Error("a failure code was taken for an answer")
	}
}

func TestADiscoveryReply(t *testing.T) {
	d := make([]byte, 24)
	d[1] = devTypeMid360
	copy(d[2:], "47MDL9N0010015")
	copy(d[18:], []byte{192, 168, 1, 115})
	binary.LittleEndian.PutUint16(d[22:], 56100)
	a, err := parseDiscoveryAck(d)
	if err != nil || a.devType != 9 || a.serial != "47MDL9N0010015" || !a.ip.Equal(net.ParseIP("192.168.1.115")) || a.cmdPort != 56100 {
		t.Errorf("%+v %v", a, err)
	}
}

func TestAPointPacketSurvivesTheRoundTrip(t *testing.T) {
	pts := []point{{x: 1.234, y: -0.5, z: 0.25, refl: 9}, {}, {x: -3, y: 2, z: -0.1}}
	b := encodePacket(dataCartesian32, 3, 123456789, pts, nil)
	if len(b) != 36+3*14 {
		t.Fatalf("%d bytes", len(b))
	}
	p, err := decodePacket(b, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.points) != 2 || p.empty != 1 {
		t.Fatalf("%d returns and %d empty, want 2 and 1: the origin is no return", len(p.points), p.empty)
	}
	if q := p.points[0]; math.Abs(q.x-1.234) > 1e-9 || math.Abs(q.y+0.5) > 1e-9 || q.refl != 9 {
		t.Errorf("%+v", q)
	}
	if p.stamp != 123456789 || p.udpCnt != 3 {
		t.Errorf("stamp %d, udp_cnt %d", p.stamp, p.udpCnt)
	}
}

func TestAnIMUPacketSurvivesTheRoundTrip(t *testing.T) {
	b := encodePacket(dataIMU, 0, 5, nil, []imuSample{{gyro: [3]float64{0.1, -0.2, 0.3}, acc: [3]float64{0, 0, 1}}})
	p, err := decodePacket(b, true)
	if err != nil || len(p.imu) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if s := p.imu[0]; math.Abs(s.gyro[2]-0.3) > 1e-6 || s.acc[2] != 1 {
		t.Errorf("%+v", s)
	}
}

func TestABadDataPacketIsRefused(t *testing.T) {
	good := encodePacket(dataCartesian32, 1, 1, []point{{x: 1}}, nil)

	short := good[:len(good)-1]
	if _, err := decodePacket(short, false); err == nil {
		t.Error("a datagram shorter than its length field was accepted")
	}
	wrongType := append([]byte(nil), good...)
	wrongType[10] = 7
	if _, err := decodePacket(wrongType, false); err == nil {
		t.Error("an unknown data type was accepted")
	}
	badCount := append([]byte(nil), good...)
	binary.LittleEndian.PutUint16(badCount[5:], 2)
	if _, err := decodePacket(badCount, false); err == nil {
		t.Error("a point count that does not fill the data was accepted")
	}
	flipped := append([]byte(nil), good...)
	flipped[40] ^= 1
	if _, err := decodePacket(flipped, true); !errors.Is(err, errDataCRC) {
		t.Errorf("a corrupted point: %v, want errDataCRC", err)
	}
	if _, err := decodePacket(flipped, false); err != nil {
		t.Errorf("with the CRC check off, a well-formed packet was refused: %v", err)
	}
}

// Packets as the Artitrax's Mid-360 sent them (firmware 13.18.2.44), taken
// from a recording on the vehicle on 26 October 2026.
func TestPacketsFromTheDevice(t *testing.T) {
	pts, err := os.ReadFile("testdata/mid360-points.bin")
	if err != nil {
		t.Fatal(err)
	}
	// Its point packets leave the checksum empty. They must still be read,
	// marked as unchecked, and a corrupted one with a checksum filled in
	// must not be.
	p, err := decodePacket(pts, true)
	if err != nil {
		t.Fatalf("a point packet from the device: %v", err)
	}
	if !p.unchecked || p.dataType != dataCartesian32 || len(p.points)+p.empty != 96 {
		t.Errorf("unchecked %v, type %d, %d points and %d empty; want an unchecked 32-bit packet of 96",
			p.unchecked, p.dataType, len(p.points), p.empty)
	}
	for _, q := range p.points {
		if r := math.Sqrt(q.x*q.x + q.y*q.y + q.z*q.z); r < 0.05 || r > 70 {
			t.Errorf("a return at %.2f m, outside what a Mid-360 measures", r)
			break
		}
	}
	wrong := append([]byte(nil), pts...)
	binary.LittleEndian.PutUint32(wrong[24:], 0xDEADBEEF)
	if _, err := decodePacket(wrong, true); !errors.Is(err, errDataCRC) {
		t.Errorf("a filled-in wrong checksum: %v, want errDataCRC", err)
	}

	// Its IMU packets carry the checksum, and it matches.
	imu, err := os.ReadFile("testdata/mid360-imu.bin")
	if err != nil {
		t.Fatal(err)
	}
	p, err = decodePacket(imu, true)
	if err != nil {
		t.Fatalf("an IMU packet from the device: %v", err)
	}
	if p.unchecked || len(p.imu) != 1 {
		t.Fatalf("unchecked %v with %d samples; want a checked packet of one", p.unchecked, len(p.imu))
	}
	a := p.imu[0].acc
	if g := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2]); math.Abs(g-1) > 0.05 {
		t.Errorf("the standing vehicle's accelerometer reads %.3f g, want about 1", g)
	}
}
