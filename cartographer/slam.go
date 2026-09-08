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

// The mapping and localization themselves: an occupancy grid in log-odds, and
// a correlative scan matcher that finds where a sweep must have been taken from
// for it to line up with the map so far.
//
// This is a SLAM *front end* and it is honest about being one. It has no pose
// graph and no loop closure, so a long loop through a building will not snap
// shut when it returns to where it started; the drift accumulated on the way
// round simply stays. For a hallway driven up and back — which is the
// experiment the students are being given — that is the right amount of
// machinery. Loop closure is a second system's worth of work and should be
// added when there is a loop to close, not before.

import (
	"fmt"
	"math"
	"os"

	"github.com/sdoque/mbaigo/forms"
)

// pose is where the sensor was, in the map frame. Angles in radians here;
// degrees only ever appear at the edges, in forms and configuration.
type pose struct {
	x, y, theta float64
}

// The log-odds a single observation is worth, and the clamp that stops any one
// cell from becoming so certain that later evidence cannot move it. A corridor
// that is remapped after a door opens has to be able to change its mind.
const (
	logFree  float32 = -0.4
	logOcc   float32 = 0.85
	logClamp float32 = 5.0
)

type grid struct {
	width, height int
	res           float64 // metres per cell
	// world coordinates of the centre of cell (0,0)
	originX, originY float64
	cells            []float32

	// field is the likelihood field the scan matcher scores against: the
	// occupancy evidence spread over its neighbours.
	//
	// Scoring against cells directly does not work, and the way it fails is
	// instructive. One sweep marks only the cells its own beams happened to
	// land on, so a wall enters the map as a dotted line. A later sweep then
	// scores highest where its endpoints fall back onto those same dots —
	// which is the pose the vehicle has already left. The matcher confidently
	// reports no motion, and the map stays beautiful and wrong.
	//
	// Spreading each occupied cell over a small neighbourhood makes a wall
	// continuous, so the along-wall direction stops carrying false evidence and
	// the discrimination comes from where it should: the ends of the corridor
	// and features like doorways.
	field []float32
	dirty bool

	// matchRange is how far out the scan matcher trusts a return, in metres;
	// 0 means all of them.
	//
	// Far returns are worth less than they look. A sweep samples a wall at
	// intervals that grow with range, so distant wall is recorded as scattered
	// dots rather than a surface, and the matcher can score highest by landing
	// its endpoints back on those dots — fitting the sampling pattern instead
	// of the geometry. Distant points are also where a small heading error
	// throws an endpoint furthest. Ignoring them for matching costs nothing:
	// they are still integrated into the map.
	matchRange float64
}

func newGrid(widthM, heightM, res float64) *grid {
	w := int(math.Ceil(widthM / res))
	h := int(math.Ceil(heightM / res))
	return &grid{
		width: w, height: h, res: res,
		// The vehicle starts at the middle of the grid, because it may drive
		// in any direction and a map that only grows one way wastes three
		// quarters of itself.
		originX: -widthM / 2,
		originY: -heightM / 2,
		cells:   make([]float32, w*h),
		field:   make([]float32, w*h),
	}
}

func (g *grid) cellOf(x, y float64) (int, int, bool) {
	cx := int(math.Floor((x - g.originX) / g.res))
	cy := int(math.Floor((y - g.originY) / g.res))
	if cx < 0 || cy < 0 || cx >= g.width || cy >= g.height {
		return 0, 0, false
	}
	return cx, cy, true
}

func (g *grid) at(cx, cy int) float32 {
	if cx < 0 || cy < 0 || cx >= g.width || cy >= g.height {
		return 0
	}
	return g.cells[cy*g.width+cx]
}

func (g *grid) add(cx, cy int, v float32) {
	if cx < 0 || cy < 0 || cx >= g.width || cy >= g.height {
		return
	}
	i := cy*g.width + cx
	n := g.cells[i] + v
	if n > logClamp {
		n = logClamp
	}
	if n < -logClamp {
		n = -logClamp
	}
	g.cells[i] = n
}

// integrate folds one sweep into the map from a known pose.
//
// Invalid points are skipped entirely rather than treated as free space out to
// maximum range. That is the conservative choice and it is deliberate: this
// system cannot tell "nothing within range" from "a surface that returned
// nothing", and a black door or a puddle marked as open floor is precisely the
// error that would drive a vehicle into it. An unknown cell is an honest cell.
func (g *grid) integrate(p pose, angles, distances []float64, valid []bool) {
	sx, sy, inside := g.cellOf(p.x, p.y)
	if !inside {
		return
	}
	prevX, prevY, prevOK := 0, 0, false
	for i := range angles {
		if i >= len(valid) || i >= len(distances) || !valid[i] {
			prevOK = false
			continue
		}
		wx, wy := project(p, angles[i], distances[i])
		ex, ey, ok := g.cellOf(wx, wy)
		if !ok {
			prevOK = false
			continue
		}
		for _, c := range line(sx, sy, ex, ey) {
			g.add(c[0], c[1], logFree)
		}
		g.add(ex, ey, logOcc)

		// Join this return to the one before it when the two are close enough
		// to be the same surface. A sweep samples a wall at intervals that grow
		// with range, so recording only the endpoints leaves a wall as a row of
		// dots with gaps between them — and a later sweep then scores best by
		// dropping its endpoints back into those same dots, which is the pose
		// the vehicle has already left. Drawing the surface between two
		// adjacent returns is what makes a wall a wall.
		//
		// The distance test is what stops it inventing one: at a doorway or a
		// corner the range jumps, the two returns are far apart, and nothing is
		// drawn across the gap.
		if prevOK {
			if dx, dy := float64(ex-prevX)*g.res, float64(ey-prevY)*g.res; math.Hypot(dx, dy) <= surfaceGap {
				for _, c := range line(prevX, prevY, ex, ey) {
					g.add(c[0], c[1], logOcc)
				}
			}
		}
		prevX, prevY, prevOK = ex, ey, true
	}
	g.dirty = true
}

// surfaceGap is how far apart two adjacent returns may be and still be treated
// as the same surface, in metres.
const surfaceGap = 0.30

// project turns one (bearing, range) pair into a world point, given the pose
// the sweep was taken from.
func project(p pose, angleDeg, distance float64) (float64, float64) {
	a := p.theta + angleDeg*math.Pi/180
	return p.x + distance*math.Cos(a), p.y + distance*math.Sin(a)
}

// fieldSigma is how far, in metres, a wall's influence reaches. A tenth of a
// metre is about the accuracy worth chasing from a moving vehicle.
const fieldSigma = 0.10

// fieldCutoff is where the Gaussian is small enough to call zero, in sigmas.
const fieldCutoff = 3

// rebuildField computes the likelihood field: for every cell, how close the
// nearest occupied cell is, passed through a Gaussian.
//
// It is a Euclidean distance transform and not a blur, and the difference is
// the whole reason the matcher works. Spreading each occupied cell over a fixed
// kernel leaves a wall as a row of bumps, because a sweep samples a wall at
// intervals that grow with range. The matcher then finds its best score where
// the new sweep's endpoints land back on the old sweep's bumps — which is the
// pose the vehicle has already left. It fits the sampling pattern rather than
// the geometry, and reports that the vehicle has not moved.
//
// A distance transform makes a wall uniformly attractive along its length. The
// along-wall direction then carries no false evidence, and the pose is
// constrained by the things that genuinely constrain it: the ends of a
// corridor, a doorway, a corner.
func (g *grid) rebuildField() {
	const big = float32(1e9)
	d := make([]float32, len(g.cells))
	for i, v := range g.cells {
		if v > 0 {
			d[i] = 0
		} else {
			d[i] = big
		}
	}

	// Two-pass chamfer. The diagonal step is sqrt(2), which is what makes this
	// approximate Euclidean distance rather than city-block distance.
	const diag = float32(1.41421356)
	at := func(x, y int) float32 {
		if x < 0 || y < 0 || x >= g.width || y >= g.height {
			return big
		}
		return d[y*g.width+x]
	}
	relax := func(x, y int, candidates ...float32) {
		i := y*g.width + x
		for _, c := range candidates {
			if c < d[i] {
				d[i] = c
			}
		}
	}
	for y := 0; y < g.height; y++ {
		for x := 0; x < g.width; x++ {
			relax(x, y, at(x-1, y)+1, at(x, y-1)+1, at(x-1, y-1)+diag, at(x+1, y-1)+diag)
		}
	}
	for y := g.height - 1; y >= 0; y-- {
		for x := g.width - 1; x >= 0; x-- {
			relax(x, y, at(x+1, y)+1, at(x, y+1)+1, at(x+1, y+1)+diag, at(x-1, y+1)+diag)
		}
	}

	// Only near an obstacle is the Gaussian worth computing; beyond the cutoff
	// it is indistinguishable from zero and this is a million-cell loop.
	cutoffCells := float32(fieldCutoff * fieldSigma / g.res)
	for i, dist := range d {
		if dist > cutoffCells {
			g.field[i] = 0
			continue
		}
		m := float64(dist) * g.res
		g.field[i] = float32(math.Exp(-m * m / (2 * fieldSigma * fieldSigma)))
	}
	g.dirty = false
}

// score is how well a sweep taken from this pose agrees with the map: the
// likelihood-field evidence under its endpoints. A sweep in the right place
// puts its endpoints on or beside cells already believed to be walls.
func (g *grid) score(p pose, angles, distances []float64, valid []bool, stride int) float64 {
	var total float64
	for i := 0; i < len(angles); i += stride {
		if i >= len(valid) || i >= len(distances) || !valid[i] {
			continue
		}
		if g.matchRange > 0 && distances[i] > g.matchRange {
			continue
		}
		wx, wy := project(p, angles[i], distances[i])
		cx, cy, ok := g.cellOf(wx, wy)
		if !ok {
			continue
		}
		total += float64(g.field[cy*g.width+cx])
	}
	return total
}

// matchResult is where a sweep was taken from, and whether the map was able to
// say so.
//
// sharp is the part that matters. A scan matcher always returns a best pose,
// and in a place that cannot constrain it — a straight corridor, an empty
// hall — that best pose is whichever one the sampling pattern happened to
// favour. Returning it without comment would be this project's recurring
// failure in a new costume: a thing that cannot see, reporting that all is
// well. So the peak is probed, and a flat one is declared flat.
type matchResult struct {
	at    pose
	score float64
	sharp bool
}

// probeDistance and probeAngle are how far the peak is nudged to see whether it
// is a peak at all; sharpRatio is how much the score must fall for it to count
// as one.
const (
	probeDistance = 0.10
	probeAngle    = 3 * math.Pi / 180
	sharpRatio    = 0.95
)

// match searches for the pose that best explains this sweep, starting from a
// prior — the previous pose advanced by whatever motion is believed to have
// happened since.
//
// Two passes: a coarse one over a wide window, then a fine one around whatever
// the coarse pass liked. Exhaustive search at fine resolution over the same
// window would be some hundreds of times the work for the same answer.
func (g *grid) match(prior pose, angles, distances []float64, valid []bool, cfg SearchConfig) matchResult {
	if g.dirty {
		g.rebuildField()
	}

	search := func(centre pose, linStep, linRange, angStep, angRange float64, stride int) (pose, float64) {
		local, localScore := centre, math.Inf(-1)
		for dx := -linRange; dx <= linRange+1e-9; dx += linStep {
			for dy := -linRange; dy <= linRange+1e-9; dy += linStep {
				for dt := -angRange; dt <= angRange+1e-9; dt += angStep {
					c := pose{centre.x + dx, centre.y + dy, centre.theta + dt}
					s := g.score(c, angles, distances, valid, stride)
					if s > localScore {
						local, localScore = c, s
					}
				}
			}
		}
		return local, localScore
	}

	coarse, _ := search(prior, cfg.CoarseStep, cfg.SearchRange, cfg.CoarseAngle, cfg.AngleRange, cfg.CoarseStride)
	best, bestScore := search(coarse, cfg.FineStep, cfg.CoarseStep, cfg.FineAngle, cfg.CoarseAngle, 1)

	return matchResult{at: best, score: bestScore, sharp: g.isSharp(best, bestScore, angles, distances, valid)}
}

// isSharp nudges the winning pose in six directions and asks whether the score
// actually falls. If the map is happy to put the sweep a hand's breadth away in
// some direction, then it does not know where the sweep was taken from in that
// direction, whatever the best score says.
//
// This is the aperture problem, and it is not a defect to be tuned away: two
// parallel walls genuinely say nothing about how far along them you are. The
// answer is odometry, which is why this system consumes a pose service when one
// exists. What it must never do is invent the missing degree of freedom.
func (g *grid) isSharp(best pose, bestScore float64, angles, distances []float64, valid []bool) bool {
	if bestScore <= 0 {
		return false
	}
	probes := []pose{
		{best.x + probeDistance, best.y, best.theta},
		{best.x - probeDistance, best.y, best.theta},
		{best.x, best.y + probeDistance, best.theta},
		{best.x, best.y - probeDistance, best.theta},
		{best.x, best.y, best.theta + probeAngle},
		{best.x, best.y, best.theta - probeAngle},
	}
	for _, p := range probes {
		if g.score(p, angles, distances, valid, 1) >= sharpRatio*bestScore {
			return false
		}
	}
	return true
}

// SearchConfig is the scan matcher's window, exposed because the right values
// depend on how fast the vehicle moves between sweeps.
type SearchConfig struct {
	SearchRange  float64 `json:"searchRangeMetres"`
	CoarseStep   float64 `json:"coarseStepMetres"`
	FineStep     float64 `json:"fineStepMetres"`
	AngleRange   float64 `json:"angleRangeRadians"`
	CoarseAngle  float64 `json:"coarseAngleRadians"`
	FineAngle    float64 `json:"fineAngleRadians"`
	CoarseStride int     `json:"coarseStride"`
}

func defaultSearch() SearchConfig {
	return SearchConfig{
		SearchRange:  0.30,
		CoarseStep:   0.05,
		FineStep:     0.0125,
		AngleRange:   12 * math.Pi / 180,
		CoarseAngle:  2 * math.Pi / 180,
		FineAngle:    0.5 * math.Pi / 180,
		CoarseStride: 3,
	}
}

// line is Bresenham between two cells, used to mark the free space a beam
// passed through on its way to whatever stopped it.
func line(x0, y0, x1, y1 int) [][2]int {
	var out [][2]int
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		if x0 == x1 && y0 == y1 {
			return out // the endpoint is the obstacle, not free space
		}
		out = append(out, [2]int{x0, y0})
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

//-------------------------------------Publishing the map

// form renders the grid as the wire representation. Log-odds become the three
// values a consumer has to tell apart, and a cell nothing has ever touched
// stays Unknown rather than becoming free.
func (g *grid) form() forms.MapA_v1a {
	m := forms.MapA_v1a{}
	m.NewForm()
	m.Width, m.Height = g.width, g.height
	m.Resolution = g.res
	m.OriginX, m.OriginY = g.originX, g.originY
	m.Frame = "map"
	m.DistanceUnit = "<http://qudt.org/vocab/unit/M>"
	m.Cells = make([]byte, len(g.cells))
	for i, v := range g.cells {
		switch {
		case v == 0:
			m.Cells[i] = forms.MapUnknown
		case v > 0:
			m.Cells[i] = forms.MapOccupied
		default:
			m.Cells[i] = forms.MapFree
		}
	}
	return m
}

// writePGM dumps the map as a grey image, which is the form a person can
// actually look at. The values follow the convention the robotics tools use:
// black is occupied, white is free, mid-grey is unseen.
func (g *grid) writePGM(path string) error {
	buf := make([]byte, 0, g.width*g.height+64)
	buf = append(buf, []byte(fmt.Sprintf("P5\n%d %d\n255\n", g.width, g.height))...)
	// PGM runs top row first; the grid's row 0 is the bottom, so it is written
	// in reverse, otherwise the hallway comes out mirrored.
	for y := g.height - 1; y >= 0; y-- {
		for x := 0; x < g.width; x++ {
			v := g.cells[y*g.width+x]
			switch {
			case v == 0:
				buf = append(buf, 205)
			case v > 0:
				buf = append(buf, 0)
			default:
				buf = append(buf, 254)
			}
		}
	}
	return os.WriteFile(path, buf, 0o644)
}
