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

// The LightWare LWNX serial protocol, as spoken by the SF45/B.
//
// ==========================  READ THIS FIRST  ==========================
// This framing is written from the published description of LWNX and has NOT
// been checked against SF45B-Product-Guide-v3.pdf, which nobody has read past
// the cover. Three things in particular are unconfirmed:
//
//   - the message IDs below, especially the streaming distance-data message;
//   - the field layout inside that message;
//   - how a no-return is signalled, which is the load-bearing detail of the
//     whole guetteur design and the one thing that must not be guessed.
//
// What makes it safe to ship anyway is the CRC. Every frame is checked, and a
// frame that does not validate is discarded and counted. If the framing here is
// wrong, no frame ever validates, and the system says "no valid frames" instead
// of publishing plausible rubbish. A wrong guess is loud, not silent — which is
// the opposite of how the file-based integration fails today.
//
// Before this drives anything: read the guide, confirm the three points above,
// and delete this banner.
// =======================================================================

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

	// Message IDs. UNCONFIRMED — see the banner.
	msgDistanceData = 44
	msgStreamSelect = 30
	msgScanSpeed    = 85
	msgSectorLeft   = 98
	msgSectorRight  = 99

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

// setSector asks the sensor to narrow its arc. Narrowing raises the revisit
// rate over the arc that matters, which is what a navigator wants when moving
// quickly.
func (s *serialSource) setSector(degrees float64) error {
	half := int16(math.Round(degrees / 2))
	if err := s.writeFrame(msgSectorLeft, i16(half)); err != nil {
		return err
	}
	return s.writeFrame(msgSectorRight, i16(-half))
}

func (s *serialSource) sweeps(ctx context.Context) (<-chan sweep, error) {
	// Ask for streaming distance data rather than polling: a sweep arrives when
	// the sensor has one, and polling a scanning sensor returns whatever point
	// it happens to be looking at.
	if err := s.writeFrame(msgStreamSelect, u32(msgDistanceData)); err != nil {
		return nil, fmt.Errorf("request distance stream: %w", err)
	}
	if s.cfg.SweepHz > 0 {
		if err := s.writeFrame(msgScanSpeed, u32(uint32(s.cfg.SweepHz))); err != nil {
			log.Printf("guetteur: could not set scan speed: %v", err)
		}
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
		started   = time.Now()
	)

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
		if id != msgDistanceData {
			continue
		}
		angle, distance, ok := parseDistanceData(payload, s.cfg.MaxRange)
		if !ok {
			continue
		}

		if !math.IsNaN(lastAngle) {
			nowRising := angle > lastAngle
			if len(cur.angles) > 2 && nowRising != rising {
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
			}
			rising = nowRising
		}
		lastAngle = angle

		cur.angles = append(cur.angles, angle)
		cur.distances = append(cur.distances, math.Max(distance, 0))
		cur.valid = append(cur.valid, distance > 0)
	}
}

// parseDistanceData pulls one (angle, distance) pair out of a distance message.
//
// UNCONFIRMED layout: first return in millimetres as int16, then the scan angle
// in hundredths of a degree as int16.
//
// The no-return rule is the important line here, and it is stated rather than
// implied: a negative distance, a zero distance, or anything beyond the
// configured maximum range is NOT a reading. It must never reach a consumer as
// a distance, because "nothing came back" and "clear to fifty metres" are the
// same bytes on this wire and opposite facts on a moving vehicle.
func parseDistanceData(payload []byte, maxRange float64) (angleDeg, distanceM float64, ok bool) {
	if len(payload) < 4 {
		return 0, 0, false
	}
	mm := int16(binary.LittleEndian.Uint16(payload[0:2]))
	centideg := int16(binary.LittleEndian.Uint16(payload[2:4]))

	angleDeg = float64(centideg) / 100
	if mm <= 0 {
		return angleDeg, 0, true // a point, but not a return
	}
	distanceM = float64(mm) / 1000
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
			// Fifty frames, none of them valid: this is a framing error and not
			// a noisy cable. Say so once, plainly, rather than letting it look
			// like an idle sensor.
			log.Printf("guetteur: %d frames read and none passed CRC — the framing in lwnx.go is wrong for this sensor, check it against the product guide", s.framesBad)
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

// crc16 is CCITT-FALSE, which is what LWNX uses.
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

func i16(v int16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(v))
	return b
}
