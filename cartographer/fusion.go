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

import "math"

// Weighing the view against the wheels.
//
// The first versions decided: the view fixed the pose, or it did not and the
// wheels did, or — in a corridor — the view fixed the heading and the wheels
// the distance. Each decision was a threshold on how much the fit fell when the
// pose was nudged, and everything on the wrong side of a threshold counted for
// nothing.
//
// Now both are estimates with an uncertainty, and the pose is their weighted
// combination: a Kalman filter on the pose. The wheels' uncertainty grows with
// the distance driven and the angle turned; the view's comes from how the fit
// falls off around its best answer, as Olson computes it for correlative scan
// matching (Real-time correlative scan matching, ICRA 2009, §F). Down a
// corridor the view is certain across it and of the heading, and has nothing
// to say along it, so the wheels decide the distance and the walls the rest —
// with no threshold deciding which, and with a partly seen doorway counting
// for what it is worth. The covariance this leaves is also what a pose graph
// needs, when there is one.

// mat3 is a 3×3 matrix over (x, y, heading).
type mat3 [3][3]float64

func diag3(a, b, c float64) mat3 { return mat3{{a, 0, 0}, {0, b, 0}, {0, 0, c}} }

func (a mat3) add(b mat3) mat3 {
	for i := range a {
		for j := range a[i] {
			a[i][j] += b[i][j]
		}
	}
	return a
}

func (a mat3) mulVec(v [3]float64) [3]float64 {
	var r [3]float64
	for i := range a {
		for j := range a[i] {
			r[i] += a[i][j] * v[j]
		}
	}
	return r
}

func (a mat3) isZero() bool { return a == mat3{} }

// inverse by the adjugate; ok is false for a singular matrix.
func (a mat3) inverse() (mat3, bool) {
	c := mat3{
		{a[1][1]*a[2][2] - a[1][2]*a[2][1], a[0][2]*a[2][1] - a[0][1]*a[2][2], a[0][1]*a[1][2] - a[0][2]*a[1][1]},
		{a[1][2]*a[2][0] - a[1][0]*a[2][2], a[0][0]*a[2][2] - a[0][2]*a[2][0], a[0][2]*a[1][0] - a[0][0]*a[1][2]},
		{a[1][0]*a[2][1] - a[1][1]*a[2][0], a[0][1]*a[2][0] - a[0][0]*a[2][1], a[0][0]*a[1][1] - a[0][1]*a[1][0]},
	}
	det := a[0][0]*c[0][0] + a[0][1]*c[1][0] + a[0][2]*c[2][0]
	if det == 0 || math.IsNaN(det) {
		return mat3{}, false
	}
	for i := range c {
		for j := range c[i] {
			c[i][j] /= det
		}
	}
	return c, true
}

// eigen returns the eigenvalues of a symmetric matrix and its eigenvectors as
// the columns of the second result, by Jacobi rotations.
func (a mat3) eigen() ([3]float64, mat3) {
	v := diag3(1, 1, 1)
	for sweep := 0; sweep < 50; sweep++ {
		off := a[0][1]*a[0][1] + a[0][2]*a[0][2] + a[1][2]*a[1][2]
		if off < 1e-30 {
			break
		}
		for p := 0; p < 2; p++ {
			for q := p + 1; q < 3; q++ {
				if a[p][q] == 0 {
					continue
				}
				theta := (a[q][q] - a[p][p]) / (2 * a[p][q])
				t := math.Copysign(1, theta) / (math.Abs(theta) + math.Sqrt(theta*theta+1))
				c := 1 / math.Sqrt(t*t+1)
				s := t * c
				for k := 0; k < 3; k++ {
					akp, akq := a[k][p], a[k][q]
					a[k][p], a[k][q] = c*akp-s*akq, s*akp+c*akq
				}
				for k := 0; k < 3; k++ {
					apk, aqk := a[p][k], a[q][k]
					a[p][k], a[q][k] = c*apk-s*aqk, s*apk+c*aqk
				}
				for k := 0; k < 3; k++ {
					vkp, vkq := v[k][p], v[k][q]
					v[k][p], v[k][q] = c*vkp-s*vkq, s*vkp+c*vkq
				}
			}
		}
	}
	return [3]float64{a[0][0], a[1][1], a[2][2]}, v
}

//-------------------------------------The view's uncertainty

// matchSample is one pose the coarse search scored: its offset from where the
// search was centered, and the score at full stride.
type matchSample struct {
	d     [3]float64
	score float64
}

// flatVariance is where a direction stops counting as seen. Variances are
// measured in units of the search window's half-width; a fit that does not fall
// at all across the window spreads its weight evenly over it, a variance of
// about a third, and anything wider than four-tenths of the half-width either
// way (0.4² = 0.16) is taken to be that. Without this a direction the view
// cannot see would still be reported as known to within the window, because
// nothing outside the window was ever scored.
const flatVariance = 0.16

// matchNat is how much worse a fit, as a fraction of the best, makes a pose e
// times less likely; see matchInformation.
const matchNat = 0.05

// matchInformation is how much the search's scores say about the pose, as an
// information matrix in the map frame, and in how many independent directions
// they say anything at all.
//
// Each set of scores is turned into likelihoods and a Gaussian fitted to them
// (Olson's equation 3). Two questions are asked of two sets, because one scale
// cannot answer both.
//
// Whether a direction is seen at all is asked of the coarse pass, over the
// whole window, with the scores read loosely: a fit worse by matchNat of the
// best is e times less likely. A sweep's own sampling pattern moves the score
// by a few percent wherever it is put, and so do the ends of walls the map has
// not drawn yet; read any more tightly, those few percent decide the position
// along a corridor and the matcher drags the map back onto what it drew last
// time — the overconfidence Olson warns of. A direction in which even this
// loose likelihood spreads across the window is not seen, and contributes
// nothing.
//
// How well the seen directions are fixed is asked of the fine pass, close
// around the peak, where the fit falls as a quadratic and the score can be
// read for what it is: near a wall the likelihood field, exp(−d²/2σ²), is one
// plus the log of the same Gaussian, so the score is the log-likelihood if the
// returns are independent. They are not quite, which the search's own step,
// added as a floor, and the wheels' own uncertainty keep in check. Read
// loosely too, a corridor's walls would fix the heading to a degree, the
// wheels' account would outweigh them, and a wheel slightly larger than the
// other would turn the map after all.
func matchInformation(wide, near []matchSample, cfg SearchConfig) (mat3, int) {
	// Whether: the window, read loosely, in units of its half-width so that
	// one threshold serves position and heading.
	scale := [3]float64{cfg.SearchRange, cfg.SearchRange, cfg.AngleRange}
	cov, ok := fitGaussian(wide, matchNat, cfg.CoarseStep, cfg.CoarseAngle)
	if !ok {
		return mat3{}, 0
	}
	var norm mat3
	for j := 0; j < 3; j++ {
		for k := 0; k < 3; k++ {
			norm[j][k] = cov[j][k] / (scale[j] * scale[k])
		}
	}
	values, vectors := norm.eigen()

	// How well: the peak, read as a log-likelihood.
	peak, ok := fitGaussian(near, 0, cfg.FineStep, cfg.FineAngle)
	if !ok {
		return mat3{}, 0
	}
	info, ok := peak.inverse()
	if !ok {
		return mat3{}, 0
	}

	// Nothing in the directions the window did not see. The projector keeps
	// every displacement except along those directions, which it sends to
	// zero, so the information has nothing to say there.
	project := diag3(1, 1, 1)
	seen := 3
	for e, lambda := range values {
		if lambda < flatVariance && lambda > 0 {
			continue
		}
		seen--
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				// D·(v vᵀ)·D⁻¹, the window's direction in real units.
				project[j][k] -= scale[j] * vectors[j][e] * vectors[k][e] / scale[k]
			}
		}
	}
	if seen == 0 {
		return mat3{}, 0
	}
	if seen < 3 {
		var projected mat3
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				for j := 0; j < 3; j++ {
					for k := 0; k < 3; k++ {
						projected[a][b] += project[j][a] * info[j][k] * project[k][b]
					}
				}
			}
		}
		info = projected
	}
	return info, seen
}

// fitGaussian fits a Gaussian to scored poses, each weighted by its likelihood:
// exp of the score less the best, divided by nat times the best when nat is
// not zero. To its covariance is added that of a uniform spread over one step
// of the search, which is as finely as the search can tell.
func fitGaussian(samples []matchSample, nat, linStep, angStep float64) (mat3, bool) {
	top := math.Inf(-1)
	for _, s := range samples {
		top = math.Max(top, s.score)
	}
	if len(samples) == 0 || top <= 0 {
		return mat3{}, false
	}
	var mean [3]float64
	var total float64
	w := make([]float64, len(samples))
	for i, s := range samples {
		if nat > 0 {
			w[i] = math.Exp((s.score - top) / (nat * top))
		} else {
			w[i] = math.Exp(s.score - top)
		}
		total += w[i]
		for k := range mean {
			mean[k] += w[i] * s.d[k]
		}
	}
	for k := range mean {
		mean[k] /= total
	}
	var cov mat3
	for i, s := range samples {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				cov[j][k] += w[i] * (s.d[j] - mean[j]) * (s.d[k] - mean[k]) / total
			}
		}
	}
	return cov.add(diag3(linStep*linStep/12, linStep*linStep/12, angStep*angStep/12)), true
}

//-------------------------------------The wheels' uncertainty

// OdometryNoise is how far the wheels' account of a step may be off, growing
// with the step. Each is one standard deviation.
type OdometryNoise struct {
	// AlongFraction and CrossFraction are the error in the distance driven,
	// and sideways to it, as a fraction of that distance.
	AlongFraction float64 `json:"alongFraction"`
	CrossFraction float64 `json:"crossFraction"`

	// HeadingDegreesPerMeter is the turn the wheels may report that never
	// happened, per meter driven: a 1 % difference between the two wheels
	// is about 0.9° per meter.
	HeadingDegreesPerMeter float64 `json:"headingDegreesPerMeter"`

	// TurnFraction is the error in a turn, as a fraction of it: the tires
	// scrub when the vehicle turns, and the track it behaves as having is not
	// the one measured with a tape.
	TurnFraction float64 `json:"turnFraction"`
}

func defaultOdometryNoise() OdometryNoise {
	return OdometryNoise{AlongFraction: 0.03, CrossFraction: 0.02, HeadingDegreesPerMeter: 1.5, TurnFraction: 0.1}
}

// The least uncertainty a step has, standing still included: the encoders'
// and the clocks' resolution.
const (
	minStepMetres  = 0.005
	minStepRadians = 0.1 * math.Pi / 180
)

// stepCovariance is the uncertainty of the wheels' step delta, taken from a
// pose with heading theta, in the map frame.
func (n OdometryNoise) stepCovariance(delta pose, theta float64) mat3 {
	ds := math.Hypot(delta.x, delta.y)
	along := math.Max(minStepMetres, n.AlongFraction*ds)
	cross := math.Max(minStepMetres, n.CrossFraction*ds)
	turn := math.Max(minStepRadians,
		n.HeadingDegreesPerMeter*math.Pi/180*ds+n.TurnFraction*math.Abs(delta.theta))
	dir := theta
	if ds > 1e-6 {
		dir += math.Atan2(delta.y, delta.x)
	}
	c, s := math.Cos(dir), math.Sin(dir)
	a2, c2 := along*along, cross*cross
	return mat3{
		{c*c*a2 + s*s*c2, c * s * (a2 - c2), 0},
		{c * s * (a2 - c2), s*s*a2 + c*c*c2, 0},
		{0, 0, turn * turn},
	}
}

//-------------------------------------The two together

// The filter's state is the pose and one more number: the turn the wheels
// report per meter that never happened, drift, in radians per meter. Wheels
// of slightly different size do exactly that, every meter, in the same
// direction. Treated as noise it is not noise: each step's share of it that
// the view does not overrule is drawn into the map, the next sweep is matched
// against that map, and the map turns. So it is estimated instead. Wherever
// the walls fix the heading the filter learns how much the wheels turn
// without turning, and takes it out of every step after; it is then the
// wheel-size error UMBmark measures, found while driving.

// filterState is the pose and the drift, with their joint covariance.
type filterState struct {
	at    pose
	drift float64
	cov   mat4
}

// mat4 is a 4×4 matrix over (x, y, heading, drift).
type mat4 [4][4]float64

func (a mat4) add(b mat4) mat4 {
	for i := range a {
		for j := range a[i] {
			a[i][j] += b[i][j]
		}
	}
	return a
}

func (a mat4) mul(b mat4) mat4 {
	var c mat4
	for i := range a {
		for j := range b[0] {
			for k := range b {
				c[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return c
}

func (a mat4) transpose() mat4 {
	var t mat4
	for i := range a {
		for j := range a[i] {
			t[j][i] = a[i][j]
		}
	}
	return t
}

func (a mat4) mulVec(v [4]float64) [4]float64 {
	var r [4]float64
	for i := range a {
		for j := range a[i] {
			r[i] += a[i][j] * v[j]
		}
	}
	return r
}

// inverse by Gauss–Jordan elimination with partial pivoting.
func (a mat4) inverse() (mat4, bool) {
	inv := mat4{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}
	for col := 0; col < 4; col++ {
		pivot := col
		for r := col + 1; r < 4; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if a[pivot][col] == 0 || math.IsNaN(a[pivot][col]) {
			return mat4{}, false
		}
		a[col], a[pivot] = a[pivot], a[col]
		inv[col], inv[pivot] = inv[pivot], inv[col]
		p := a[col][col]
		for j := 0; j < 4; j++ {
			a[col][j] /= p
			inv[col][j] /= p
		}
		for r := 0; r < 4; r++ {
			if r == col {
				continue
			}
			f := a[r][col]
			for j := 0; j < 4; j++ {
				a[r][j] -= f * a[col][j]
				inv[r][j] -= f * inv[col][j]
			}
		}
	}
	return inv, true
}

// embed puts a 3×3 pose matrix in the top left of a 4×4 one.
func embed(m mat3) mat4 {
	var r mat4
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			r[i][j] = m[i][j]
		}
	}
	return r
}

// The drift before anything is known of it, and how fast it may change.
// Two degrees per meter is a little more than the 1.7 % difference between
// the Artitrax's front tires, measured in October 2026; it changes as the
// tires warm, lose pressure, or meet a different floor, but slowly.
const (
	initialDriftRadiansPerMeter = 2 * math.Pi / 180
	driftChangePerRootMeter     = 0.05 * math.Pi / 180
)

func newFilter() filterState {
	var c mat4
	c[3][3] = initialDriftRadiansPerMeter * initialDriftRadiansPerMeter
	return filterState{cov: c}
}

// predict moves the state by the wheels' step delta, taken in the frame of
// the current pose, less the drift the filter has learned, and grows the
// covariance by the step's own uncertainty.
func (f filterState) predict(delta pose, noise OdometryNoise) filterState {
	// Forward is positive, backward negative: smaller wheels turn the vehicle
	// the other way when it reverses.
	travel := delta.x
	corrected := pose{delta.x, delta.y, delta.theta - f.drift*travel}
	next := compose(f.at, corrected)

	// How the new pose depends on the old one and on the drift.
	c, s := math.Cos(f.at.theta), math.Sin(f.at.theta)
	jac := mat4{
		{1, 0, -s*corrected.x - c*corrected.y, 0},
		{0, 1, c*corrected.x - s*corrected.y, 0},
		{0, 0, 1, -travel},
		{0, 0, 0, 1},
	}
	q := embed(noise.stepCovariance(delta, f.at.theta))
	q[3][3] = driftChangePerRootMeter * driftChangePerRootMeter * math.Abs(travel)
	return filterState{at: next, drift: f.drift, cov: jac.mul(f.cov).mul(jac.transpose()).add(q)}
}

// update weighs the prediction against the view's match. Where the view says
// nothing the prediction stands; where it is certain it wins; in between each
// counts by its weight. The drift is never seen directly, only through what
// the view says about the heading.
func (f filterState) update(m matchResult) filterState {
	if m.info.isZero() {
		return f
	}
	priorInfo, ok := f.cov.inverse()
	if !ok {
		return f
	}
	info := embed(m.info)
	cov, ok := priorInfo.add(info).inverse()
	if !ok {
		return f
	}
	d := [4]float64{m.at.x - f.at.x, m.at.y - f.at.y, wrapAngle(m.at.theta - f.at.theta), 0}
	c := cov.mulVec(info.mulVec(d))
	return filterState{
		at:    pose{f.at.x + c[0], f.at.y + c[1], wrapAngle(f.at.theta + c[2])},
		drift: f.drift + c[3],
		cov:   cov,
	}
}

// fix is the view's match taken on its own, where it fixes everything and
// there are no wheels: the pose is the match's, and so is its uncertainty.
func (f filterState) fix(m matchResult) filterState {
	poseCov, ok := m.info.inverse()
	if !ok {
		return f
	}
	cov := embed(poseCov)
	cov[3][3] = f.cov[3][3]
	return filterState{at: m.at, drift: f.drift, cov: cov}
}
