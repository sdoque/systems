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
	"math"
	"sync"
	"time"
)

// The vehicle frame is ISO 8855, as everywhere else on the Artitrax: x forward,
// y to the left, z up, its origin on the ground below the reference point the
// mount is measured from. Angles grow to the left; pitch is positive nose down
// and roll positive with the right side down, which is what right-handed
// rotations about forward and left give.

// mount places the lidar on the vehicle: where it is, and how it is turned. It
// is assumed that the Mid-360's own frame is x forward, y left, z up, as Livox
// draws it; if the vehicle shows otherwise, the yaw, pitch and roll correct it.
type Mount struct {
	XMetres      float64 `json:"xMetres"`
	YMetres      float64 `json:"yMetres"`
	ZMetres      float64 `json:"zMetres"` // the lidar's height above the ground
	RollDegrees  float64 `json:"rollDegrees"`
	PitchDegrees float64 `json:"pitchDegrees"`
	YawDegrees   float64 `json:"yawDegrees"`
}

// rotation is the lidar-to-vehicle rotation, Rz(yaw)·Ry(pitch)·Rx(roll).
type rotation [3][3]float64

func (m Mount) rotation() rotation {
	r, p, y := m.RollDegrees*math.Pi/180, m.PitchDegrees*math.Pi/180, m.YawDegrees*math.Pi/180
	cr, sr, cp, sp, cy, sy := math.Cos(r), math.Sin(r), math.Cos(p), math.Sin(p), math.Cos(y), math.Sin(y)
	return rotation{
		{cy * cp, cy*sp*sr - sy*cr, cy*sp*cr + sy*sr},
		{sy * cp, sy*sp*sr + cy*cr, sy*sp*cr - cy*sr},
		{-sp, cp * sr, cp * cr},
	}
}

func (r rotation) apply(v [3]float64) [3]float64 {
	return [3]float64{
		r[0][0]*v[0] + r[0][1]*v[1] + r[0][2]*v[2],
		r[1][0]*v[0] + r[1][1]*v[1] + r[1][2]*v[2],
		r[2][0]*v[0] + r[2][1]*v[1] + r[2][2]*v[2],
	}
}

// SliceConfig chooses the points that make the horizontal scan.
type SliceConfig struct {
	// The band, in metres above the ground, whose points make the scan. A
	// band rather than a plane: the Mid-360 does not sweep a plane, and a
	// wall is a wall at any height a vehicle could hit.
	MinHeightMetres float64 `json:"minHeightMetres"`
	MaxHeightMetres float64 `json:"maxHeightMetres"`
	BinDegrees      float64 `json:"binDegrees"`
	MinRangeMetres  float64 `json:"minRangeMetres"`
	MaxRangeMetres  float64 `json:"maxRangeMetres"`
	// AccumulateMs is how long points are gathered for one scan. The Mid-360
	// covers its field of view gradually, so a tenth of a second is sparse.
	AccumulateMs int `json:"accumulateMs"`
}

// Box is a region of the vehicle frame, in metres. A zero box is no box.
type Box struct {
	MinX float64 `json:"minX"`
	MaxX float64 `json:"maxX"`
	MinY float64 `json:"minY"`
	MaxY float64 `json:"maxY"`
}

func (b Box) contains(x, y float64) bool {
	if b.MinX == 0 && b.MaxX == 0 && b.MinY == 0 && b.MaxY == 0 {
		return false
	}
	return x >= b.MinX && x <= b.MaxX && y >= b.MinY && y <= b.MaxY
}

// ClearanceConfig is the corridor the vehicle will drive through.
type ClearanceConfig struct {
	HalfWidthMetres float64 `json:"halfWidthMetres"`
	MinHeightMetres float64 `json:"minHeightMetres"` // above the ground: lower is the floor
	MaxHeightMetres float64 `json:"maxHeightMetres"` // the vehicle's height and a margin
}

// sweep is one horizontal scan, in the vehicle frame: angle zero straight
// ahead, angles growing to the left, distances from the reference point.
type sweep struct {
	angles    []float64
	distances []float64
	valid     []bool
	taken     time.Time
	duration  time.Duration
}

type timedPoint struct {
	at      time.Time
	x, y, z float64 // vehicle frame
}

// cloud keeps the last window of points in the vehicle frame, and what the
// scan, the clearance and the quality are made from.
type cloud struct {
	mu      sync.Mutex
	points  []timedPoint
	returns []int // per packet: how many came back
	empties []int // and how many did not
	stamps  []time.Time
	window  time.Duration
}

func (c *cloud) add(at time.Time, pts []timedPoint, empty int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.points = append(c.points, pts...)
	c.returns = append(c.returns, len(pts))
	c.empties = append(c.empties, empty)
	c.stamps = append(c.stamps, at)
	c.trim(at)
}

// trim drops what is older than the window.
func (c *cloud) trim(now time.Time) {
	cut := now.Add(-c.window)
	i := 0
	for i < len(c.points) && c.points[i].at.Before(cut) {
		i++
	}
	c.points = c.points[i:]
	j := 0
	for j < len(c.stamps) && c.stamps[j].Before(cut) {
		j++
	}
	c.returns, c.empties, c.stamps = c.returns[j:], c.empties[j:], c.stamps[j:]
}

func (c *cloud) snapshot() ([]timedPoint, int, int, time.Time, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pts := append([]timedPoint(nil), c.points...)
	ret, emp := 0, 0
	for i := range c.returns {
		ret += c.returns[i]
		emp += c.empties[i]
	}
	var first, last time.Time
	if len(c.stamps) > 0 {
		first, last = c.stamps[0], c.stamps[len(c.stamps)-1]
	}
	return pts, ret, emp, first, last
}

// slice makes a horizontal scan of the window: in each bin of direction, the
// nearest point within the height band, outside the vehicle's own body.
func slice(pts []timedPoint, cfg SliceConfig, body Box, first, last time.Time) sweep {
	n := int(math.Round(360 / cfg.BinDegrees))
	sw := sweep{
		angles:    make([]float64, n),
		distances: make([]float64, n),
		valid:     make([]bool, n),
		taken:     last,
		duration:  last.Sub(first),
	}
	for i := range sw.angles {
		sw.angles[i] = -180 + (float64(i)+0.5)*cfg.BinDegrees
	}
	for _, p := range pts {
		if p.z < cfg.MinHeightMetres || p.z > cfg.MaxHeightMetres || body.contains(p.x, p.y) {
			continue
		}
		d := math.Hypot(p.x, p.y)
		if d < cfg.MinRangeMetres || d > cfg.MaxRangeMetres {
			continue
		}
		a := math.Atan2(p.y, p.x) * 180 / math.Pi
		i := int(math.Floor((a + 180) / cfg.BinDegrees))
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		if !sw.valid[i] || d < sw.distances[i] {
			sw.distances[i], sw.valid[i] = d, true
		}
	}
	return sw
}

// clearanceAhead is the nearest return in the corridor ahead, measured forward
// from the reference point. ok is false when nothing in the corridor came
// back: a lidar that saw nothing there cannot say whether the way is clear or
// it was blind, and a consumer must not be handed a distance for either.
func clearanceAhead(pts []timedPoint, cfg ClearanceConfig, body Box) (float64, bool) {
	best := math.Inf(1)
	for _, p := range pts {
		if p.x <= 0 || math.Abs(p.y) > cfg.HalfWidthMetres || p.z < cfg.MinHeightMetres || p.z > cfg.MaxHeightMetres {
			continue
		}
		if body.contains(p.x, p.y) {
			continue
		}
		if p.x < best {
			best = p.x
		}
	}
	if math.IsInf(best, 1) {
		return 0, false
	}
	return best, true
}

//-------------------------------------The IMU

// motion is what the IMU says, in the vehicle frame.
type motion struct {
	mu      sync.Mutex
	samples []timedIMU
	window  time.Duration

	heading   float64 // degrees, integrated since start
	lastStamp uint64  // the lidar's time of the previous sample, ns
	got       bool
}

type timedIMU struct {
	at    time.Time  // when it arrived
	stamp uint64     // when the lidar took it, ns on its own clock
	gyro  [3]float64 // rad/s
	acc   [3]float64 // g
}

func (m *motion) add(s timedIMU) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Integrate the yaw rate into a heading, sample by sample, on the lidar's
	// own clock: arrival times carry the network's jitter, its stamps do not.
	// A gap of more than a quarter second is a lost stream, not a quarter
	// second of turning, and a clock that went backwards is a restart.
	if m.got && s.stamp > m.lastStamp {
		if dt := float64(s.stamp-m.lastStamp) / 1e9; dt < 0.25 {
			m.heading += s.gyro[2] * dt * 180 / math.Pi
		}
	}
	m.lastStamp, m.got = s.stamp, true
	m.samples = append(m.samples, s)
	cut := s.at.Add(-m.window)
	i := 0
	for i < len(m.samples) && m.samples[i].at.Before(cut) {
		i++
	}
	m.samples = m.samples[i:]
}

// averages are the mean gyro and accelerometer over the window, the newest
// sample's time, and whether there was any.
func (m *motion) averages() (gyro, acc [3]float64, heading float64, at time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) == 0 {
		return gyro, acc, m.heading, at, false
	}
	for _, s := range m.samples {
		for k := 0; k < 3; k++ {
			gyro[k] += s.gyro[k]
			acc[k] += s.acc[k]
		}
	}
	n := float64(len(m.samples))
	for k := 0; k < 3; k++ {
		gyro[k] /= n
		acc[k] /= n
	}
	return gyro, acc, m.heading, m.samples[len(m.samples)-1].at, true
}

// tilt is pitch and roll from the accelerometer, in degrees. At rest it reads
// the reaction to gravity, one g upward; tilted nose down by θ, the upward
// vector seen from the vehicle leans back, so its x component is −sin θ.
func tilt(acc [3]float64) (pitch, roll float64) {
	pitch = math.Atan2(-acc[0], math.Hypot(acc[1], acc[2])) * 180 / math.Pi
	roll = math.Atan2(acc[1], acc[2]) * 180 / math.Pi
	return pitch, roll
}
