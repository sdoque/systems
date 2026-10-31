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
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"
)

// datagram is one UDP payload as it arrived, and on which host port: the
// point port or the IMU port.
type datagram struct {
	port uint16
	at   time.Time
	data []byte
}

// source is where datagrams come from: the lidar, the simulator, or a
// recording of the lidar. All three hand over the bytes as they would arrive,
// so everything after the socket is exercised the same way whichever runs.
type source interface {
	run(ctx context.Context, out chan<- datagram) error
}

//-------------------------------------The lidar

// livoxSource receives the lidar's points and IMU samples.
type livoxSource struct{}

func (livoxSource) run(ctx context.Context, out chan<- datagram) error {
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, port := range []uint16{portHostPoints, portHostIMU} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(port)})
		if err != nil {
			return fmt.Errorf("cannot listen on UDP %d: %w", port, err)
		}
		// Points arrive at about 200 000 a second, in 1 380-byte datagrams:
		// a large receive buffer rides out a garbage collection.
		conn.SetReadBuffer(4 << 20)
		wg.Add(1)
		go func(conn *net.UDPConn, port uint16) {
			defer wg.Done()
			defer conn.Close()
			go func() { <-ctx.Done(); conn.Close() }()
			buf := make([]byte, 65536)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					if ctx.Err() == nil {
						errs <- err
					}
					return
				}
				d := datagram{port: port, at: time.Now(), data: append([]byte(nil), buf[:n]...)}
				select {
				case out <- d:
				case <-ctx.Done():
					return
				}
			}
		}(conn, port)
	}
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

//-------------------------------------Recording and replay

// A recording is a header line and then, per datagram, the arrival time in
// nanoseconds, the host port and the length, little-endian, and the bytes.
const recordMagic = "VIGIE-RECORDING-1\n"

type recorder struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func newRecorder(path string) (*recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.WriteString(recordMagic); err != nil {
		f.Close()
		return nil, err
	}
	return &recorder{f: f, w: w}, nil
}

func (r *recorder) write(d datagram) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := make([]byte, 12)
	binary.LittleEndian.PutUint64(h, uint64(d.at.UnixNano()))
	binary.LittleEndian.PutUint16(h[8:], d.port)
	binary.LittleEndian.PutUint16(h[10:], uint16(len(d.data)))
	if _, err := r.w.Write(h); err != nil {
		return err
	}
	_, err := r.w.Write(d.data)
	return err
}

func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.w.Flush(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}

// replaySource plays a recording back at the pace it was recorded, with the
// times shifted to now, and then stops.
type replaySource struct{ path string }

func (s replaySource) run(ctx context.Context, out chan<- datagram) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	magic := make([]byte, len(recordMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != recordMagic {
		return fmt.Errorf("%s is not a vigie recording", s.path)
	}
	var offset time.Duration
	first := true
	start := time.Now()
	h := make([]byte, 12)
	for {
		if _, err := io.ReadFull(r, h); err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("vigie: the recording %s has ended", s.path)
				return nil
			}
			return err
		}
		at := time.Unix(0, int64(binary.LittleEndian.Uint64(h)))
		data := make([]byte, binary.LittleEndian.Uint16(h[10:]))
		if _, err := io.ReadFull(r, data); err != nil {
			return fmt.Errorf("the recording is cut short: %w", err)
		}
		if first {
			offset, first = start.Sub(at), false
		}
		when := at.Add(offset)
		if wait := time.Until(when); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil
			}
		}
		select {
		case out <- datagram{port: binary.LittleEndian.Uint16(h[8:]), at: when, data: data}:
		case <-ctx.Done():
			return nil
		}
	}
}

//-------------------------------------The simulator

// simulatedSource is a Mid-360 driving slowly down a tunnel: 3 m wide, 3 m
// high, ending at a face 25 m ahead, with a side gallery to the left 6 m ahead,
// so a scan has something in it to tell one place from another. It makes real packets — headers,
// checksums, a share of no-returns — so the decoding is exercised too, not
// only what comes after it.
type simulatedSource struct {
	height  float64 // the lidar above the floor
	speed   float64 // m/s
	yawRate float64 // rad/s
}

func (s simulatedSource) run(ctx context.Context, out chan<- datagram) error {
	rng := rand.New(rand.NewSource(1))
	const pointsPerPacket = 96
	const packetsPerSecond = 2000 // 192 000 points a second, as the Mid-360
	tick := time.NewTicker(time.Second / 200)
	defer tick.Stop()
	start := time.Now()
	var udpCnt uint16
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			travelled := s.speed * now.Sub(start).Seconds()
			for k := 0; k < packetsPerSecond/200; k++ {
				pts := make([]point, pointsPerPacket)
				for i := range pts {
					pts[i] = s.ray(rng, travelled)
				}
				udpCnt++
				pkt := encodePacket(dataCartesian32, udpCnt, uint64(now.UnixNano()), pts, nil)
				if !s.send(ctx, out, datagram{port: portHostPoints, at: now, data: pkt}) {
					return nil
				}
			}
			gyro := [3]float64{rng.NormFloat64() * 0.002, rng.NormFloat64() * 0.002, s.yawRate + rng.NormFloat64()*0.002}
			acc := [3]float64{rng.NormFloat64() * 0.01, rng.NormFloat64() * 0.01, 1 + rng.NormFloat64()*0.01}
			pkt := encodePacket(dataIMU, 0, uint64(now.UnixNano()), nil, []imuSample{{gyro, acc}})
			if !s.send(ctx, out, datagram{port: portHostIMU, at: now, data: pkt}) {
				return nil
			}
		}
	}
}

func (s simulatedSource) send(ctx context.Context, out chan<- datagram, d datagram) bool {
	select {
	case out <- d:
		return true
	case <-ctx.Done():
		return false
	}
}

// ray casts one beam in a random direction of the Mid-360's field of view —
// all round, from 7° below the horizon to 52° above — against the tunnel.
func (s simulatedSource) ray(rng *rand.Rand, travelled float64) point {
	az := rng.Float64() * 2 * math.Pi
	el := (-7 + rng.Float64()*59) * math.Pi / 180
	dx, dy, dz := math.Cos(el)*math.Cos(az), math.Cos(el)*math.Sin(az), math.Sin(el)
	best := math.Inf(1)
	hit := func(t float64) {
		if t > 0.1 && t < best {
			best = t
		}
	}
	// Floor and roof.
	if dz < 0 {
		hit(-s.height / dz)
	} else if dz > 0 {
		hit((3 - s.height) / dz)
	}
	// Walls at y = ±1.5, with a 2 m gallery in the left wall 6 m ahead.
	if dy != 0 {
		for _, wy := range []float64{1.5, -1.5} {
			t := wy / dy
			if t <= 0 {
				continue
			}
			x := travelled + t*dx
			if wy > 0 && x > 6 && x < 8 {
				continue // the gallery's mouth
			}
			hit(t)
		}
	}
	// The face the tunnel ends at, 25 m from where the vehicle started.
	if dx > 0 {
		t := (25 - travelled) / dx
		if y := t * dy; math.Abs(y) < 1.5 {
			hit(t)
		}
	}
	// The gallery's far wall.
	if dy > 0 {
		t := 4.5 / dy
		x := travelled + t*dx
		if x > 6 && x < 8 {
			hit(t)
		}
	}
	if math.IsInf(best, 1) || best > 40 {
		return point{} // no return: the Mid-360 reports the origin
	}
	best += rng.NormFloat64() * 0.02
	return point{x: best * dx, y: best * dy, z: best * dz, refl: 80}
}

// encodePacket writes a point cloud or IMU packet as the Mid-360 sends it.
func encodePacket(dataType uint8, udpCnt uint16, stamp uint64, pts []point, imu []imuSample) []byte {
	size := map[uint8]int{dataIMU: 24, dataCartesian32: 14}[dataType]
	n := len(pts)
	if dataType == dataIMU {
		n = len(imu)
	}
	b := make([]byte, dataHeaderLength+n*size)
	binary.LittleEndian.PutUint16(b[1:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[3:], 10) // 1 µs between first and last
	binary.LittleEndian.PutUint16(b[5:], uint16(n))
	binary.LittleEndian.PutUint16(b[7:], udpCnt)
	b[10] = dataType
	binary.LittleEndian.PutUint64(b[28:], stamp)
	d := b[dataHeaderLength:]
	for i := 0; i < n; i++ {
		e := d[i*size:]
		if dataType == dataIMU {
			for j := 0; j < 3; j++ {
				binary.LittleEndian.PutUint32(e[4*j:], math.Float32bits(float32(imu[i].gyro[j])))
				binary.LittleEndian.PutUint32(e[12+4*j:], math.Float32bits(float32(imu[i].acc[j])))
			}
			continue
		}
		p := pts[i]
		binary.LittleEndian.PutUint32(e, uint32(int32(math.Round(p.x*1000))))
		binary.LittleEndian.PutUint32(e[4:], uint32(int32(math.Round(p.y*1000))))
		binary.LittleEndian.PutUint32(e[8:], uint32(int32(math.Round(p.z*1000))))
		e[12], e[13] = p.refl, p.tag
	}
	binary.LittleEndian.PutUint32(b[24:], crc32Livox(b[28:]))
	return b
}
