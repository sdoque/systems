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
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeLidar answers commands on 127.0.0.1:56100 as the protocol says a
// Mid-360 does, and remembers the settings it was given.
type fakeLidar struct {
	conn *net.UDPConn
	mu   sync.Mutex
	set  map[uint16][]byte
	// ignore makes it drop this many requests first, to exercise the retry.
	ignore int
}

func startFakeLidar(t *testing.T) *fakeLidar {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portLidarCmd})
	if err != nil {
		t.Skipf("cannot listen on %d for a fake lidar: %v", portLidarCmd, err)
	}
	f := &fakeLidar{conn: conn, set: map[uint16][]byte{}}
	t.Cleanup(func() { conn.Close() })
	go f.serve()
	return f
}

func (f *fakeLidar) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		req, err := decodeFrame(buf[:n])
		if err != nil {
			continue
		}
		f.mu.Lock()
		if f.ignore > 0 {
			f.ignore--
			f.mu.Unlock()
			continue
		}
		var data []byte
		switch req.cmd {
		case cmdSetParams:
			kvs, _ := parseKeyValues(req.data[4:], int(binary.LittleEndian.Uint16(req.data)))
			for _, kv := range kvs {
				f.set[kv.key] = kv.value
			}
			data = []byte{0, 0, 0}
		case cmdQueryParams:
			n := int(binary.LittleEndian.Uint16(req.data))
			data = []byte{0, 0, 0}
			count := 0
			for i := 0; i < n; i++ {
				key := binary.LittleEndian.Uint16(req.data[4+2*i:])
				var v []byte
				switch key {
				case keySerialNumber:
					v = append([]byte("47MDL9N0010015"), 0, 0)
				case keyProductInfo:
					v = []byte("Mid-360 2024/03/01\x00")
				case keyVersionApp:
					v = []byte{10, 11, 0, 7}
				case keyCurWorkState:
					v = []byte{stateSampling}
				case keyPointHostIPCfg:
					v = []byte{192, 168, 1, 50, 0xED, 0xDB, 0xEC, 0xDB}
				default:
					continue
				}
				h := make([]byte, 4)
				binary.LittleEndian.PutUint16(h, key)
				binary.LittleEndian.PutUint16(h[2:], uint16(len(v)))
				data = append(data, h...)
				data = append(data, v...)
				count++
			}
			binary.LittleEndian.PutUint16(data[1:], uint16(count))
		}
		f.mu.Unlock()
		ack := frame{seq: req.seq, cmd: req.cmd, cmdType: cmdTypeAck, sender: 1, data: data}.encode()
		f.conn.WriteToUDP(ack, from)
	}
}

func TestTalkingToALidar(t *testing.T) {
	fake := startFakeLidar(t)
	l, err := connect("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer l.conn.Close()

	// A lost request is sent again.
	fake.mu.Lock()
	fake.ignore = 1
	fake.mu.Unlock()
	desc := l.describe()
	for _, want := range []string{"47MDL9N0010015", "Mid-360 2024/03/01", "10.11.0.7", "sampling", "192.168.1.50:56301 (from its port 56300)"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description lacks %q:\n%s", want, desc)
		}
	}

	if err := l.configure(net.IPv4(192, 168, 1, 50)); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for key, want := range map[uint16][]byte{
		keyPointDataType:  {dataCartesian32},
		keyPointHostIPCfg: {192, 168, 1, 50, 0xED, 0xDB, 0xEC, 0xDB},
		keyIMUHostIPCfg:   {192, 168, 1, 50, 0x51, 0xDC, 0x50, 0xDC},
		keyIMUDataEnable:  {1},
		keyWorkTargetMode: {stateSampling},
	} {
		if got := fake.set[key]; !bytes.Equal(got, want) {
			t.Errorf("key 0x%04X set to % X, want % X", key, got, want)
		}
	}
}
