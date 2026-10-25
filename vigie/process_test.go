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
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// The signs, which is where a mounting goes wrong without anyone noticing.
func TestTheMountRotatesAsISO8855Says(t *testing.T) {
	// Turned 90° to the left, the lidar's forward is the vehicle's left.
	v := Mount{YawDegrees: 90}.rotation().apply([3]float64{1, 0, 0})
	if !near(v[0], 0, 1e-9) || !near(v[1], 1, 1e-9) {
		t.Errorf("yaw 90°: %v, want (0, 1, 0)", v)
	}
	// Pitched nose down, the lidar's forward points below the horizon.
	v = Mount{PitchDegrees: 10}.rotation().apply([3]float64{1, 0, 0})
	if v[2] >= 0 {
		t.Errorf("pitch 10° nose down: %v, want z below zero", v)
	}
	// Rolled right side down, the lidar's left points up.
	v = Mount{RollDegrees: 10}.rotation().apply([3]float64{0, 1, 0})
	if v[2] <= 0 {
		t.Errorf("roll 10° right side down: %v, want z above zero", v)
	}
}

func TestTiltFromGravity(t *testing.T) {
	r := math.Pi / 180
	pitch, roll := tilt([3]float64{-math.Sin(10 * r), 0, math.Cos(10 * r)})
	if !near(pitch, 10, 1e-9) || !near(roll, 0, 1e-9) {
		t.Errorf("nose down 10°: pitch %.3f roll %.3f", pitch, roll)
	}
	pitch, roll = tilt([3]float64{0, math.Sin(5 * r), math.Cos(5 * r)})
	if !near(pitch, 0, 1e-9) || !near(roll, 5, 1e-9) {
		t.Errorf("right side down 5°: pitch %.3f roll %.3f", pitch, roll)
	}
	// And the mount and the tilt agree: a lidar pitched nose down on a level
	// vehicle reads the vehicle as level once its reading is turned by the mount.
	m := Mount{PitchDegrees: 10}.rotation()
	lidarSees := [3]float64{-math.Sin(10 * r), 0, math.Cos(10 * r)} // gravity's reaction, in the tilted lidar
	if pitch, _ := tilt(m.apply(lidarSees)); !near(pitch, 0, 1e-9) {
		t.Errorf("a pitched mount on a level vehicle reads %.3f°", pitch)
	}
}

func TestTheSliceKeepsTheNearestInTheBand(t *testing.T) {
	now := time.Now()
	cfg := SliceConfig{MinHeightMetres: 0.3, MaxHeightMetres: 1.5, BinDegrees: 1, MinRangeMetres: 0.2, MaxRangeMetres: 40}
	pts := []timedPoint{
		{x: 5, y: 0, z: 1},      // ahead, in the band
		{x: 3, y: 0, z: 1},      // nearer, same bin: kept
		{x: 2, y: 0, z: 0.1},    // nearer still, but on the floor: ignored
		{x: 0, y: 2, z: 1},      // to the left
		{x: 0.1, y: 0.05, z: 1}, // inside the minimum range
		{x: -1, y: 0, z: 1},     // behind, but inside the body box
	}
	sw := slice(pts, cfg, Box{MinX: -1.5, MaxX: -0.5, MinY: -0.5, MaxY: 0.5}, now, now)
	if len(sw.angles) != 360 {
		t.Fatalf("%d bins", len(sw.angles))
	}
	at := func(deg float64) (float64, bool) {
		for i, a := range sw.angles {
			if near(a, deg, 0.5) {
				return sw.distances[i], sw.valid[i]
			}
		}
		return 0, false
	}
	if d, ok := at(0.5); !ok || !near(d, 3, 1e-9) {
		t.Errorf("ahead: %.2f %v, want 3 m", d, ok)
	}
	if d, ok := at(90.5); !ok || !near(d, 2, 1e-9) { // 90° exactly is the bin [90, 91)
		t.Errorf("left: %.2f %v, want 2 m", d, ok)
	}
	if _, ok := at(179.5); ok {
		t.Error("the vehicle's own body was taken for an obstacle")
	}
}

func TestClearanceSeesWhatTheLaserPlaneWouldMiss(t *testing.T) {
	cfg := ClearanceConfig{HalfWidthMetres: 0.45, MinHeightMetres: 0.1, MaxHeightMetres: 1.6}
	pts := []timedPoint{
		{x: 6, y: 0, z: 0.5},  // ahead
		{x: 4, y: 0, z: 1.55}, // a hanging cable, above any single scan plane at 0.5 m
		{x: 2, y: 1.0, z: 1},  // beside the corridor
		{x: 1, y: 0, z: 0.02}, // the floor
	}
	if d, ok := clearanceAhead(pts, cfg, Box{}); !ok || d != 4 {
		t.Errorf("%.2f %v, want the cable at 4 m", d, ok)
	}
	if _, ok := clearanceAhead(pts[2:], cfg, Box{}); ok {
		t.Error("a corridor nothing came back from was reported as a distance")
	}
}

func TestTheHeadingIntegratesTheYawRate(t *testing.T) {
	m := &motion{window: 100 * time.Millisecond}
	start := time.Now()
	for i := 0; i <= 200; i++ { // one second at 200 Hz, 0.1 rad/s to the left
		m.add(timedIMU{at: start.Add(time.Duration(i) * 5 * time.Millisecond), stamp: uint64(i) * 5e6, gyro: [3]float64{0, 0, 0.1}, acc: [3]float64{0, 0, 1}})
	}
	gyro, _, heading, _, ok := m.averages()
	if !ok || !near(gyro[2], 0.1, 1e-9) || !near(heading, 0.1*180/math.Pi, 1e-6) {
		t.Errorf("yaw rate %.4f, heading %.4f°, want 0.1 rad/s and %.4f°", gyro[2], heading, 0.1*180/math.Pi)
	}
	// A clock that jumps is a lost stream, not a turn.
	m.add(timedIMU{at: start.Add(2 * time.Second), stamp: 5e9, gyro: [3]float64{0, 0, 0.1}})
	if _, _, h, _, _ := m.averages(); !near(h, heading, 1e-9) {
		t.Errorf("a four-second gap added %.2f° of heading", h-heading)
	}
}

// The whole chain on the simulator: real packets, decoded, mounted, sliced.
// The tunnel is 3 m wide, so a scan from its middle sees the walls 1.5 m to
// either side.
func TestTheSimulatedTunnelEndToEnd(t *testing.T) {
	cfg := defaultConfig()
	tr := &Traits{
		cfg: cfg,
		rot: cfg.Mount.rotation(),
		pts: &cloud{window: time.Duration(cfg.Slice.AccumulateMs) * time.Millisecond},
		imu: &motion{window: time.Duration(cfg.IMUAverageMs) * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	ch := make(chan datagram, 4096)
	go simulatedSource{height: cfg.Mount.ZMetres, speed: 0}.run(ctx, ch)
	go tr.consume(ctx, ch)
	<-ctx.Done()

	pts, ret, emp, first, last, _ := tr.window()
	if ret == 0 {
		t.Fatal("no points arrived")
	}
	if tr.stat.crcFailed+tr.stat.refused > 0 {
		t.Errorf("%d packets failed the CRC and %d were refused", tr.stat.crcFailed, tr.stat.refused)
	}
	sw := slice(pts, cfg.Slice, cfg.Body, first, last)
	for _, side := range []float64{90, -90} {
		var got []float64
		for i, a := range sw.angles {
			if near(a, side, 3) && sw.valid[i] {
				got = append(got, sw.distances[i])
			}
		}
		if len(got) == 0 {
			t.Errorf("nothing seen at %.0f°", side)
			continue
		}
		for _, d := range got {
			if !near(d, 1.5, 0.1) {
				t.Errorf("the wall at %.0f° is %.2f m away, want 1.5", side, d)
				break
			}
		}
	}
	if q := 100 * float64(ret) / float64(ret+emp); q < 50 || q > 100 {
		t.Errorf("quality %.0f%%", q)
	}
	if _, _, _, _, ok := tr.imu.averages(); !ok {
		t.Error("no IMU samples arrived")
	}
}

func TestARecordingReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.vigie")
	rec, err := newRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 5; i++ {
		pkt := encodePacket(dataCartesian32, uint16(i), uint64(i), []point{{x: float64(i + 1)}}, nil)
		if err := rec.write(datagram{port: portHostPoints, at: start.Add(time.Duration(i) * time.Millisecond), data: pkt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.close(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan datagram, 10)
	if err := (replaySource{path: path}).run(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	n := 0
	for d := range ch {
		p, err := decodePacket(d.data, true)
		if err != nil || d.port != portHostPoints || len(p.points) != 1 || p.points[0].x != float64(n+1) {
			t.Errorf("datagram %d: %+v %v", n, p, err)
		}
		n++
	}
	if n != 5 {
		t.Errorf("%d datagrams replayed, want 5", n)
	}
}
