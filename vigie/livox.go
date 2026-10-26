/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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

package main

// The Livox Mid-360's network protocol, written from Livox's published
// description (livox_eth_protocol_mid360.md in Livox-SDK/livox_wiki_en) and
// checked against the Livox-SDK2 source where the prose leaves a detail open:
// which bytes each checksum covers, and the byte order of the ports in the
// destination settings. Nothing here has met the device yet; the checksums are
// what will say so if it is wrong.
//
// Everything on the wire is little-endian.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"net"
	"time"
)

// Ports. The lidar listens for discovery on 56000 and for commands on 56100;
// it sends points from 56300 and IMU samples from 56400 to whatever host
// address and ports it was last told.
const (
	portDiscovery   = 56000
	portLidarCmd    = 56100
	portLidarPoints = 56300
	portLidarIMU    = 56400
	portHostCmd     = 56101
	portHostPoints  = 56301
	portHostIMU     = 56401
)

// Command identifiers.
const (
	cmdDiscovery   uint16 = 0x0000
	cmdSetParams   uint16 = 0x0100
	cmdQueryParams uint16 = 0x0101
)

// Parameter keys (cmd 0x0100 and 0x0101).
const (
	keyPointDataType   uint16 = 0x0000
	keyPointHostIPCfg  uint16 = 0x0006
	keyIMUHostIPCfg    uint16 = 0x0007
	keyWorkTargetMode  uint16 = 0x001A
	keyIMUDataEnable   uint16 = 0x001C
	keySerialNumber    uint16 = 0x8000
	keyProductInfo     uint16 = 0x8001
	keyVersionApp      uint16 = 0x8002
	keyCurWorkState    uint16 = 0x8006
	keyCoreTemperature uint16 = 0x8007
	keyDiagStatus      uint16 = 0x800E
)

// Working states (key 0x001A to ask for one, 0x8006 to read it).
const (
	stateSampling = 0x01
	stateIdle     = 0x02
	stateReady    = 0x09
)

const devTypeMid360 = 9

// Data types in a point cloud or IMU packet.
const (
	dataIMU          = 0
	dataCartesian32  = 1
	dataCartesian16  = 2
	dataSpherical    = 3
	dataHeaderLength = 36
)

const (
	frameSOF          = 0xAA
	frameHeaderLength = 24
	cmdTypeRequest    = 0x00
	cmdTypeAck        = 0x01
	senderHost        = 0x00
)

//-------------------------------------Checksums

// crc16CCITTFalse is CRC-16/CCITT-FALSE: polynomial 0x1021, initial 0xFFFF,
// no reflection, no final XOR. It covers the first 18 bytes of a command
// frame's header.
func crc16CCITTFalse(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, c := range b {
		crc ^= uint16(c) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// crc32Livox is the standard CRC-32 (polynomial 0x04C11DB7 reflected, initial
// and final XOR 0xFFFFFFFF), which is what Go's IEEE table computes.
func crc32Livox(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

//-------------------------------------Command frames

// frame is one control command or its acknowledgement.
type frame struct {
	seq     uint32
	cmd     uint16
	cmdType uint8
	sender  uint8
	data    []byte
}

// encode writes a frame: the 24-byte header, its CRC-16 over the first 18
// bytes, and a CRC-32 over the data, zero when there is none.
func (f frame) encode() []byte {
	b := make([]byte, frameHeaderLength+len(f.data))
	b[0] = frameSOF
	b[1] = 0 // protocol version
	binary.LittleEndian.PutUint16(b[2:], uint16(len(b)))
	binary.LittleEndian.PutUint32(b[4:], f.seq)
	binary.LittleEndian.PutUint16(b[8:], f.cmd)
	b[10] = f.cmdType
	b[11] = f.sender
	// b[12:18] reserved
	binary.LittleEndian.PutUint16(b[18:], crc16CCITTFalse(b[:18]))
	if len(f.data) > 0 {
		binary.LittleEndian.PutUint32(b[20:], crc32Livox(f.data))
	}
	copy(b[24:], f.data)
	return b
}

// decodeFrame reads a command frame and checks both checksums. A frame that
// fails either is refused: a configuration acknowledged by garbage is not
// acknowledged.
func decodeFrame(b []byte) (frame, error) {
	if len(b) < frameHeaderLength {
		return frame{}, fmt.Errorf("command frame of %d bytes is shorter than its header", len(b))
	}
	if b[0] != frameSOF {
		return frame{}, fmt.Errorf("command frame starts with 0x%02X, not 0xAA", b[0])
	}
	length := int(binary.LittleEndian.Uint16(b[2:]))
	if length < frameHeaderLength || length > len(b) {
		return frame{}, fmt.Errorf("command frame says %d bytes and %d arrived", length, len(b))
	}
	if got, want := binary.LittleEndian.Uint16(b[18:]), crc16CCITTFalse(b[:18]); got != want {
		return frame{}, fmt.Errorf("command header CRC-16 is 0x%04X, computed 0x%04X", got, want)
	}
	data := b[frameHeaderLength:length]
	want := uint32(0)
	if len(data) > 0 {
		want = crc32Livox(data)
	}
	if got := binary.LittleEndian.Uint32(b[20:]); got != want {
		return frame{}, fmt.Errorf("command data CRC-32 is 0x%08X, computed 0x%08X", got, want)
	}
	return frame{
		seq:     binary.LittleEndian.Uint32(b[4:]),
		cmd:     binary.LittleEndian.Uint16(b[8:]),
		cmdType: b[10],
		sender:  b[11],
		data:    append([]byte(nil), data...),
	}, nil
}

//-------------------------------------Command payloads

// keyValue is one parameter in a set, query reply or push.
type keyValue struct {
	key   uint16
	value []byte
}

// setParamsData is the payload of cmd 0x0100: a count, two reserved bytes, and
// each key with its length and value.
func setParamsData(kvs []keyValue) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint16(b, uint16(len(kvs)))
	for _, kv := range kvs {
		h := make([]byte, 4)
		binary.LittleEndian.PutUint16(h, kv.key)
		binary.LittleEndian.PutUint16(h[2:], uint16(len(kv.value)))
		b = append(b, h...)
		b = append(b, kv.value...)
	}
	return b
}

// queryParamsData is the payload of cmd 0x0101: a count, two reserved bytes,
// and the keys.
func queryParamsData(keys []uint16) []byte {
	b := make([]byte, 4+2*len(keys))
	binary.LittleEndian.PutUint16(b, uint16(len(keys)))
	for i, k := range keys {
		binary.LittleEndian.PutUint16(b[4+2*i:], k)
	}
	return b
}

// parseQueryAck reads the reply to cmd 0x0101: a return code, a count, and the
// keys with their values.
func parseQueryAck(data []byte) ([]keyValue, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("query reply of %d bytes", len(data))
	}
	if data[0] != 0 {
		return nil, fmt.Errorf("the lidar answered the query with %s", retCode(data[0]))
	}
	n := int(binary.LittleEndian.Uint16(data[1:]))
	return parseKeyValues(data[3:], n)
}

func parseKeyValues(b []byte, n int) ([]keyValue, error) {
	var kvs []keyValue
	for i := 0; i < n; i++ {
		if len(b) < 4 {
			return kvs, fmt.Errorf("key %d of %d is cut short", i+1, n)
		}
		k := binary.LittleEndian.Uint16(b)
		l := int(binary.LittleEndian.Uint16(b[2:]))
		if len(b) < 4+l {
			return kvs, fmt.Errorf("key 0x%04X says %d bytes and %d remain", k, l, len(b)-4)
		}
		kvs = append(kvs, keyValue{k, append([]byte(nil), b[4:4+l]...)})
		b = b[4+l:]
	}
	return kvs, nil
}

// hostIPCfg is the value of keys 0x0005-0x0007: where the lidar should send a
// kind of data. The host's address, the host's port, then the lidar's own
// source port, both little-endian, as Livox-SDK2's BuildRequest packs them.
func hostIPCfg(host net.IP, hostPort, lidarPort uint16) ([]byte, error) {
	ip4 := host.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("%v is not an IPv4 address", host)
	}
	b := make([]byte, 8)
	copy(b, ip4)
	binary.LittleEndian.PutUint16(b[4:], hostPort)
	binary.LittleEndian.PutUint16(b[6:], lidarPort)
	return b, nil
}

// discoveryAck is what a lidar says about itself when discovered.
type discoveryAck struct {
	devType uint8
	serial  string
	ip      net.IP
	cmdPort uint16
}

func parseDiscoveryAck(data []byte) (discoveryAck, error) {
	if len(data) < 24 {
		return discoveryAck{}, fmt.Errorf("discovery reply of %d bytes, want 24", len(data))
	}
	if data[0] != 0 {
		return discoveryAck{}, fmt.Errorf("discovery answered with %s", retCode(data[0]))
	}
	return discoveryAck{
		devType: data[1],
		serial:  cString(data[2:18]),
		ip:      net.IPv4(data[18], data[19], data[20], data[21]),
		cmdPort: binary.LittleEndian.Uint16(data[22:]),
	}, nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func retCode(c uint8) string {
	names := map[uint8]string{
		0x00: "success", 0x01: "failure", 0x02: "not permitted in the current state",
		0x03: "a value out of range", 0x20: "a parameter it does not support",
		0x21: "a parameter that takes effect only after a reboot",
		0x22: "a read-only parameter", 0x23: "a wrong parameter length",
		0x24: "a key count that does not match the keys",
	}
	if n, ok := names[c]; ok {
		return fmt.Sprintf("0x%02X (%s)", c, n)
	}
	return fmt.Sprintf("0x%02X", c)
}

//-------------------------------------Data packets

// point is one return in the lidar's own frame, in metres.
type point struct {
	x, y, z float64
	refl    uint8
	tag     uint8
}

// imuSample is one IMU reading in the lidar's frame: rad/s and g.
type imuSample struct {
	gyro [3]float64
	acc  [3]float64
}

// packet is one point cloud or IMU packet.
type packet struct {
	dataType uint8
	udpCnt   uint16
	frameCnt uint8
	stamp    uint64        // ns; see timeType
	timeType uint8         // 0 since power-up, 1 PTP, 2 GPS
	interval time.Duration // first point to last
	points   []point       // returns only: a point with no return is dropped
	empty    int           // points that came back as no return
	imu      []imuSample
	// unchecked is a packet whose sender left the checksum empty.
	unchecked bool
}

var errDataCRC = errors.New("data packet CRC-32 does not match")

// decodePacket reads a point cloud or IMU packet.
//
// The structure is always checked: the length field against the datagram, the
// point count against the data type. checkCRC adds the CRC-32 the protocol
// describes over the timestamp and the data. Livox's own SDK does not check it,
// and on the Artitrax's Mid-360 (firmware 13.18.2.44, October 2026) only the
// IMU packets carry it: every point packet has zero in the field. A stored
// zero is therefore taken as "not filled in", and such a packet is checked for
// its structure alone and marked unchecked; the odds that a real checksum is
// zero are one in four billion. A structural fault is refused either way, and
// a CRC that is filled in and wrong is reported as errDataCRC so the caller
// can count it separately.
func decodePacket(b []byte, checkCRC bool) (packet, error) {
	if len(b) < dataHeaderLength {
		return packet{}, fmt.Errorf("data packet of %d bytes is shorter than its header", len(b))
	}
	if b[0] != 0 {
		return packet{}, fmt.Errorf("data packet version %d, expected 0", b[0])
	}
	length := int(binary.LittleEndian.Uint16(b[1:]))
	if length != len(b) {
		return packet{}, fmt.Errorf("data packet says %d bytes and %d arrived", length, len(b))
	}
	p := packet{
		interval: time.Duration(binary.LittleEndian.Uint16(b[3:])) * 100 * time.Nanosecond,
		udpCnt:   binary.LittleEndian.Uint16(b[7:]),
		frameCnt: b[9],
		dataType: b[10],
		timeType: b[11],
		stamp:    binary.LittleEndian.Uint64(b[28:]),
	}
	n := int(binary.LittleEndian.Uint16(b[5:]))
	size := map[uint8]int{dataIMU: 24, dataCartesian32: 14, dataCartesian16: 8, dataSpherical: 10}[p.dataType]
	if size == 0 {
		return packet{}, fmt.Errorf("data packet of unknown data type %d", p.dataType)
	}
	data := b[dataHeaderLength:]
	if n*size != len(data) {
		return packet{}, fmt.Errorf("data type %d: %d points of %d bytes do not fill %d bytes", p.dataType, n, size, len(data))
	}
	switch stored := binary.LittleEndian.Uint32(b[24:]); {
	case stored == 0:
		p.unchecked = true
	case checkCRC && stored != crc32Livox(b[28:]):
		return p, errDataCRC
	}
	for i := 0; i < n; i++ {
		d := data[i*size:]
		switch p.dataType {
		case dataIMU:
			var s imuSample
			for j := 0; j < 3; j++ {
				s.gyro[j] = float64(math.Float32frombits(binary.LittleEndian.Uint32(d[4*j:])))
				s.acc[j] = float64(math.Float32frombits(binary.LittleEndian.Uint32(d[12+4*j:])))
			}
			p.imu = append(p.imu, s)
		case dataCartesian32:
			x := int32(binary.LittleEndian.Uint32(d))
			y := int32(binary.LittleEndian.Uint32(d[4:]))
			z := int32(binary.LittleEndian.Uint32(d[8:]))
			p.addPoint(float64(x)/1000, float64(y)/1000, float64(z)/1000, d[12], d[13])
		case dataCartesian16:
			x := int16(binary.LittleEndian.Uint16(d))
			y := int16(binary.LittleEndian.Uint16(d[2:]))
			z := int16(binary.LittleEndian.Uint16(d[4:]))
			p.addPoint(float64(x)/100, float64(y)/100, float64(z)/100, d[6], d[7])
		case dataSpherical:
			r := float64(binary.LittleEndian.Uint32(d)) / 1000
			theta := float64(binary.LittleEndian.Uint16(d[4:])) / 100 * math.Pi / 180
			phi := float64(binary.LittleEndian.Uint16(d[6:])) / 100 * math.Pi / 180
			p.addPoint(r*math.Sin(theta)*math.Cos(phi), r*math.Sin(theta)*math.Sin(phi), r*math.Cos(theta), d[8], d[9])
		}
	}
	return p, nil
}

// addPoint keeps a return and counts a silence. The Mid-360 reports "nothing
// within range" as the origin, and a consumer that took (0, 0, 0) for a point
// would see an obstacle inside the sensor.
func (p *packet) addPoint(x, y, z float64, refl, tag uint8) {
	if x == 0 && y == 0 && z == 0 {
		p.empty++
		return
	}
	p.points = append(p.points, point{x, y, z, refl, tag})
}
