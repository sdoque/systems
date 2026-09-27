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

import (
	"container/heap"
	"fmt"
	"math"

	"github.com/sdoque/mbaigo/forms"
)

// Planning a forward route through an occupancy map, no tighter than the
// vehicle can turn.
//
// The search is a hybrid A*: states are continuous positions and headings, and
// the vehicle moves between them only along arcs it can actually drive — left
// at its tightest, half that, straight, and the same to the right. So a route
// that comes out of it can be followed, which a route through grid cells cannot
// promise: a grid path turns a corner on the spot, and an articulated loader
// turns on a radius of two meters.
//
// It only drives forward. The vehicle has nothing looking backwards, and the
// corridors it works in loop, so a destination behind it is reached by going on
// round rather than by reversing. Where there is no loop, there is no route,
// and the planner says so rather than invent a maneuver.
//
// The search is guided by the true distance to the goal around the obstacles,
// computed once per plan by a flood outward from the goal. That keeps a hybrid
// A* in a corridor from exploring every dead end before finding the way.

// Vehicle is what the planner needs to know about the machine.
type Vehicle struct {
	// MaxCurvature is the tightest the vehicle turns, in 1/m. The loader
	// publishes it; the planner uses a share of it, so that a follower has
	// room to correct.
	MaxCurvature float64
	// Clearance is how far the vehicle's middle must keep from anything: half
	// its width and a margin, in meters.
	Clearance float64
}

// planGrid is the map as the planner sees it: coarser than the cartographer's,
// and with every cell marked for whether the vehicle may stand in it.
type planGrid struct {
	w, h   int
	res    float64
	ox, oy float64 // world coordinate of the center of cell (0,0)

	blocked []bool    // occupied, or never seen
	unknown []bool    // never seen — the reason, when a goal lands here
	clear   []float64 // meters to the nearest blocked cell
}

// newPlanGrid coarsens a map to the planning resolution. A planning cell is
// blocked if any map cell inside it is: coarsening must never turn a wall into
// a gap.
func newPlanGrid(m *forms.MapA_v1a, res float64) (*planGrid, error) {
	if err := m.Check(); err != nil {
		return nil, err
	}
	if m.Resolution <= 0 {
		return nil, fmt.Errorf("the map has no resolution")
	}
	k := int(math.Round(res / m.Resolution))
	if k < 1 {
		k = 1
	}
	g := &planGrid{
		w:   (m.Width + k - 1) / k,
		h:   (m.Height + k - 1) / k,
		res: float64(k) * m.Resolution,
	}
	g.ox = m.OriginX + float64(k-1)/2*m.Resolution
	g.oy = m.OriginY + float64(k-1)/2*m.Resolution
	n := g.w * g.h
	g.blocked = make([]bool, n)
	g.unknown = make([]bool, n)
	g.clear = make([]float64, n)
	occupied := make([]bool, n)
	seenFree := make([]bool, n)
	for my := 0; my < m.Height; my++ {
		for mx := 0; mx < m.Width; mx++ {
			c := (my/k)*g.w + mx/k
			switch m.Cells[my*m.Width+mx] {
			case forms.MapFree:
				seenFree[c] = true
			case forms.MapUnknown:
			default:
				occupied[c] = true
			}
		}
	}
	// Anything occupied makes a cell a wall: coarsening must never open a gap.
	// Otherwise a cell is free if any of it has been seen free.
	//
	// Not "all of it", which is what this first did. Far from the scanner its
	// rays spread apart, so the map is striped — seen along each ray, unseen
	// between them — and a planner that treated every stripe as a wall could
	// not see past three meters down a twenty-meter corridor. What lies
	// between two rays a few centimeters apart is covered by the clearance
	// margin, and while driving by the guetteur's clearance, which a follower
	// must stop on.
	for c := 0; c < n; c++ {
		switch {
		case occupied[c]:
			g.blocked[c] = true
		case !seenFree[c]:
			g.blocked[c], g.unknown[c] = true, true
		}
	}
	g.bridgeRayGaps(occupied, seenFree)
	return g, nil
}

// bridgeGap is the widest unseen gap, in planning cells, that is taken as free
// when there is ground seen free on both sides of it: three cells, 30 cm at the
// default resolution. The SF45/B's rays are about a degree apart, so that
// bridges the gaps between them out to some seventeen meters.
const bridgeGap = 3

// bridgeRayGaps treats a small unseen gap as free when seen-free ground lies
// on both sides of it, along a row, a column or a diagonal, and nothing
// occupied lies between. That is the space between two neighbouring rays: the
// scanner looked either side of it and found nothing. It is a planning
// assumption and not a fact, which is why it is made here and the map itself
// goes on saying unseen.
//
// One pass, from the map as it was: bridging from bridged cells would let the
// assumption creep into ground nobody has looked at.
func (g *planGrid) bridgeRayGaps(occupied, seenFree []bool) {
	dirs := [][2]int{{1, 0}, {0, 1}, {1, 1}, {1, -1}}
	var bridged []int
	for j := 0; j < g.h; j++ {
		for i := 0; i < g.w; i++ {
			c := g.idx(i, j)
			if !g.unknown[c] {
				continue
			}
			for _, d := range dirs {
				if g.freeWithin(i, j, d[0], d[1], occupied, seenFree) &&
					g.freeWithin(i, j, -d[0], -d[1], occupied, seenFree) {
					bridged = append(bridged, c)
					break
				}
			}
		}
	}
	for _, c := range bridged {
		g.blocked[c], g.unknown[c] = false, false
	}
}

// freeWithin looks from (i, j) in one direction for seen-free ground within
// bridgeGap cells, stopping at anything occupied.
func (g *planGrid) freeWithin(i, j, di, dj int, occupied, seenFree []bool) bool {
	for s := 1; s <= bridgeGap; s++ {
		ni, nj := i+s*di, j+s*dj
		if !g.inside(ni, nj) {
			return false
		}
		n := g.idx(ni, nj)
		if occupied[n] {
			return false
		}
		if seenFree[n] {
			return true
		}
	}
	return false
}

func (g *planGrid) idx(i, j int) int { return j*g.w + i }

func (g *planGrid) inside(i, j int) bool { return i >= 0 && j >= 0 && i < g.w && j < g.h }

func (g *planGrid) cellOf(x, y float64) (int, int) {
	return int(math.Round((x - g.ox) / g.res)), int(math.Round((y - g.oy) / g.res))
}

func (g *planGrid) center(i, j int) (float64, float64) {
	return g.ox + float64(i)*g.res, g.oy + float64(j)*g.res
}

// freeAround clears the unseen cells within radius of a point. The vehicle is
// standing there, so that ground is free whatever the map says; the scanner
// cannot see the ground under itself, and without this the vehicle's own
// position would be a wall and no route could leave it. Cells seen occupied
// are left alone: a wall beside the vehicle is still a wall.
func (g *planGrid) freeAround(x, y, radius float64) {
	ci, cj := g.cellOf(x, y)
	r := int(math.Ceil(radius / g.res))
	for j := cj - r; j <= cj+r; j++ {
		for i := ci - r; i <= ci+r; i++ {
			if !g.inside(i, j) {
				continue
			}
			cx, cy := g.center(i, j)
			if math.Hypot(cx-x, cy-y) > radius {
				continue
			}
			if c := g.idx(i, j); g.unknown[c] {
				g.blocked[c], g.unknown[c] = false, false
			}
		}
	}
}

// measureClearance fills clear with each cell's distance to the nearest
// blocked cell, by a two-pass chamfer. Accurate to a few percent, which is
// plenty for deciding how close to a wall is too close.
func (g *planGrid) measureClearance() {
	inf := math.Inf(1)
	for i := range g.clear {
		if g.blocked[i] {
			g.clear[i] = 0
		} else {
			g.clear[i] = inf
		}
	}
	d1, d2 := g.res, g.res*math.Sqrt2
	relax := func(i, j, ni, nj int, d float64) {
		if !g.inside(ni, nj) {
			return
		}
		c, n := g.idx(i, j), g.idx(ni, nj)
		if g.clear[n]+d < g.clear[c] {
			g.clear[c] = g.clear[n] + d
		}
	}
	for j := 0; j < g.h; j++ {
		for i := 0; i < g.w; i++ {
			relax(i, j, i-1, j, d1)
			relax(i, j, i, j-1, d1)
			relax(i, j, i-1, j-1, d2)
			relax(i, j, i+1, j-1, d2)
		}
	}
	for j := g.h - 1; j >= 0; j-- {
		for i := g.w - 1; i >= 0; i-- {
			relax(i, j, i+1, j, d1)
			relax(i, j, i, j+1, d1)
			relax(i, j, i+1, j+1, d2)
			relax(i, j, i-1, j+1, d2)
		}
	}
}

// allowed says whether the vehicle's middle may be at cell c. Nowhere closer
// to a wall than the clearance — except that where the vehicle already is
// closer, it may stay that close while it gets away: a route that could not
// start from a vehicle parked near a wall would be no route at all.
func (g *planGrid) allowed(c int, need float64) bool {
	return !g.blocked[c] && g.clear[c] >= need
}

// floodFrom is the true distance, around obstacles, from every allowed cell
// to one cell. It guides the search, and a cell it cannot reach is one no
// route can use.
func (g *planGrid) floodFrom(si, sj int, need float64) []float64 {
	dist := make([]float64, g.w*g.h)
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	if !g.inside(si, sj) {
		return dist
	}
	start := g.idx(si, sj)
	dist[start] = 0
	pq := &cellQueue{{cell: start}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(cellItem)
		if cur.dist > dist[cur.cell] {
			continue
		}
		ci, cj := cur.cell%g.w, cur.cell/g.w
		for dj := -1; dj <= 1; dj++ {
			for di := -1; di <= 1; di++ {
				if di == 0 && dj == 0 {
					continue
				}
				ni, nj := ci+di, cj+dj
				if !g.inside(ni, nj) {
					continue
				}
				n := g.idx(ni, nj)
				if !g.allowed(n, need) {
					continue
				}
				step := g.res
				if di != 0 && dj != 0 {
					step *= math.Sqrt2
				}
				if d := cur.dist + step; d < dist[n] {
					dist[n] = d
					heap.Push(pq, cellItem{cell: n, dist: d})
				}
			}
		}
	}
	return dist
}

// Plan is a route, and what the planner had to decide to find it.
type Plan struct {
	X, Y, Heading []float64 // the route, heading in radians, positive counter-clockwise

	GoalX, GoalY float64 // where the route actually ends
	Adjusted     bool    // the goal asked for could not be reached
	Why          string  // why not, when Adjusted
	Expanded     int     // states the search examined
}

// plan finds a forward route from a pose to as near a goal as can be reached.
func plan(m *forms.MapA_v1a, res float64, v Vehicle, sx, sy, sth, gx, gy float64) (*Plan, error) {
	g, err := newPlanGrid(m, res)
	if err != nil {
		return nil, err
	}
	g.freeAround(sx, sy, v.Clearance)
	g.measureClearance()

	si, sj := g.cellOf(sx, sy)
	if !g.inside(si, sj) {
		return nil, fmt.Errorf("the vehicle is outside the map")
	}
	// Where the vehicle already is closer to a wall than the clearance, it may
	// stay that close; never closer.
	need := math.Min(v.Clearance, g.clear[g.idx(si, sj)])
	if need <= 0 {
		return nil, fmt.Errorf("the map shows the vehicle inside an obstacle")
	}

	out := &Plan{GoalX: gx, GoalY: gy}
	gi, gj := g.cellOf(gx, gy)
	fromStart := g.floodFrom(si, sj, need)
	reachable := func(i, j int) bool { return g.inside(i, j) && !math.IsInf(fromStart[g.idx(i, j)], 1) }

	if !reachable(gi, gj) {
		out.Adjusted = true
		switch {
		case !g.inside(gi, gj):
			out.Why = "the goal is outside the map"
		case g.unknown[g.idx(gi, gj)]:
			out.Why = "the goal is somewhere the map has never seen"
		case g.blocked[g.idx(gi, gj)]:
			out.Why = "the goal is on an obstacle"
		case g.clear[g.idx(gi, gj)] < need:
			out.Why = fmt.Sprintf("the goal is closer than %.2f m to a wall", need)
		default:
			out.Why = "the goal is not connected to where the vehicle is"
		}
		best, bestD := -1, math.Inf(1)
		for c, d := range fromStart {
			if math.IsInf(d, 1) {
				continue
			}
			cx, cy := g.center(c%g.w, c/g.w)
			if dd := math.Hypot(cx-gx, cy-gy); dd < bestD {
				best, bestD = c, dd
			}
		}
		if best < 0 {
			return nil, fmt.Errorf("nowhere is reachable from where the vehicle is")
		}
		gi, gj = best%g.w, best/g.w
		out.GoalX, out.GoalY = g.center(gi, gj)
	}

	toGoal := g.floodFrom(gi, gj, need)
	if err := g.search(out, toGoal, v, need, sx, sy, sth); err != nil {
		return nil, err
	}
	return out, nil
}

// The search's tuning. Plain constants: they depend on the geometry of the
// search, not on the site.
const (
	headingBins  = 72  // 5 degrees
	stepFactor   = 1.5 // arc length per step, in planning cells
	curvatureUse = 0.8 // share of the vehicle's tightest turn the planner uses
	steerCost    = 0.4 // per 1/m of change in curvature, in meters
	wallCost     = 1.0 // per meter traveled closer than twice the clearance
	maxExpand    = 400000
)

type state struct {
	x, y, th, kappa float64
	g               float64
	parent          int
}

func (g *planGrid) search(out *Plan, toGoal []float64, v Vehicle, need, sx, sy, sth float64) error {
	kmax := v.MaxCurvature * curvatureUse
	curvatures := []float64{-kmax, -kmax / 2, 0, kmax / 2, kmax}
	step := stepFactor * g.res
	tol := math.Max(g.res, 0.25)

	key := func(x, y, th float64) int {
		i, j := g.cellOf(x, y)
		b := int(math.Floor(wrap(th)/(2*math.Pi)*headingBins+0.5)) % headingBins
		if b < 0 {
			b += headingBins
		}
		return (j*g.w+i)*headingBins + b
	}
	heur := func(x, y float64) float64 {
		i, j := g.cellOf(x, y)
		if !g.inside(i, j) {
			return math.Inf(1)
		}
		return toGoal[g.idx(i, j)]
	}

	states := []state{{x: sx, y: sy, th: sth, parent: -1}}
	best := map[int]float64{key(sx, sy, sth): 0}
	open := &stateQueue{{id: 0, f: heur(sx, sy)}}

	for open.Len() > 0 {
		cur := heap.Pop(open).(stateItem)
		s := states[cur.id]
		if s.g > best[key(s.x, s.y, s.th)] {
			continue
		}
		out.Expanded++
		if math.Hypot(s.x-out.GoalX, s.y-out.GoalY) <= tol {
			g.trace(out, states, cur.id)
			return nil
		}
		if out.Expanded > maxExpand {
			return fmt.Errorf("no route found after examining %d states", maxExpand)
		}
		for _, k := range curvatures {
			nx, ny, nth, ok := g.drive(s.x, s.y, s.th, k, step, need)
			if !ok {
				continue
			}
			h := heur(nx, ny)
			if math.IsInf(h, 1) {
				continue
			}
			i, j := g.cellOf(nx, ny)
			cost := step + steerCost*math.Abs(k-s.kappa)
			if c := g.clear[g.idx(i, j)]; c < 2*v.Clearance {
				cost += wallCost * step * (2*v.Clearance - c) / (2 * v.Clearance)
			}
			ng := s.g + cost
			kk := key(nx, ny, nth)
			if old, seen := best[kk]; seen && ng >= old {
				continue
			}
			best[kk] = ng
			states = append(states, state{x: nx, y: ny, th: nth, kappa: k, g: ng, parent: cur.id})
			heap.Push(open, stateItem{id: len(states) - 1, f: ng + h})
		}
	}
	return fmt.Errorf("no forward route: the vehicle cannot get there without reversing or turning tighter than it can")
}

// drive moves along an arc of curvature k for one step, checking the ground
// all the way, not only where it arrives: an arc can clip a corner that
// neither end touches.
func (g *planGrid) drive(x, y, th, k, s, need float64) (float64, float64, float64, bool) {
	n := int(math.Ceil(s / (g.res / 2)))
	for p := 1; p <= n; p++ {
		d := s * float64(p) / float64(n)
		px, py, _ := arc(x, y, th, k, d)
		i, j := g.cellOf(px, py)
		if !g.inside(i, j) || !g.allowed(g.idx(i, j), need) {
			return 0, 0, 0, false
		}
	}
	nx, ny, nth := arc(x, y, th, k, s)
	return nx, ny, nth, true
}

// arc is where a vehicle ends after driving a distance d on curvature k.
func arc(x, y, th, k, d float64) (float64, float64, float64) {
	if math.Abs(k) < 1e-9 {
		return x + d*math.Cos(th), y + d*math.Sin(th), th
	}
	nth := th + k*d
	return x + (math.Sin(nth)-math.Sin(th))/k, y + (math.Cos(th)-math.Cos(nth))/k, nth
}

func (g *planGrid) trace(out *Plan, states []state, id int) {
	var rev []state
	for ; id >= 0; id = states[id].parent {
		rev = append(rev, states[id])
	}
	for i := len(rev) - 1; i >= 0; i-- {
		out.X = append(out.X, rev[i].x)
		out.Y = append(out.Y, rev[i].y)
		out.Heading = append(out.Heading, wrap(rev[i].th))
	}
}

func wrap(a float64) float64 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a < -math.Pi {
		a += 2 * math.Pi
	}
	return a
}

//-------------------------------------Queues

type cellItem struct {
	cell int
	dist float64
}
type cellQueue []cellItem

func (q cellQueue) Len() int            { return len(q) }
func (q cellQueue) Less(i, j int) bool  { return q[i].dist < q[j].dist }
func (q cellQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *cellQueue) Push(x interface{}) { *q = append(*q, x.(cellItem)) }
func (q *cellQueue) Pop() interface{} {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}

type stateItem struct {
	id int
	f  float64
}
type stateQueue []stateItem

func (q stateQueue) Len() int            { return len(q) }
func (q stateQueue) Less(i, j int) bool  { return q[i].f < q[j].f }
func (q stateQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *stateQueue) Push(x interface{}) { *q = append(*q, x.(stateItem)) }
func (q *stateQueue) Pop() interface{} {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}
