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
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// lidar is the command channel to one Mid-360.
type lidar struct {
	conn *net.UDPConn // bound to the host command port
	addr *net.UDPAddr // the lidar's command port

	mu  sync.Mutex
	seq uint32
}

// openCommand binds the host's command port. Discovery replies are broadcast,
// so the socket listens on every interface.
func openCommand() (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: portHostCmd})
	if err != nil {
		return nil, fmt.Errorf("cannot listen on UDP %d for the lidar's replies: %w", portHostCmd, err)
	}
	return conn, nil
}

// broadcastAddresses are where a discovery request goes: the limited broadcast
// and every IPv4 interface's own broadcast address.
//
// The limited broadcast alone is not enough. Linux sends 255.255.255.255 out of
// the interface of the default route, and the lidar is usually on a second
// Ethernet port with a cable of its own, where that never arrives.
func broadcastAddresses() []*net.UDPAddr {
	addrs := []*net.UDPAddr{{IP: net.IPv4bcast, Port: portDiscovery}}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		ias, _ := ifc.Addrs()
		for _, a := range ias {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip4, mask := ipn.IP.To4(), net.IP(ipn.Mask).To4()
			if mask == nil {
				continue
			}
			bc := make(net.IP, 4)
			for i := range bc {
				bc[i] = ip4[i] | ^mask[i]
			}
			addrs = append(addrs, &net.UDPAddr{IP: bc, Port: portDiscovery})
		}
	}
	return addrs
}

// discover broadcasts a discovery request and collects the replies until the
// timeout.
func discover(conn *net.UDPConn, timeout time.Duration) ([]discoveryAck, error) {
	req := frame{seq: 1, cmd: cmdDiscovery, cmdType: cmdTypeRequest, sender: senderHost}.encode()
	sent := 0
	for _, a := range broadcastAddresses() {
		if _, err := conn.WriteToUDP(req, a); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return nil, errors.New("could not send a discovery broadcast on any interface")
	}
	seen := make(map[string]discoveryAck)
	buf := make([]byte, 2048)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		f, err := decodeFrame(buf[:n])
		if err != nil || f.cmd != cmdDiscovery || f.cmdType != cmdTypeAck {
			continue
		}
		ack, err := parseDiscoveryAck(f.data)
		if err != nil {
			log.Printf("vigie: discovery reply refused: %v", err)
			continue
		}
		seen[ack.serial+ack.ip.String()] = ack
	}
	conn.SetReadDeadline(time.Time{})
	var out []discoveryAck
	for _, a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].serial < out[j].serial })
	return out, nil
}

// request sends a command and waits for its acknowledgement, three times
// before giving up. The lidar answers from its command port with the request's
// sequence number.
func (l *lidar) request(cmd uint16, data []byte) (frame, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := make([]byte, 2048)
	for attempt := 0; attempt < 3; attempt++ {
		l.seq++
		req := frame{seq: l.seq, cmd: cmd, cmdType: cmdTypeRequest, sender: senderHost, data: data}.encode()
		if _, err := l.conn.WriteToUDP(req, l.addr); err != nil {
			return frame{}, err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			l.conn.SetReadDeadline(deadline)
			n, _, err := l.conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			f, err := decodeFrame(buf[:n])
			if err != nil {
				log.Printf("vigie: a reply from the lidar was refused: %v", err)
				continue
			}
			if f.cmdType == cmdTypeAck && f.cmd == cmd && f.seq == l.seq {
				l.conn.SetReadDeadline(time.Time{})
				return f, nil
			}
		}
	}
	l.conn.SetReadDeadline(time.Time{})
	return frame{}, fmt.Errorf("no acknowledgement of command 0x%04X from %v after three tries", cmd, l.addr)
}

// query reads parameters.
func (l *lidar) query(keys ...uint16) (map[uint16][]byte, error) {
	f, err := l.request(cmdQueryParams, queryParamsData(keys))
	if err != nil {
		return nil, err
	}
	kvs, err := parseQueryAck(f.data)
	if err != nil {
		return nil, err
	}
	out := make(map[uint16][]byte)
	for _, kv := range kvs {
		out[kv.key] = kv.value
	}
	return out, nil
}

// set writes parameters. A key that needs a reboot is reported as such, not as
// a failure: the setting is stored and will apply.
func (l *lidar) set(kvs ...keyValue) error {
	f, err := l.request(cmdSetParams, setParamsData(kvs))
	if err != nil {
		return err
	}
	if len(f.data) < 1 {
		return errors.New("empty acknowledgement of a parameter setting")
	}
	switch code := f.data[0]; code {
	case 0x00:
		return nil
	case 0x21:
		log.Println("vigie: the lidar stored a setting that takes effect only after it is power-cycled")
		return nil
	default:
		key := uint16(0)
		if len(f.data) >= 3 {
			key = binary.LittleEndian.Uint16(f.data[1:])
		}
		return fmt.Errorf("the lidar refused key 0x%04X with %s", key, retCode(code))
	}
}

// hostAddressFor is the address of this machine on the interface that reaches
// the lidar, found by asking the kernel which one it would use.
func hostAddressFor(lidarIP net.IP) (net.IP, error) {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: lidarIP, Port: portLidarCmd})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}

// configure tells the lidar to send 32-bit Cartesian points and IMU samples to
// this host, and to sample.
func (l *lidar) configure(host net.IP) error {
	points, err := hostIPCfg(host, portHostPoints, portLidarPoints)
	if err != nil {
		return err
	}
	imu, err := hostIPCfg(host, portHostIMU, portLidarIMU)
	if err != nil {
		return err
	}
	if err := l.set(
		keyValue{keyPointDataType, []byte{dataCartesian32}},
		keyValue{keyPointHostIPCfg, points},
		keyValue{keyIMUHostIPCfg, imu},
		keyValue{keyIMUDataEnable, []byte{1}},
	); err != nil {
		return err
	}
	return l.set(keyValue{keyWorkTargetMode, []byte{stateSampling}})
}

// describe reads what the lidar says about itself, for a person.
func (l *lidar) describe() string {
	v, err := l.query(keySerialNumber, keyProductInfo, keyVersionApp, keyCurWorkState,
		keyCoreTemperature, keyDiagStatus, keyPointDataType, keyPointHostIPCfg, keyIMUHostIPCfg, keyIMUDataEnable)
	if err != nil {
		return fmt.Sprintf("could not read its parameters: %v", err)
	}
	var b strings.Builder
	line := func(name, value string) { fmt.Fprintf(&b, "    %-24s %s\n", name, value) }
	if s, ok := v[keySerialNumber]; ok {
		line("serial number", cString(s))
	}
	if s, ok := v[keyProductInfo]; ok {
		line("product", cString(s))
	}
	if s, ok := v[keyVersionApp]; ok && len(s) == 4 {
		line("firmware", fmt.Sprintf("%d.%d.%d.%d", s[0], s[1], s[2], s[3]))
	}
	if s, ok := v[keyCurWorkState]; ok && len(s) == 1 {
		line("working state", stateName(s[0]))
	}
	if s, ok := v[keyCoreTemperature]; ok && len(s) == 4 {
		line("core temperature", fmt.Sprintf("%.2f °C", float64(int32(binary.LittleEndian.Uint32(s)))/100))
	}
	if s, ok := v[keyDiagStatus]; ok && len(s) == 2 {
		line("diagnostic status", fmt.Sprintf("0x%04X (0 is normal)", binary.LittleEndian.Uint16(s)))
	}
	if s, ok := v[keyPointDataType]; ok && len(s) == 1 {
		line("point data type", fmt.Sprintf("%d (1 is 32-bit Cartesian)", s[0]))
	}
	for _, k := range []struct {
		key  uint16
		name string
	}{{keyPointHostIPCfg, "points sent to"}, {keyIMUHostIPCfg, "IMU sent to"}} {
		if s, ok := v[k.key]; ok && len(s) == 8 {
			line(k.name, fmt.Sprintf("%d.%d.%d.%d:%d (from its port %d)", s[0], s[1], s[2], s[3],
				binary.LittleEndian.Uint16(s[4:]), binary.LittleEndian.Uint16(s[6:])))
		}
	}
	if s, ok := v[keyIMUDataEnable]; ok && len(s) == 1 {
		line("IMU enabled", fmt.Sprintf("%v", s[0] == 1))
	}
	return b.String()
}

func stateName(s uint8) string {
	names := map[uint8]string{0x01: "sampling", 0x02: "idle", 0x04: "error", 0x05: "self-check",
		0x06: "motor starting", 0x08: "upgrading", 0x09: "ready"}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprintf("0x%02X", s)
}
