// Package subsync aligns a stored subtitle to its media file's audio. It
// compares where the subtitle shows text with where the audio carries speech
// and finds the timing correction (a framerate scale and an offset) that
// lines them up, or reports that nothing does: the subtitle most likely
// belongs to a different release or title.
//
// See docs/architecture/subtitle-sync.md.
package subsync

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// Tuning. Every constant that shapes a decision lives here.
const (
	// frameSeconds is the resolution of both signals; it matches
	// mediasample.SpeechFrameSeconds.
	frameSeconds = mediasample.SpeechFrameSeconds
	// coarseFactor frames make one frame of the coarse search.
	coarseFactor = 4
	// maxOffsetSeconds bounds the offset searched for in each window.
	maxOffsetSeconds = 600
	// minCueSeconds is how much subtitle text a window's search range needs
	// before its lag means anything.
	minCueSeconds = 10
	// inlierSeconds is how close two windows' offsets must be to agree. Real
	// cue timing jitters by a frame or two around the speech it covers.
	inlierSeconds = 0.25
	// minInliers windows must agree, and at least minInlierShare of the
	// windows that produced a lag. A single window's best lag says little on
	// its own: on real audio a correct lag often stands only a few standard
	// deviations above the rest, no more than a wrong one. Agreement is what
	// separates them: wrong lags spread over the whole ±maxOffsetSeconds
	// range, so three of a dozen sharing a line by chance is rare, while
	// correct ones line up. On real files, subtitles of another title got at
	// most two agreeing windows; right ones at least three, even from a
	// different cut of the film where many windows disagree.
	minInliers     = 3
	minInlierShare = 0.25
	// minProminence is the least mean prominence the agreeing windows need:
	// how far, in standard deviations, their lags stand above the other lags
	// in their search range. Right subtitles averaged 4.1 and more on real
	// files; the rare chance agreement of a wrong one, about 3.
	minProminence = 3.75
	// minJointProminence is the least prominence the joint search's summed
	// evidence needs, and minJointProminenceFew the least for a file with
	// fewer than jointFewWindows windows, whose sums have heavier tails.
	// Measured on 2,708 production sidecars: right subtitles the window
	// search missed reached 6 and more; another episode of the same series
	// (shared music and credits, short episodes) reached 6.97 with seven
	// windows and under 6 with eight or more.
	minJointProminence    = 6.0
	minJointProminenceFew = 7.0
	jointFewWindows       = 8
	// minJointAgreement of the windows (of at least jointFewWindows) lining
	// up within inlierSeconds is evidence of its own, enough with a joint
	// prominence of minJointProminenceAgreed: right subtitles under a laugh
	// track or score reached 11 or 12 of 12 windows at a prominence of 4 to
	// 5; another episode at most 10 of 12.
	minJointAgreement        = 0.9
	minJointProminenceAgreed = 4.0
	// prominenceExclusionSeconds around a lag is left out when measuring its
	// prominence: a cue map's correlation peak is about that wide.
	prominenceExclusionSeconds = 2.0
	// alreadySyncedSeconds: a correction that moves no cue by more than
	// this over the runtime is no correction. Against subtitles whose timing
	// is known, alignment of real audio lands within about this much: cues
	// are spotted a little ahead of speech and linger after it.
	alreadySyncedSeconds = 0.15
	// jointPoolSeconds is how far each window's lag curve is widened (a
	// running maximum) before the joint search sums the curves, so a drift
	// between two points of its drift grid still lines the windows up.
	jointPoolSeconds = 0.12
	// localSearchSeconds around the joint line is searched again, frame by
	// frame, for each window's own lag.
	localSearchSeconds = 1.0
	// maxJointDriftSteps bounds the joint search's drift grid each way.
	maxJointDriftSteps = 100
	// maxDrift bounds the residual drift fitted on top of a framerate ratio,
	// in seconds of offset per second of media. Releases cut from different
	// masters can drift by a few hundredths of a percent without any
	// standard framerate conversion between them.
	maxDrift = 0.002
	// maxCueDuration is the longest cue that takes part in alignment.
	maxCueDuration = time.Minute
	// refineSeconds around the fitted offset is searched again with every
	// agreeing window's correlation summed, for the final offset.
	refineSeconds = 0.5
)

// scaleCandidates are the framerate conversions subtitles are commonly cut
// for: a 25 fps PAL release, 24 fps, and 23.976 fps NTSC film, each way.
var scaleCandidates = []float64{
	1,
	25 / 23.976, 23.976 / 25,
	25.0 / 24, 24.0 / 25,
	24 / 23.976, 23.976 / 24,
}

// Status is the outcome of an alignment.
type Status string

const (
	// StatusSynced found a correction that differs from the current one.
	StatusSynced Status = "synced"
	// StatusAlreadySynced found the current correction is right.
	StatusAlreadySynced Status = "already_synced"
	// StatusNoMatch found no correction the audio agrees with.
	StatusNoMatch Status = "no_match"
)

// ErrNoCues reports a subtitle with no timed text to align.
var ErrNoCues = errors.New("subtitle has no cues to align")

// ErrNoSpeech reports audio that gave nothing to align against.
var ErrNoSpeech = errors.New("no speech levels to align against")

// Alignment is the best correction found and how well the audio supports it.
type Alignment struct {
	// Timing maps the subtitle's original times to media times.
	Timing subtitles.Timing
	// Windows is how many speech windows produced a confident lag, and
	// Inliers how many of them agree with Timing.
	Windows int
	Inliers int
	// Confidence is Inliers / Windows, zero when no window produced a lag.
	Confidence float64
	// Prominence is the agreeing windows' mean prominence.
	Prominence float64
	// JointProminence is set when the joint search found Timing: how far,
	// in standard deviations, the windows' summed lag curves stand above
	// their sums at other offsets along the best line.
	JointProminence float64
}

// Matched reports whether the audio supports Timing: enough windows agree
// on their own; or, from the joint search, enough agree and the summed
// evidence stands out, or nearly every window agrees.
func (a Alignment) Matched() bool {
	if a.Inliers < minInliers {
		return false
	}
	if a.JointProminence > 0 {
		if a.Windows < jointFewWindows {
			return a.JointProminence >= minJointProminenceFew
		}
		return a.JointProminence >= minJointProminence ||
			(a.Confidence >= minJointAgreement && a.JointProminence >= minJointProminenceAgreed)
	}
	return a.Confidence >= minInlierShare && a.Prominence >= minProminence
}

// Decide returns the status of a against the subtitle's current correction,
// for a file of the given runtime.
func Decide(a Alignment, current subtitles.Timing, runtime time.Duration) Status {
	if !a.Matched() {
		return StatusNoMatch
	}
	limit := time.Duration(alreadySyncedSeconds * float64(time.Second))
	for _, at := range []time.Duration{0, runtime} {
		if diff := a.Timing.Apply(at) - current.Apply(at); diff > limit || diff < -limit {
			return StatusSynced
		}
	}
	return StatusAlreadySynced
}

// windowLag is one window's best offset: media time = subtitle time +
// OffsetSeconds near media time At, under the scale being tried.
type windowLag struct {
	At            float64
	OffsetSeconds float64
	Score         float64
	// Prominence is how many standard deviations the window's best lag
	// stands above its other lags.
	Prominence float64
	signal     *speechSignal
}

// Align finds the correction that lines cues (original subtitle timing) up
// with the speech in windows. Each window's own best lag is tried first;
// when too few agree, the joint search sums every window's evidence before
// choosing a lag, which finds subtitles whose windows are each too weak to
// stand out (dialog under a laugh track or a score).
func Align(windows []mediasample.SpeechLevels, cues []subtitles.SubtitleCue) (Alignment, error) {
	signals, cues, search, err := prepare(windows, cues)
	if err != nil {
		return Alignment{}, err
	}
	best := alignWindows(signals, cues, search)
	if best.Matched() {
		return best, nil
	}
	if joint, ok := alignJoint(signals, cues, search); ok && joint.Matched() {
		return joint, nil
	}
	return best, nil
}

// prepare turns windows into speech signals and drops the cues none of
// them can reach, with a searcher sized for the longest window.
func prepare(windows []mediasample.SpeechLevels, cues []subtitles.SubtitleCue) ([]*speechSignal, []subtitles.SubtitleCue, *searcher, error) {
	if len(cues) == 0 {
		return nil, nil, nil, ErrNoCues
	}
	signals := make([]*speechSignal, 0, len(windows))
	longest := 0
	for _, w := range windows {
		if s, ok := newSpeechSignal(w); ok {
			signals = append(signals, s)
			longest = max(longest, len(s.coarse))
		}
	}
	if len(signals) == 0 {
		return nil, nil, nil, ErrNoSpeech
	}
	cues = cuesInReach(cues, signals)
	if len(cues) == 0 {
		return nil, nil, nil, ErrNoCues
	}
	return signals, cues, newSearcher(longest), nil
}

// alignWindows fits a line through the windows' own best lags.
func alignWindows(signals []*speechSignal, cues []subtitles.SubtitleCue, search *searcher) Alignment {
	var best Alignment
	var bestFit lineFit
	var bestMap cueMap
	var bestScale float64
	for _, scale := range scaleCandidates {
		cues := newCueMap(cues, scale)
		var lags []windowLag
		for _, s := range signals {
			if lag, ok := search.bestLag(s, cues); ok {
				lags = append(lags, lag)
			}
		}
		fit := fitLine(lags)
		// Neighboring ratios differ by less than maxDrift, so one timing can
		// fit under two of them; prefer the one that needs no drift.
		if better(fit, len(fit.Group), best.Inliers, bestFit) {
			best = Alignment{Windows: len(lags), Inliers: len(fit.Group)}
			bestFit, bestMap, bestScale = fit, cues, scale
		}
	}
	best.finish(bestFit, bestMap, bestScale)
	return best
}

// finish sets a's timing from fit, found on the cue map m of scale, and the
// agreeing windows' mean prominence and share. a carries Windows and Inliers.
func (a *Alignment) finish(fit lineFit, m cueMap, scale float64) {
	if len(fit.Group) > 0 {
		for _, lag := range fit.Group {
			a.Prominence += lag.Prominence / float64(len(fit.Group))
		}
		fit.Offset += refine(fit, m)
		// On the scaled clock, media = scale*t + offset + drift*media.
		k := 1 / (1 - fit.Drift)
		scale, offset := snapScale(scale*k, fit.Offset*k, fit.Group)
		a.Timing = subtitles.Timing{Scale: scale, OffsetMS: int(math.Round(offset * 1000))}
	}
	if a.Windows > 0 {
		a.Confidence = float64(a.Inliers) / float64(a.Windows)
	}
	if a.Timing.Scale == 1 {
		a.Timing.Scale = 0
	}
}

// cuesInReach drops cues no window can pair with: those starting past the
// last window's end plus the largest offset searched, under the slowest
// ratio tried, and those longer than any spoken line, which say nothing
// about where speech is. Both bound the cue map a corrupt or hostile
// timestamp (thousands of hours) would otherwise allocate.
func cuesInReach(cues []subtitles.SubtitleCue, signals []*speechSignal) []subtitles.SubtitleCue {
	end := 0.0
	for _, s := range signals {
		end = max(end, float64(s.startFrame+len(s.fine))*frameSeconds)
	}
	limit := time.Duration((end + maxOffsetSeconds) / slices.Min(scaleCandidates) * float64(time.Second))
	kept := make([]subtitles.SubtitleCue, 0, len(cues))
	for _, c := range cues {
		if c.Start >= limit || c.End-c.Start > maxCueDuration {
			continue
		}
		c.End = min(c.End, limit)
		kept = append(kept, c)
	}
	return kept
}

// better reports whether fit, with inliers agreeing windows, beats the best
// so far: more agreeing windows, then less drift, then a higher score.
func better(fit lineFit, inliers, bestInliers int, best lineFit) bool {
	if inliers != bestInliers {
		return inliers > bestInliers
	}
	if d, bd := math.Abs(fit.Drift), math.Abs(best.Drift); d != bd {
		return d < bd
	}
	return fit.Score > best.Score
}

// snapScale replaces a fitted scale with a standard ratio when the agreeing
// windows cannot tell the two apart, keeping the timing at their mean time.
func snapScale(scale, offset float64, group []windowLag) (float64, float64) {
	var mean, span float64
	for _, lag := range group {
		mean += lag.At
		span = max(span, lag.At)
	}
	mean /= float64(len(group))
	for _, ratio := range scaleCandidates {
		if math.Abs(scale-ratio)*span < inlierSeconds/2 {
			return ratio, offset + (scale-ratio)*mean
		}
	}
	return scale, offset
}

// lineFit is the offset most windows agree on, as a line over media time:
// offset(t) = Offset + Drift*t. Group holds the agreeing windows.
type lineFit struct {
	Offset float64
	Drift  float64
	Group  []windowLag
	Score  float64
}

func (f lineFit) at(t float64) float64 { return f.Offset + f.Drift*t }

// fitLine finds the line through the most window offsets (within
// inlierSeconds), trying every window alone (no drift) and every pair whose
// slope stays within maxDrift, then fits the agreeing windows by least
// squares. Wrong lags spread over the whole search range, so they rarely
// share a line; right ones do.
func fitLine(lags []windowLag) lineFit {
	var best lineFit
	consider := func(offset, drift float64) {
		candidate := lineFit{Offset: offset, Drift: drift}
		for _, lag := range lags {
			if math.Abs(lag.OffsetSeconds-candidate.at(lag.At)) <= inlierSeconds {
				candidate.Group = append(candidate.Group, lag)
				candidate.Score += lag.Score
			}
		}
		switch {
		case len(candidate.Group) > len(best.Group),
			len(candidate.Group) == len(best.Group) && candidate.Score > best.Score:
			best = candidate
		}
	}
	for i, a := range lags {
		consider(a.OffsetSeconds, 0)
		for _, b := range lags[i+1:] {
			if dt := b.At - a.At; math.Abs(dt) >= 1 {
				if drift := (b.OffsetSeconds - a.OffsetSeconds) / dt; math.Abs(drift) <= maxDrift {
					consider(a.OffsetSeconds-drift*a.At, drift)
				}
			}
		}
	}
	if len(best.Group) == 0 {
		return best
	}
	// Least squares over the group. Drift is dropped when the group's time
	// span cannot distinguish it from window noise, or a constant offset
	// already explains every agreeing window.
	var mt, mo float64
	for _, lag := range best.Group {
		mt += lag.At
		mo += lag.OffsetSeconds
	}
	n := float64(len(best.Group))
	mt, mo = mt/n, mo/n
	var stt, sto, lo, hi float64
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, lag := range best.Group {
		stt += (lag.At - mt) * (lag.At - mt)
		sto += (lag.At - mt) * (lag.OffsetSeconds - mo)
		lo, hi = min(lo, lag.At), max(hi, lag.At)
	}
	best.Drift = 0
	if stt > 0 {
		best.Drift = min(max(sto/stt, -maxDrift), maxDrift)
	}
	if math.Abs(best.Drift)*(hi-lo) < inlierSeconds/2 || within(best.Group, mo) {
		best.Drift = 0
	}
	best.Offset = mo - best.Drift*mt
	return best
}

// within reports whether a constant offset explains every lag, so a fitted
// drift would only be following their noise.
func within(group []windowLag, offset float64) bool {
	for _, lag := range group {
		if math.Abs(lag.OffsetSeconds-offset) > inlierSeconds {
			return false
		}
	}
	return true
}

// refine returns the shift, within refineSeconds, at which the agreeing
// windows correlate best together. Each window's lag alone jitters by a frame
// or more; their summed correlation peaks where the subtitle belongs.
func refine(fit lineFit, m cueMap) float64 {
	span := int(refineSeconds / frameSeconds)
	total := make([]float64, 2*span+1)
	for _, lag := range fit.Group {
		s := lag.signal
		base := toFrame(fit.at(lag.At)) // media frame = cue frame + base
		// y[i+j] is the cue frame paired with media frame start+i at a
		// correction of base+span-j frames.
		y := m.sliceFine(s.startFrame-base-span, len(s.fine)+2*span)
		for j, v := range normalizedCorrelation(s.fine, y, crossCorrelate(s.fine, y)) {
			total[j] += v
		}
	}
	return float64(span-argmax(total)) * frameSeconds
}

// cueMap marks, on the scaled subtitle clock, whether a cue is showing:
// fine holds 0 or 1 per frame, coarse the share of each coarse frame.
type cueMap struct {
	fine   []byte
	coarse []float64
}

func newCueMap(cues []subtitles.SubtitleCue, scale float64) cueMap {
	end := 0
	for _, c := range cues {
		end = max(end, toFrame(c.End.Seconds()*scale)+1)
	}
	// Each frame is written at most once however long or overlapping the
	// cues are: the cue spans are sorted and only their union is filled,
	// so a subtitle of many long overlapping cues costs no more than the
	// map's length.
	spans := make([][2]int, 0, len(cues))
	for _, c := range cues {
		from, to := max(toFrame(c.Start.Seconds()*scale), 0), min(toFrame(c.End.Seconds()*scale), end)
		if from < to {
			spans = append(spans, [2]int{from, to})
		}
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	fine := make([]byte, end)
	filled := 0 // every frame before filled is written
	for _, span := range spans {
		for i := max(span[0], filled); i < span[1]; i++ {
			fine[i] = 1
		}
		filled = max(filled, span[1])
	}
	coarse := make([]float64, (end+coarseFactor-1)/coarseFactor)
	for i, v := range fine {
		coarse[i/coarseFactor] += float64(v) / coarseFactor
	}
	return cueMap{fine: fine, coarse: coarse}
}

// sliceCoarse writes coarse frames [from, from+len(out)) to out, zero
// outside the map.
func (m cueMap) sliceCoarse(from int, out []float64) {
	clear(out)
	lo, hi := max(from, 0), min(from+len(out), len(m.coarse))
	if lo < hi {
		copy(out[lo-from:], m.coarse[lo:hi])
	}
}

// sliceFine returns frames [from, from+n), zero outside the map.
func (m cueMap) sliceFine(from, n int) []float64 {
	out := make([]float64, n)
	for i := max(from, 0); i < min(from+n, len(m.fine)); i++ {
		out[i-from] = float64(m.fine[i])
	}
	return out
}

func toFrame(seconds float64) int { return int(math.Round(seconds / frameSeconds)) }

// speechSignal is one window's speech levels, clipped to the window's
// dynamic range and centered, at full and coarse resolution. corr caches the
// coarse signal's transform across the scales tried.
type speechSignal struct {
	startFrame int
	fine       []float64
	coarse     []float64
	corr       *correlator
}

// middle is the media time at the middle of the window.
func (s *speechSignal) middle() float64 {
	return (float64(s.startFrame) + float64(len(s.fine))/2) * frameSeconds
}

func newSpeechSignal(w mediasample.SpeechLevels) (*speechSignal, bool) {
	if len(w.Levels) < 10*coarseFactor || w.FrameSeconds != frameSeconds {
		return nil, false
	}
	sorted := slices.Clone(w.Levels)
	slices.Sort(sorted)
	floor := float64(sorted[len(sorted)*20/100])
	ceiling := float64(sorted[len(sorted)*98/100])
	if ceiling-floor < 3 {
		return nil, false // no dynamics: silence or a constant hum
	}
	fine := make([]float64, len(w.Levels))
	for i, level := range w.Levels {
		fine[i] = min(max(float64(level), floor), ceiling) - floor
	}
	center(fine)
	return &speechSignal{startFrame: toFrame(w.StartSeconds), fine: fine, coarse: center(downsample(fine))}, true
}

// downsample averages each coarseFactor frames into one.
func downsample(x []float64) []float64 {
	out := make([]float64, len(x)/coarseFactor)
	for i := range out {
		sum := 0.0
		for _, v := range x[i*coarseFactor : (i+1)*coarseFactor] {
			sum += v
		}
		out[i] = sum / coarseFactor
	}
	return out
}

func center(x []float64) []float64 {
	mean := 0.0
	for _, v := range x {
		mean += v
	}
	mean /= float64(len(x))
	for i := range x {
		x[i] -= mean
	}
	return x
}

// searcher finds windows' best lags, reusing one transform plan and its
// buffers across every window and scale.
type searcher struct {
	coarseRange int
	plan        *fftPlan
	y, raw, ncc []float64
	sum, sumSq  []float64
}

func newSearcher(longestCoarse int) *searcher {
	coarseRange := int(maxOffsetSeconds / (frameSeconds * coarseFactor))
	return &searcher{coarseRange: coarseRange, plan: newFFTPlan(nextPow2(longestCoarse + 2*coarseRange))}
}

// bestLag searches offsets within maxOffsetSeconds: coarsely over the whole
// range, then frame by frame around the coarse peak.
func (r *searcher) bestLag(s *speechSignal, m cueMap) (windowLag, bool) {
	n := len(s.coarse)
	if s.corr == nil {
		s.corr = newCorrelator(r.plan, s.coarse)
	}
	// y[k+i] pairs speech frame i with subtitle frame start+i+k-range.
	r.y = resize(r.y, n+2*r.coarseRange)
	m.sliceCoarse(s.startFrame/coarseFactor-r.coarseRange, r.y)
	r.raw = s.corr.correlate(r.y, r.raw)
	ncc := r.normalize(s.coarse, r.y, r.raw)
	if ncc == nil {
		return windowLag{}, false
	}
	peak := argmax(ncc)
	if !hasEnoughCues(r.y[peak:peak+n], minCueSeconds/(frameSeconds*coarseFactor)) {
		return windowLag{}, false
	}
	prom := prominence(ncc, peak, int(prominenceExclusionSeconds/(frameSeconds*coarseFactor)))

	// Refine at full resolution around the coarse peak.
	coarseLag := (peak-r.coarseRange)*coarseFactor - s.startFrame%coarseFactor
	from := s.startFrame + coarseLag - coarseFactor
	fineY := m.sliceFine(from, len(s.fine)+2*coarseFactor)
	fine := normalizedCorrelation(s.fine, fineY, crossCorrelate(s.fine, fineY))
	if fine == nil {
		return windowLag{}, false
	}
	best := argmax(fine)
	lagFrames := coarseLag - coarseFactor + best
	// Subtitle frame start+i+lag shows the speech at media frame start+i, so
	// media time = subtitle time - lag.
	return windowLag{At: s.middle(), OffsetSeconds: -float64(lagFrames) * frameSeconds, Score: fine[best], Prominence: prom, signal: s}, true
}

func (r *searcher) normalize(x, y, raw []float64) []float64 {
	r.sum = resize(r.sum, len(y)+1)
	r.sumSq = resize(r.sumSq, len(y)+1)
	r.ncc = resize(r.ncc, len(raw))
	return normalizeInto(x, y, raw, r.sum, r.sumSq, r.ncc)
}

func resize(buf []float64, n int) []float64 {
	if cap(buf) < n {
		return make([]float64, n)
	}
	return buf[:n]
}

// normalizedCorrelation turns raw[k] = sum_i x[i]*y[i+k], x already centered,
// into the Pearson correlation of x with y[k : k+len(x)].
func normalizedCorrelation(x, y, raw []float64) []float64 {
	return normalizeInto(x, y, raw, make([]float64, len(y)+1), make([]float64, len(y)+1), make([]float64, len(raw)))
}

func normalizeInto(x, y, raw, sum, sumSq, out []float64) []float64 {
	if raw == nil {
		return nil
	}
	xx := 0.0
	for _, v := range x {
		xx += v * v
	}
	if xx == 0 {
		return nil
	}
	n := len(x)
	sum[0], sumSq[0] = 0, 0
	for i, v := range y {
		sum[i+1] = sum[i] + v
		sumSq[i+1] = sumSq[i] + v*v
	}
	for k := range raw {
		s := sum[k+n] - sum[k]
		variance := sumSq[k+n] - sumSq[k] - s*s/float64(n)
		if variance <= 1e-9 {
			out[k] = 0
			continue
		}
		out[k] = raw[k] / math.Sqrt(xx*variance)
	}
	return out
}

func hasEnoughCues(y []float64, frames float64) bool {
	sum := 0.0
	for _, v := range y {
		sum += v
	}
	return sum >= frames
}

func argmax(x []float64) int {
	best := 0
	for i, v := range x {
		if v > x[best] {
			best = i
		}
	}
	return best
}

// prominence is how many standard deviations x[peak] stands above the values
// more than exclusion away from it.
func prominence(x []float64, peak, exclusion int) float64 {
	var sum, sumSq float64
	count := 0
	for i, v := range x {
		if i >= peak-exclusion && i <= peak+exclusion {
			continue
		}
		sum += v
		sumSq += v * v
		count++
	}
	if count < 2 {
		return 0
	}
	mean := sum / float64(count)
	std := math.Sqrt(max(sumSq/float64(count)-mean*mean, 0))
	if std == 0 {
		return 0
	}
	return (x[peak] - mean) / std
}
