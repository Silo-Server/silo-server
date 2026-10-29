package subsync

// The joint search: when too few windows agree on their own best lags,
// every window's whole lag curve is summed along each candidate line, so
// evidence too weak to win any single window still adds up where the
// subtitle belongs, while wrong lags, scattered, cancel out.

import (
	"math"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// lagCurve is one window's coarse correlation over every lag searched, as
// widened standard scores: pooled[k] is coarse lag k-coarseRange (cue frame
// minus media frame) near media time at.
type lagCurve struct {
	signal *speechSignal
	at     float64
	pooled []float64
}

// curve returns a window's lag curve in standard scores, widened by pool
// coarse frames, or nil when the window has no usable correlation.
func (r *searcher) curve(s *speechSignal, m cueMap, pool int) []float64 {
	n := len(s.coarse)
	if s.corr == nil {
		s.corr = newCorrelator(r.plan, s.coarse)
	}
	r.y = resize(r.y, n+2*r.coarseRange)
	m.sliceCoarse(s.startFrame/coarseFactor-r.coarseRange, r.y)
	r.raw = s.corr.correlate(r.y, r.raw)
	ncc := r.normalize(s.coarse, r.y, r.raw)
	if ncc == nil {
		return nil
	}
	// A lag with too little subtitle text in view says nothing (bestLag's
	// hasEnoughCues); near the ends of the range only a sliver of the
	// subtitle overlaps the window, and its correlation is noise.
	need := minCueSeconds / (frameSeconds * coarseFactor)
	valid := make([]bool, len(ncc))
	var sum, sumSq float64
	count := 0
	for k, v := range ncc {
		if r.sum[k+n]-r.sum[k] < need {
			continue
		}
		valid[k] = true
		sum += v
		sumSq += v * v
		count++
	}
	if count < 2 {
		return nil
	}
	mean := sum / float64(count)
	std := math.Sqrt(max(sumSq/float64(count)-mean*mean, 0))
	if std == 0 {
		return nil
	}
	z := make([]float64, len(ncc))
	for k, v := range ncc {
		if valid[k] {
			z[k] = (v - mean) / std
		}
	}
	return runningMax(z, pool)
}

// runningMax returns the maximum of x over [i-w, i+w] at each i.
func runningMax(x []float64, w int) []float64 {
	if w <= 0 {
		return x
	}
	out := make([]float64, len(x))
	for i := range x {
		best := math.Inf(-1)
		for j := max(0, i-w); j <= min(len(x)-1, i+w); j++ {
			best = max(best, x[j])
		}
		out[i] = best
	}
	return out
}

// jointPeak is the line every window's curve agrees on best: offset(t) =
// Offset + Drift*(t - center).
type jointPeak struct {
	Offset, Drift, Center float64
	Score, Prominence     float64
}

// bestJoint sums the windows' curves along every line of the drift grid
// and returns the highest sum with its prominence over the other offsets
// of that drift.
func bestJoint(curves []lagCurve, coarseRange int) jointPeak {
	coarseSeconds := frameSeconds * coarseFactor
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, c := range curves {
		lo, hi = min(lo, c.at), max(hi, c.at)
	}
	center := (lo + hi) / 2
	half := max((hi-lo)/2, 1)
	// A drift step that moves the farthest window by one pooled width, with
	// at most maxJointDriftSteps each way: past that span (about 6 hours
	// of windows) the grid coarsens instead of the cost growing.
	step := 2 * jointPoolSeconds / half
	steps := int(math.Ceil(maxDrift / step))
	if steps > maxJointDriftSteps {
		steps = maxJointDriftSteps
		step = maxDrift / float64(steps)
	}
	norm := 1 / math.Sqrt(float64(len(curves)))
	total := make([]float64, 2*coarseRange+1)
	var best jointPeak
	best.Score = math.Inf(-1)
	for d := -steps; d <= steps; d++ {
		drift := float64(d) * step
		if math.Abs(drift) > maxDrift {
			drift = math.Copysign(maxDrift, drift)
		}
		clear(total)
		for _, c := range curves {
			shift := int(math.Round(drift * (c.at - center) / coarseSeconds))
			// Lag L (cue minus media, coarse) of the line sits at index
			// L + coarseRange - shift in this window's curve.
			for i := range total {
				k := i - shift
				if k >= 0 && k < len(c.pooled) {
					total[i] += c.pooled[k]
				}
			}
		}
		peak := argmax(total)
		if score := total[peak] * norm; score > best.Score {
			best = jointPeak{
				Offset: -float64(peak-coarseRange) * coarseSeconds, Drift: drift, Center: center, Score: score,
				Prominence: prominence(total, peak, int(prominenceExclusionSeconds/coarseSeconds)),
			}
		}
	}
	return best
}

// localLag searches a window frame by frame within localSearchSeconds of
// the offset expected at its time.
func localLag(s *speechSignal, m cueMap, offset, prom float64) (windowLag, bool) {
	span := int(localSearchSeconds / frameSeconds)
	lag := -toFrame(offset) // cue frame = media frame + lag
	y := m.sliceFine(s.startFrame+lag-span, len(s.fine)+2*span)
	if !hasEnoughCues(y, minCueSeconds/frameSeconds) {
		return windowLag{}, false
	}
	fine := normalizedCorrelation(s.fine, y, crossCorrelate(s.fine, y))
	if fine == nil {
		return windowLag{}, false
	}
	best := argmax(fine)
	// The margins can hold the cues the chosen lag's own stretch lacks.
	if !hasEnoughCues(y[best:best+len(s.fine)], minCueSeconds/frameSeconds) {
		return windowLag{}, false
	}
	lagFrames := lag - span + best
	return windowLag{At: s.middle(), OffsetSeconds: -float64(lagFrames) * frameSeconds, Score: fine[best], Prominence: prom, signal: s}, true
}

// alignJoint finds the line the windows' summed lag curves agree on best,
// then each window's own lag near it, fitted as alignWindows fits. ok is
// false when no window produced a curve.
func alignJoint(signals []*speechSignal, cues []subtitles.SubtitleCue, search *searcher) (Alignment, bool) {
	pool := int(math.Round(jointPoolSeconds / (frameSeconds * coarseFactor)))

	var best jointPeak
	best.Score = math.Inf(-1)
	var bestMap cueMap
	var bestScale float64
	var bestCurves []lagCurve
	for _, scale := range scaleCandidates {
		m := newCueMap(cues, scale)
		var curves []lagCurve
		for _, s := range signals {
			if c := search.curve(s, m, pool); c != nil {
				curves = append(curves, lagCurve{signal: s, at: s.middle(), pooled: c})
			}
		}
		if len(curves) == 0 {
			continue
		}
		if peak := bestJoint(curves, search.coarseRange); peak.Score > best.Score {
			best, bestMap, bestScale, bestCurves = peak, m, scale, curves
		}
	}
	if bestCurves == nil {
		return Alignment{}, false
	}
	var lags []windowLag
	for _, c := range bestCurves {
		expected := best.Offset + best.Drift*(c.at-best.Center)
		k := search.coarseRange - int(math.Round(expected/(frameSeconds*coarseFactor)))
		prom := 0.0
		if k >= 0 && k < len(c.pooled) {
			prom = c.pooled[k]
		}
		if lag, ok := localLag(c.signal, bestMap, expected, prom); ok {
			lags = append(lags, lag)
		}
	}
	// Every lag here lies within localSearchSeconds of the line, so a
	// window of noise lands within inlierSeconds of it by chance far more
	// often than in the window search: Inliers counts for less here, and
	// Matched holds the joint search to its own, measured thresholds.
	fit := fitLine(lags)
	result := Alignment{Windows: len(bestCurves), Inliers: len(fit.Group), JointProminence: best.Prominence}
	result.finish(fit, bestMap, bestScale)
	return result, true
}
