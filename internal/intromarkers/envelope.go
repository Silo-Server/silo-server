package intromarkers

import (
	"math"
	"sort"
)

// Envelope matching finds the stretches of two episodes' openings that carry
// the same recording.
//
// It is the same idea as the subtitle aligner's speech correlation, applied to
// a season: a file's loudness curve, gain normalized, is cross-correlated with
// a partner's at every offset the window allows, the strongest offsets are
// kept, and each of them is then walked frame by frame with a short sliding
// correlation to find where the two actually agree. The band limit
// (200 to 3400 Hz) is what makes two lossy encodes of one theme correlate;
// gain normalization is what makes a quiet theme count as much as a loud one.
//
// Unlike a Chromaprint match, the result has no fixed lead to correct: an
// edge is the first frame where the shared recording starts, within the hop.

// envelope is one file's loudness trace.
type envelope struct {
	// db holds the raw level of each frame, in dB above -100 dBFS, as
	// measured. Edges and refinements read it.
	db []float64
	// v holds the same frames with the window's mean removed and scaled to
	// unit variance, which is what correlation reads. normalized builds it.
	v   []float64
	hop float64
}

func newEnvelope(db []float64, hop float64) envelope {
	if hop <= 0 {
		hop = LevelsHopSeconds
	}
	return envelope{db: db, hop: hop}
}

// normalized returns a copy with v filled in. A flat envelope, such as a
// window of digital silence, normalizes to zeros, which correlate with
// nothing rather than with everything.
func (e envelope) normalized() envelope {
	out := envelope{db: e.db, v: make([]float64, len(e.db)), hop: e.hop}
	if len(e.db) == 0 {
		return out
	}
	var sum float64
	for _, db := range e.db {
		sum += db
	}
	mean := sum / float64(len(e.db))
	var variance float64
	for _, db := range e.db {
		d := db - mean
		variance += d * d
	}
	variance /= float64(len(e.db))
	if variance <= 1e-6 {
		return out
	}
	scale := 1 / math.Sqrt(variance)
	for i, db := range e.db {
		out.v[i] = (db - mean) * scale
	}
	return out
}

// len is the number of frames.
func (e envelope) len() int { return len(e.v) }

// seconds is the duration the envelope covers.
func (e envelope) seconds() float64 { return float64(len(e.v)) * e.hop }

// dbAt returns the raw level at a time, and whether the envelope covers it.
func (e envelope) dbAt(seconds float64) (float64, bool) {
	index := int(math.Round(seconds / e.hop))
	if index < 0 || index >= len(e.db) {
		return 0, false
	}
	return e.db[index], true
}

// levelMatchConfig tunes how two envelopes are compared. The values follow the
// reference engine's rules, whose aligner measured them on lossy encodes of
// one recording, and are re-tuned here for a 10 ms loudness trace instead of
// an 8 kHz waveform: the correlation window is a second, because a ten-frame
// window carries too little shape, and the run threshold is lower, because a
// level trace of two encodes agrees to about 0.7 where the waveforms agree to
// 0.98.
type levelMatchConfig struct {
	// WindowSeconds is the sliding correlation window.
	WindowSeconds float64
	// HopSeconds is how often that window is scored.
	HopSeconds float64
	// MinCorrelation is the least similarity a scored window needs to count
	// as shared.
	MinCorrelation float64
	// BridgeSeconds is the longest gap between shared windows that still
	// reads as one stretch. It is short on purpose: a studio logo and the
	// theme after it are two recordings separated by a second or two of
	// silence, and bridging them is what made a Chromaprint match start an
	// intro at zero.
	BridgeSeconds float64
	// MinRunSeconds is the shortest shared stretch worth reporting.
	MinRunSeconds float64
	// Offsets is how many offsets the search walks.
	Offsets int
}

// The offset search runs at a coarser resolution than the run walk: a tenth of
// a second per bin, scored two seconds at a time.
const (
	coarseStepSeconds   = 0.2
	coarseWindowSeconds = 2.0
	// pairCorrelationTie is how close two stretches' correlations are before
	// their lengths decide which describes the shared recording.
	pairCorrelationTie = 0.02
)

// defaultLevelMatchConfig is the profile intro detection compares with.
func defaultLevelMatchConfig() levelMatchConfig {
	return levelMatchConfig{
		// A window has to be shorter than the gaps the comparison must not
		// bridge: a second of a studio logo and the theme after it correlate
		// as one stretch through a one-second window, because the step from
		// silence to music is itself a feature both files share.
		WindowSeconds:  0.4,
		HopSeconds:     0.1,
		MinCorrelation: 0.7,
		BridgeSeconds:  0.6,
		MinRunSeconds:  4.0,
		Offsets:        6,
	}
}

func (c levelMatchConfig) normalized() levelMatchConfig {
	def := defaultLevelMatchConfig()
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = def.WindowSeconds
	}
	if c.HopSeconds <= 0 {
		c.HopSeconds = def.HopSeconds
	}
	if c.MinCorrelation <= 0 {
		c.MinCorrelation = def.MinCorrelation
	}
	if c.BridgeSeconds < 0 {
		c.BridgeSeconds = def.BridgeSeconds
	}
	if c.MinRunSeconds <= 0 {
		c.MinRunSeconds = def.MinRunSeconds
	}
	if c.Offsets <= 0 {
		c.Offsets = def.Offsets
	}
	return c
}

// levelRun is one stretch two episodes share. The times are in each file's
// own media clock.
type levelRun struct {
	Start, End float64
	// Correlation is the mean similarity over the stretch, 0 to 1.
	Correlation float64
}

// sharedRuns returns the stretches of x that y carries too, as pairs carrying
// both files' times. It walks the strongest offsets of the pair's
// cross-correlation and keeps, for each stretch of x, the best offset that
// explains it.
func sharedRuns(x, y envelope, cfg levelMatchConfig) []levelPair {
	cfg = cfg.normalized()
	if x.len() < 2 || y.len() < 2 {
		return nil
	}
	var pairs []levelPair
	for _, offset := range candidateLevelOffsets(x, y, cfg) {
		// The offset search runs on a decimated trace, so its candidates sit
		// on a coarse grid: a true alignment between two grid points scores
		// badly there, and is found by walking the frames around each.
		for _, run := range runsAtOffset(x, y, refineLevelOffset(x, y, offset, cfg), cfg) {
			pair := levelPair{
				Start:        run.Start,
				End:          run.End,
				PartnerStart: run.Start + float64(offset)*x.hop,
				Correlation:  run.Correlation,
			}
			if pair.PartnerStart < 0 {
				continue
			}
			if other, ok := overlappingPair(pairs, pair); ok {
				// The better-aligned stretch wins over a longer one: a
				// quasi-periodic music cue correlates with itself at several
				// shifts, and the extra length those shifts find is not the
				// recording.
				if !betterPair(pair, other) {
					continue
				}
				pairs = replacePair(pairs, other, pair)
				continue
			}
			pairs = append(pairs, pair)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Start < pairs[j].Start })
	return pairs
}

// overlappingPair returns the pair already kept that shares half of pair's
// span.
func overlappingPair(pairs []levelPair, pair levelPair) (levelPair, bool) {
	for _, kept := range pairs {
		overlap := math.Min(kept.End, pair.End) - math.Max(kept.Start, pair.Start)
		shortest := math.Min(kept.End-kept.Start, pair.End-pair.Start)
		if overlap > 0 && overlap >= 0.5*shortest {
			return kept, true
		}
	}
	return levelPair{}, false
}

// refineLevelOffset returns the frame offset near coarse whose longest shared
// run is longest. A theme that starts half a coarse step off the grid
// correlates no better than chance at the grid point itself.
func refineLevelOffset(x, y envelope, coarse int, cfg levelMatchConfig) int {
	span := max(1, int(math.Round(coarseStepSeconds/x.hop)))
	best, bestFrames := coarse, -1
	for offset := coarse - span; offset <= coarse+span; offset++ {
		frames := 0
		for _, run := range runsAtOffset(x, y, offset, cfg) {
			frames = max(frames, int(math.Round((run.End-run.Start)/x.hop)))
		}
		if frames > bestFrames {
			best, bestFrames = offset, frames
		}
	}
	return best
}

// betterPair reports whether pair replaces kept: a clearer correlation wins
// outright, and a near-tie goes to the longer stretch.
func betterPair(pair, kept levelPair) bool {
	switch {
	case pair.Correlation > kept.Correlation+pairCorrelationTie:
		return true
	case kept.Correlation > pair.Correlation+pairCorrelationTie:
		return false
	default:
		return pair.End-pair.Start > kept.End-kept.Start
	}
}

// replacePair swaps old for replacement, keeping the slice order.
func replacePair(pairs []levelPair, old, replacement levelPair) []levelPair {
	for i, pair := range pairs {
		if pair == old {
			pairs[i] = replacement
		}
	}
	return pairs
}

// candidateLevelOffsets returns the offsets, in frames, that best explain the
// two files' shared audio.
//
// Scoring an offset by its whole overlap averages one shared theme over five
// minutes of unrelated episode and loses it in the noise; an intro is a short
// stretch, not a global alignment. Each offset is therefore scored by its best
// local window, which a shared theme raises to nearly one and unrelated audio
// leaves near its chance level. The search runs on a decimated copy of both
// traces, so a ten-minute opening costs a few million multiplications.
func candidateLevelOffsets(x, y envelope, cfg levelMatchConfig) []int {
	factor := max(2, int(math.Round(coarseStepSeconds/x.hop)))
	coarseX, coarseY := decimate(x.v, factor), decimate(y.v, factor)
	if len(coarseX) < 16 || len(coarseY) < 16 {
		return nil
	}
	// A coarse bin is coarseStepSeconds; the window is a couple of seconds,
	// long enough to hold a beat and short enough to catch one phrase.
	window := max(4, int(math.Round(coarseWindowSeconds/coarseStepSeconds)))
	score := longestSharedBins(coarseX, coarseY, window, cfg.MinCorrelation)

	type peak struct {
		offset int
		score  int
	}
	var peaks []peak
	// An offset that leaves only a handful of bins overlapping can correlate
	// perfectly by chance, so it is not evidence.
	shorter := min(len(coarseX), len(coarseY))
	minOverlap := max(2*window, shorter/10)
	for offset := -(len(coarseY) - 1); offset < len(coarseX); offset++ {
		from := max(0, -offset)
		if min(len(coarseX), len(coarseY)-offset)-from < minOverlap {
			continue
		}
		if value, ok := score(offset); ok && value > 0 {
			peaks = append(peaks, peak{offset: offset * factor, score: value})
		}
	}
	if len(peaks) == 0 {
		return nil
	}
	sort.SliceStable(peaks, func(i, j int) bool { return peaks[i].score > peaks[j].score })

	// Peaks within one window of a kept one describe the same alignment and
	// would only repeat the same run.
	neighborhood := window * factor
	offsets := make([]int, 0, cfg.Offsets)
	for _, p := range peaks {
		if len(offsets) >= cfg.Offsets {
			break
		}
		duplicate := false
		for _, kept := range offsets {
			if absInt(p.offset-kept) < neighborhood {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		offsets = append(offsets, p.offset)
	}
	return offsets
}

// longestSharedBins returns a function giving the longest stretch, in coarse
// bins, over which an offset's windows correlate at least minCorrelation. A
// shared theme scores its own length; unrelated audio only ever lines up for a
// window or two by chance, however short the overlap. The prefix sums of each
// side are built once; only the products, which move with the offset, are
// summed per offset.
func longestSharedBins(x, y []float64, window int, minCorrelation float64) func(offset int) (int, bool) {
	px, px2 := prefixSums(x), prefixSumsOfSquares(x)
	py, py2 := prefixSums(y), prefixSumsOfSquares(y)
	products := make([]float64, len(x))
	xy := make([]float64, len(x)+1)
	return func(offset int) (int, bool) {
		from := max(0, -offset)
		to := min(len(x), len(y)-offset)
		if to-from <= window {
			return 0, false
		}
		n := to - from
		if cap(products) < n {
			products = make([]float64, n)
			xy = make([]float64, n+1)
		}
		products, xy = products[:n], xy[:n+1]
		xy[0] = 0
		for i := 0; i < n; i++ {
			products[i] = x[from+i] * y[from+i+offset]
			xy[i+1] = xy[i] + products[i]
		}
		best, run := 0, 0
		for start := 0; start+window <= n; start++ {
			value := pearson(
				xy[start+window]-xy[start],
				px2[from+start+window]-px2[from+start],
				py2[from+start+offset+window]-py2[from+start+offset],
				px[from+start+window]-px[from+start],
				py[from+start+offset+window]-py[from+start+offset],
				float64(window),
			)
			if value < minCorrelation {
				run = 0
				continue
			}
			run++
			if run > best {
				best = run
			}
		}
		// The run is scored in windows; a stretch of n windows covers n-1+window
		// bins, which is what the top offsets are ordered by.
		if best == 0 {
			return 0, true
		}
		return best + window - 1, true
	}
}

// pearson turns the sums of a window into the two traces' Pearson correlation.
func pearson(sumXY, sumXX, sumYY, sumX, sumY, n float64) float64 {
	covariance := sumXY - sumX*sumY/n
	varianceX := sumXX - sumX*sumX/n
	varianceY := sumYY - sumY*sumY/n
	if varianceX <= 1e-9 || varianceY <= 1e-9 {
		return 0
	}
	return covariance / math.Sqrt(varianceX*varianceY)
}

// prefixSumsOfSquares returns the running sums of the values' squares.
func prefixSumsOfSquares(values []float64) []float64 {
	sums := make([]float64, len(values)+1)
	for i, v := range values {
		sums[i+1] = sums[i] + v*v
	}
	return sums
}

// decimate averages each group of factor frames, so the coarse search sees the
// same shape at a tenth of the resolution.
func decimate(values []float64, factor int) []float64 {
	if factor <= 1 {
		return values
	}
	out := make([]float64, 0, len(values)/factor+1)
	for i := 0; i < len(values); i += factor {
		end := min(i+factor, len(values))
		var sum float64
		for _, v := range values[i:end] {
			sum += v
		}
		out = append(out, sum/float64(end-i))
	}
	return out
}

// overlapCorrelation is the Pearson correlation of the overlapping frames of
// two normalized traces at offset. It is the coarse score: the same stretch
// anywhere in the window raises it, and unrelated audio averages it to zero.
func overlapCorrelation(x, y []float64, offset int) float64 {
	from := max(0, -offset)
	to := min(len(x), len(y)-offset)
	if to-from < 8 {
		return math.NaN()
	}
	var sumXY, sumXX, sumYY float64
	for i := from; i < to; i++ {
		a, b := x[i], y[i+offset]
		sumXY += a * b
		sumXX += a * a
		sumYY += b * b
	}
	if sumXX <= 1e-9 || sumYY <= 1e-9 {
		return math.NaN()
	}
	return sumXY / math.Sqrt(sumXX*sumYY)
}

// runsAtOffset walks one offset with a sliding window and returns the
// stretches where the two agree, bridging gaps no longer than BridgeSeconds.
// Everything is counted in frames: a scored position is the frame its window
// starts at.
//
// Each window is correlated against its own mean, not the trace's, because a
// stretch of digital silence is a constant in the file's frame and would
// otherwise correlate perfectly with any other silence.
func runsAtOffset(x, y envelope, offset int, cfg levelMatchConfig) []levelRun {
	window := max(1, int(math.Round(cfg.WindowSeconds/x.hop)))
	hop := max(1, int(math.Round(cfg.HopSeconds/x.hop)))
	bridge := max(1, int(math.Round(cfg.BridgeSeconds/x.hop)))
	minRun := max(1, int(math.Round(cfg.MinRunSeconds/x.hop)))

	from := max(0, -offset)
	to := min(x.len(), y.len()-offset)
	if to-from < window {
		return nil
	}
	products := make([]float64, to-from)
	xx := make([]float64, to-from)
	yy := make([]float64, to-from)
	xs := make([]float64, to-from)
	ys := make([]float64, to-from)
	for i := from; i < to; i++ {
		a, b := x.v[i], y.v[i+offset]
		products[i-from] = a * b
		xx[i-from] = a * a
		yy[i-from] = b * b
		xs[i-from] = a
		ys[i-from] = b
	}
	windowed := windowSums{
		products: prefixSums(products),
		squaresX: prefixSums(xx),
		squaresY: prefixSums(yy),
		sumsX:    prefixSums(xs),
		sumsY:    prefixSums(ys),
	}

	scored := make([]float64, len(products)-window+1)
	for k := range scored {
		scored[k] = windowed.correlation(k, window)
	}

	var runs []levelRun
	runStart, lastHit := -1, -1
	flush := func() {
		if runStart < 0 || lastHit < 0 {
			return
		}
		start := from + runStart
		end := from + lastHit + window
		if end-start < minRun {
			return
		}
		runs = append(runs, levelRun{
			Start:       float64(start) * x.hop,
			End:         float64(end) * x.hop,
			Correlation: meanPositive(scored[runStart : lastHit+1]),
		})
	}
	for k := 0; k < len(scored); k += hop {
		if scored[k] < cfg.MinCorrelation {
			continue
		}
		if runStart >= 0 && k-lastHit > bridge {
			flush()
			runStart = -1
		}
		if runStart < 0 {
			runStart = k
		}
		lastHit = k
	}
	flush()
	return runs
}

// windowSums are the prefix sums a Pearson correlation over a window needs.
type windowSums struct {
	products, squaresX, squaresY, sumsX, sumsY []float64
}

// correlation is the Pearson correlation of the two traces over window frames
// starting at from.
func (w windowSums) correlation(from, window int) float64 {
	n := float64(window)
	sumXY := w.products[from+window] - w.products[from]
	sumX := w.sumsX[from+window] - w.sumsX[from]
	sumY := w.sumsY[from+window] - w.sumsY[from]
	sumXX := w.squaresX[from+window] - w.squaresX[from]
	sumYY := w.squaresY[from+window] - w.squaresY[from]
	covariance := sumXY - sumX*sumY/n
	varianceX := sumXX - sumX*sumX/n
	varianceY := sumYY - sumY*sumY/n
	if varianceX <= 1e-9 || varianceY <= 1e-9 {
		return 0
	}
	return covariance / math.Sqrt(varianceX*varianceY)
}

// prefixSums returns the running sums of values, each one longer than values so
// a range sum is sums[to]-sums[from].
func prefixSums(values []float64) []float64 {
	sums := make([]float64, len(values)+1)
	for i, v := range values {
		sums[i+1] = sums[i] + v
	}
	return sums
}

// meanPositive is the mean of the values above zero, or zero.
func meanPositive(values []float64) float64 {
	var sum float64
	count := 0
	for _, v := range values {
		if v > 0 {
			sum += v
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
