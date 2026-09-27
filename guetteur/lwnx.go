/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Gabriel Axheim Gustafsson, Luleå - the command set, checked on the sensor
 ***************************************************************************SDG*/

package main

// The LightWare LWNX serial protocol, as spoken by the SF45/B.
//
// Checked against SF45B-Product-Guide-v3.pdf (revision 3, 7 July 2025), and run
// on the sensor by the students (their commit e5d4545, "working guetteur and
// loader"). The first version of this file was written without the guide and
// guessed wrong about the units, the stream value and the sector commands; the
// command set below is theirs.
//
// Confirmed by the guide:
//   - framing: 0xAA, a 16-bit flags word whose top ten bits are the payload
//     length including the ID byte and whose bit 0 is the write flag, the ID,
//     the data, and a CRC-16 over everything before it;
//   - 27 Distance output selects the fields of 44 by bit, and they are packed
//     in bit order: bit 0 first return raw, int16 cm; bit 8 yaw, int16 1/100°;
//   - 30 Stream = 5 streams 44 at the update rate;
//   - a lost signal reads -1000 cm, so every non-positive distance is "no
//     return" and never a distance — the rule the whole guetteur rests on.
//
// Where the guide is unclear or disagrees with this code, and the sensor has so
// far accepted what is sent:
//   - 96 Scan enabled is listed as "1/Uint16"; one byte is sent;
//   - 98 and 99, the sector limits, are listed as "4/uint32" while taking
//     values from -170 to -5; float32 is sent, as for the alarm angles beside
//     them. A sensor that refused them would still scan its previous sector,
//     so the log line saying the sector could not be set matters;
//   - 66 Update rate: in revision 3 value 8 is 1250 samples/s, and accuracy is
//     ±5 cm up to 500/s and ±10 cm above. An older table gave 8 as 388 Hz. The
//     value is configuration, and which firmware is fitted decides what it
//     means.

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"time"

	serial "go.bug.st/serial"
)

const (
	lwnxStartByte = 0xAA

	// LWNX Command IDs for LightWare SF45/B
	cmdProductName       = 0
	cmdDistanceOutput    = 27  // RW uint32: bitmask selecting fields in cmd 44
	cmdStream            = 30  // RW uint32: 0 = disabled, 5 = stream distance data cm
	cmdDistanceDataCm    = 44  // R variable length: measurement distance data in cm
	cmdUpdateRate        = 66  // RW uint8: rev 3 table 1=50 2=100 3=200 4=400 5=500 6=625 7=1000 8=1250 … 12=5000 per second
	cmdLostSignalCounter = 195 // RW uint8: returns lost before -1000 is reported (the students had 76)
	cmdBaudRate          = 79  // RW uint8: serial baud rate
	cmdScanSpeed         = 85  // RW uint16: cycleDelay in ms (5 to 2000, default 5)
	cmdScanEnable        = 96  // RW, "1/Uint16" in the guide; one byte is sent: 1 = scan, 0 = stop
	cmdScanPosition      = 97  // RW float32: current scan angle
	cmdScanLowAngle      = 98  // RW, -170 to -5 deg (counter-clockwise limit); sent as float32
	cmdScanHighAngle     = 99  // RW, 5 to 170 deg (clockwise limit); sent as float32

	// Command 27 bitmask: bit 0 = raw first return [cm], bit 8 = yaw angle [1/100 deg]
	distanceOutputFields = (1 << 0) | (1 << 8) // 0x101

	// Lost signal value reported by SF45/B (-1000 cm). parseDistanceData
	// treats it, and anything else not positive, as no return.
	lostSignalValue = -1000

	// A payload longer than this is a framing error rather than a big message.
	lwnxMaxPayload = 1024
)

type serialSource struct {
	cfg  GuetteurConfig
	port serial.Port
	out  chan sweep

	// Counters, reported when the source gives up, so a failure says which
	// kind of failure it was.
	framesSeen, framesBad int
}

func newSerialSource(cfg GuetteurConfig) (*serialSource, error) {
	mode := &serial.Mode{
		BaudRate: cfg.BaudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	port, err := serial.Open(cfg.Port, mode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.Port, err)
	}
	if err := port.SetReadTimeout(time.Second); err != nil {
		port.Close()
		return nil, fmt.Errorf("set read timeout: %w", err)
	}
	return &serialSource{cfg: cfg, port: port, out: make(chan sweep, 2)}, nil
}

func (s *serialSource) close() error { return s.port.Close() }

// setSector sets the scanned arc limits on the SF45/B.
// Command 98 (low angle) must be negative (-170° to -5°),
// Command 99 (high angle) must be positive (5° to 170°).
func (s *serialSource) setSector(degrees float64) error {
	half := degrees / 2
	if half < 5 {
		half = 5
	}
	if half > 160 {
		half = 160
	}
	if err := s.writeFrame(cmdScanLowAngle, f32(float32(-half))); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	return s.writeFrame(cmdScanHighAngle, f32(float32(half)))
}

func (s *serialSource) sweeps(ctx context.Context) (<-chan sweep, error) {
	// 1. Halt any ongoing stream first
	if err := s.writeFrame(cmdStream, u32(0)); err != nil {
		return nil, fmt.Errorf("stop stream: %w", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 2. Configure distance output: raw first return in cm (bit 0) and yaw angle (bit 8)
	if err := s.writeFrame(cmdDistanceOutput, u32(distanceOutputFields)); err != nil {
		return nil, fmt.Errorf("configure distance output: %w", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 3. Set the measurement update rate (an enumeration; see cmdUpdateRate)
	if err := s.writeFrame(cmdUpdateRate, u8(uint8(s.cfg.UpdateRate))); err != nil {
		log.Printf("guetteur: could not set update rate: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 4. Set the scan speed: the delay between scan positions, 5 to 2000
	if err := s.writeFrame(cmdScanSpeed, u16(uint16(s.cfg.ScanDelay))); err != nil {
		log.Printf("guetteur: could not set scan speed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 5. Configure scan sector limits
	if err := s.setSector(s.cfg.SectorDegrees); err != nil {
		log.Printf("guetteur: could not set scan sector: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 6. Ensure the scanning motor is running
	if err := s.writeFrame(cmdScanEnable, u8(1)); err != nil {
		log.Printf("guetteur: could not enable scanning motor: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// 7. Start streaming Command 44 distance data in cm (stream type 5)
	if err := s.writeFrame(cmdStream, u32(5)); err != nil {
		return nil, fmt.Errorf("request distance stream: %w", err)
	}

	go s.read(ctx)
	return s.out, nil
}

// read assembles points into sweeps. The sensor reports one point at a time;
// a sweep ends when the scan angle reverses direction, which is how a
// mechanically swept sensor announces it has reached the end of its arc.
func (s *serialSource) read(ctx context.Context) {
	defer close(s.out)

	var (
		cur       sweep
		lastAngle = math.NaN()
		rising    bool
		dirSet    bool
		started   = time.Now()
	)

	// Minimum points required before a direction reversal is confirmed as a new sweep.
	// At 388 Hz across a 160° sector, each full sweep takes ~50-100 readings.
	// Requiring at least 15 points prevents jitter or turnaround pause from fragmenting sweeps.
	const minPointsPerSweep = 15

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		id, payload, err := s.readFrame()
		if err != nil {
			// A timeout is not a failure: the sensor may simply be quiet.
			if ctx.Err() != nil {
				return
			}
			continue
		}
		// Discard non-measurement frames (e.g. command responses from setup)
		if id != cmdDistanceDataCm {
			continue
		}
		angle, distance, ok := parseDistanceData(payload, s.cfg.MaxRange)
		if !ok {
			continue
		}

		// Turnaround detection: only evaluate direction change if angle actually changed
		if !math.IsNaN(lastAngle) && angle != lastAngle {
			nowRising := angle > lastAngle
			if !dirSet {
				rising = nowRising
				dirSet = true
			} else if nowRising != rising && len(cur.angles) >= minPointsPerSweep {
				cur.taken = time.Now()
				cur.duration = time.Since(started)
				select {
				case s.out <- cur:
				case <-ctx.Done():
					return
				default:
					// A consumer that is not keeping up gets the newest sweep
					// next time rather than an old one now. Stale geometry is
					// worse than a gap.
				}
				cur = sweep{}
				started = time.Now()
				rising = nowRising
			}
		}
		lastAngle = angle

		cur.angles = append(cur.angles, angle)
		cur.distances = append(cur.distances, math.Max(distance, 0))
		cur.valid = append(cur.valid, distance > 0)
	}
}

// parseDistanceData pulls one (angle, distance) pair out of a Command 44 message.
// With Distance Output (Command 27) set to 0x101:
//
//	Bytes 0-1: First return raw in cm (int16 LE)
//	Bytes 2-3: Scan yaw angle in hundredths of a degree (int16 LE)
func parseDistanceData(payload []byte, maxRange float64) (angleDeg, distanceM float64, ok bool) {
	if len(payload) < 4 {
		return 0, 0, false
	}
	cm := int16(binary.LittleEndian.Uint16(payload[0:2]))
	centideg := int16(binary.LittleEndian.Uint16(payload[2:4]))

	angleDeg = float64(centideg) / 100.0

	// SF45/B signals lost signal / no return as -1000 cm.
	// Any non-positive distance is not a valid return.
	if cm <= 0 {
		return angleDeg, 0, true // point taken, but not a return
	}

	distanceM = float64(cm) / 100.0 // centimeters to metres
	if distanceM > maxRange {
		return angleDeg, 0, true // beyond range is also not a return
	}
	return angleDeg, distanceM, true
}

//-------------------------------------Framing

// readFrame reads one CRC-checked LWNX frame.
//
//	0xAA | flags (uint16 LE: length<<6 | read/write bit) | id | payload | crc16
func (s *serialSource) readFrame() (id byte, payload []byte, err error) {
	// Hunt for the start byte. Everything before it is either noise or the
	// tail of a frame we have already given up on.
	b := make([]byte, 1)
	for {
		if _, err := readFull(s.port, b); err != nil {
			return 0, nil, err
		}
		if b[0] == lwnxStartByte {
			break
		}
	}

	header := make([]byte, 2)
	if _, err := readFull(s.port, header); err != nil {
		return 0, nil, err
	}
	flags := binary.LittleEndian.Uint16(header)
	length := int(flags>>6) - 1 // the id byte is counted in the length
	if length < 0 || length > lwnxMaxPayload {
		s.framesBad++
		return 0, nil, fmt.Errorf("implausible payload length %d", length)
	}

	body := make([]byte, 1+length)
	if _, err := readFull(s.port, body); err != nil {
		return 0, nil, err
	}
	crcBytes := make([]byte, 2)
	if _, err := readFull(s.port, crcBytes); err != nil {
		return 0, nil, err
	}

	frame := append([]byte{lwnxStartByte, header[0], header[1]}, body...)
	want := binary.LittleEndian.Uint16(crcBytes)
	s.framesSeen++
	if got := crc16(frame); got != want {
		s.framesBad++
		if s.framesBad == 50 && s.framesBad == s.framesSeen {
			log.Printf("guetteur: %d frames read and none passed CRC — "+
				"the framing in lwnx.go is wrong for this sensor, check it against the product guide",
				s.framesBad)
		}
		return 0, nil, fmt.Errorf("crc mismatch")
	}
	return body[0], body[1:], nil
}

func (s *serialSource) writeFrame(id byte, payload []byte) error {
	flags := uint16(len(payload)+1)<<6 | 1
	frame := []byte{lwnxStartByte, byte(flags), byte(flags >> 8), id}
	frame = append(frame, payload...)
	sum := crc16(frame)
	frame = append(frame, byte(sum), byte(sum>>8))
	_, err := s.port.Write(frame)
	return err
}

// crc16 is CRC-16-CCITT, polynomial 0x1021, starting from zero (the variant
// sometimes called XMODEM), as in the guide's own example.
func crc16(data []byte) uint16 {
	var crc uint16 = 0
	for _, b := range data {
		code := crc >> 8
		code ^= uint16(b)
		code ^= code >> 4
		crc <<= 8
		crc ^= code
		code <<= 5
		crc ^= code
		code <<= 7
		crc ^= code
	}
	return crc
}

func readFull(p serial.Port, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := p.Read(buf[got:])
		if err != nil {
			return got, err
		}
		if n == 0 {
			return got, fmt.Errorf("serial read timed out")
		}
		got += n
	}
	return got, nil
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func u8(v uint8) []byte {
	return []byte{v}
}

func f32(v float32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, math.Float32bits(v))
	return b
}
