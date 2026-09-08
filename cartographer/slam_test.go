package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/sdoque/mbaigo/forms"
)

// A corridor with a doorway, matching the one the guetteur's simulator draws.
// A plain corridor is deliberately not used: nothing in a straight, featureless
// hallway says how far along it you are, so a matcher tested only against one
// would look far better than it is.
var testWalls = []struct{ x1, y1, x2, y2 float64 }{
	{0, -1.5, 20, -1.5},
	{0, 1.5, 8, 1.5},
	{9.2, 1.5, 20, 1.5},
	{8, 1.5, 8, 2.7},
	{9.2, 1.5, 9.2, 2.7},
	{8, 2.7, 9.2, 2.7},
	{20, -1.5, 20, 1.5},
	{0, -1.5, 0, 1.5},
}

// sweepFrom ray-casts the corridor from a known pose, which is what makes the
// matcher testable: the answer is known before it is asked for.
func sweepFrom(p pose, points int, sector float64) ([]float64, []float64, []bool) {
	angles := make([]float64, 0, points)
	dists := make([]float64, 0, points)
	valid := make([]bool, 0, points)
	half := sector / 2
	for i := 0; i < points; i++ {
		a := -half + float64(i)*sector/float64(points-1)
		theta := p.theta + a*math.Pi/180
		dx, dy := math.Cos(theta), math.Sin(theta)
		best := math.Inf(1)
		for _, w := range testWalls {
			sx, sy := w.x2-w.x1, w.y2-w.y1
			den := dx*sy - dy*sx
			if math.Abs(den) < 1e-12 {
				continue
			}
			t := ((w.x1-p.x)*sy - (w.y1-p.y)*sx) / den
			u := ((w.x1-p.x)*dy - (w.y1-p.y)*dx) / den
			if t >= 0 && u >= 0 && u <= 1 && t < best {
				best = t
			}
		}
		angles = append(angles, a)
		if math.IsInf(best, 1) {
			dists = append(dists, 0)
			valid = append(valid, false)
		} else {
			dists = append(dists, best)
			valid = append(valid, true)
		}
	}
	return angles, dists, valid
}

// A straight corridor cannot say how far along it you are. Two parallel walls
// look identical from everywhere between them, and no amount of processing
// recovers what the geometry never encoded.
//
// So this test does not ask the matcher to succeed. It asks it to KNOW that it
// has not — because the failure that matters is not a wrong pose, it is a wrong
// pose reported confidently, which produces a corridor of the wrong length that
// looks entirely convincing.
func TestMatcherAdmitsWhenTheCorridorCannotFixIt(t *testing.T) {
	g := newGrid(40, 40, 0.05)
	g.matchRange = 10
	cfg := defaultSearch()

	truth := pose{x: 2, y: 0, theta: 0}
	a, d, v := sweepFrom(truth, 200, 200)
	g.integrate(truth, a, d, v)

	truth.x += 0.07
	a, d, v = sweepFrom(truth, 200, 200)
	m := g.match(pose{2.07, 0, 0}, a, d, v, cfg)

	if m.sharp {
		t.Error("the matcher called a featureless corridor a sharp fix; " +
			"it cannot know where along the corridor it is, and must say so")
	}
}

// Given odometry — which is what the wheel encoders and the waist encoder
// provide — the along-corridor motion is supplied and the matcher only has to
// keep the vehicle straight and centred. That is the regime this system is
// built for, and it must track.
func TestMatcherTracksWithOdometry(t *testing.T) {
	g := newGrid(40, 40, 0.05)
	g.matchRange = 10
	cfg := defaultSearch()

	truth := pose{x: 2, y: 0, theta: 0}
	a, d, v := sweepFrom(truth, 200, 200)
	g.integrate(truth, a, d, v)
	est := truth

	const step = 0.07
	worst := 0.0
	for i := 0; i < 30; i++ {
		truth = pose{truth.x + step, truth.y, truth.theta}
		a, d, v = sweepFrom(truth, 200, 200)

		// Odometry with a realistic 4% scale error and a slow heading bias,
		// which is what wheel encoders on a slipping machine actually give.
		prior := pose{est.x + step*1.04, est.y, est.theta + 0.05*math.Pi/180}
		m := g.match(prior, a, d, v, cfg)
		if m.sharp {
			est = m.at
		} else {
			est = prior // trust odometry where the view says nothing
		}
		g.integrate(est, a, d, v)

		if e := math.Hypot(est.x-truth.x, est.y-truth.y); e > worst {
			worst = e
		}
	}
	t.Logf("30 steps, %.2f m driven: worst error %.3f m, final (%.2f,%.2f) vs truth (%.2f,%.2f)",
		30*step, worst, est.x, est.y, truth.x, truth.y)

	// 4% of 2.1 m is 8 cm of odometry scale error, and with the corridor unable
	// to correct it that is the error to expect. What must not happen is the
	// estimate sitting still while the vehicle drives away.
	if worst > 0.20 {
		t.Errorf("worst tracking error %.3f m over %.2f m driven, want <= 0.20", worst, 30*step)
	}
	if math.Abs(est.x-truth.x) > 0.20 {
		t.Errorf("final along-corridor error %.3f m; the estimate did not follow the vehicle",
			math.Abs(est.x-truth.x))
	}
}

// A turn is constrained by the walls even in a corridor, so the matcher must
// get it right on its own.
func TestScanMatcherFollowsATurn(t *testing.T) {
	g := newGrid(40, 40, 0.05)
	g.matchRange = 10
	cfg := defaultSearch()

	truth := pose{x: 4, y: 0, theta: 0}
	a, d, v := sweepFrom(truth, 200, 200)
	g.integrate(truth, a, d, v)
	est := truth

	for i := 0; i < 10; i++ {
		truth = pose{truth.x, truth.y, truth.theta + 2*math.Pi/180}
		a, d, v = sweepFrom(truth, 200, 200)
		est = g.match(est, a, d, v, cfg).at
		g.integrate(est, a, d, v)
	}
	if e := math.Abs(wrapAngle(est.theta-truth.theta)) * 180 / math.Pi; e > 3 {
		t.Errorf("after turning 20°, heading error %.2f°, want <= 3", e)
	}
}

func TestIntegrateMarksFreeBetweenSensorAndWall(t *testing.T) {
	g := newGrid(20, 20, 0.1)
	at := pose{x: 0, y: 0, theta: 0}
	// One beam straight ahead, hitting something 3 m away.
	g.integrate(at, []float64{0}, []float64{3}, []bool{true})

	cx, cy, _ := g.cellOf(3, 0)
	if g.at(cx, cy) <= 0 {
		t.Errorf("the cell at the end of the beam is not occupied: %v", g.at(cx, cy))
	}
	mx, my, _ := g.cellOf(1.5, 0)
	if g.at(mx, my) >= 0 {
		t.Errorf("the cell halfway along the beam is not free: %v", g.at(mx, my))
	}
	// Beyond the obstacle nothing was observed, and must stay unknown.
	bx, by, _ := g.cellOf(4.5, 0)
	if g.at(bx, by) != 0 {
		t.Errorf("the cell behind the obstacle was written to: %v", g.at(bx, by))
	}
}

// An invalid point must contribute nothing at all. Treating it as free space to
// maximum range is the failure this whole design is built to avoid.
func TestIntegrateIgnoresInvalidReturns(t *testing.T) {
	g := newGrid(20, 20, 0.1)
	g.integrate(pose{}, []float64{0}, []float64{0}, []bool{false})
	for i, v := range g.cells {
		if v != 0 {
			t.Fatalf("cell %d was written from an invalid return: %v", i, v)
		}
	}
}

func TestFormKeepsUnknownDistinctFromFree(t *testing.T) {
	g := newGrid(2, 2, 1)
	g.add(0, 0, logFree)
	g.add(1, 1, logOcc)

	f := g.form()
	if err := f.Check(); err != nil {
		t.Fatalf("form does not check out: %v", err)
	}
	if f.Cells[0] != forms.MapFree {
		t.Errorf("observed-free cell is %d, want %d", f.Cells[0], forms.MapFree)
	}
	if f.Cells[3] != forms.MapOccupied {
		t.Errorf("occupied cell is %d, want %d", f.Cells[3], forms.MapOccupied)
	}
	if f.Cells[1] != forms.MapUnknown {
		t.Errorf("untouched cell is %d, want %d (unknown)", f.Cells[1], forms.MapUnknown)
	}
}

func TestWritePGM(t *testing.T) {
	g := newGrid(1, 1, 0.5) // 2x2
	g.add(0, 0, logOcc)
	path := filepath.Join(t.TempDir(), "map.pgm")
	if err := g.writePGM(path); err != nil {
		t.Fatalf("writePGM: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if want := "P5\n2 2\n255\n"; string(b[:len(want)]) != want {
		t.Errorf("header is %q, want %q", string(b[:len(want)]), want)
	}
	if len(b) != len("P5\n2 2\n255\n")+4 {
		t.Errorf("file holds %d bytes, want header + 4 pixels", len(b))
	}
}

func TestLineExcludesTheEndpoint(t *testing.T) {
	cells := line(0, 0, 3, 0)
	for _, c := range cells {
		if c[0] == 3 && c[1] == 0 {
			t.Fatal("the endpoint is in the free-space list; it is the obstacle")
		}
	}
	if len(cells) != 3 {
		t.Errorf("got %d intermediate cells, want 3", len(cells))
	}
}
