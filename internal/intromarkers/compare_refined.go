package intromarkers

import (
	"math"
	"math/bits"
	"sort"
)

// Intro matching (matchProfile.RefinedMatch) times points by libchromaprint's
// frame step and aligns two files to a single point. Credits matching keeps
// DefaultPointHopSeconds and the coarser alignment in compare.go.
//
// The leads move a match's edges back onto the audio it covers: a point
// summarizes the audio after its timestamp, so a shared stretch starts
// matching before it begins. They were calibrated against authored intro
// chapters.
const (
	exactChromaprintHopSeconds = 1365.0 / 11025.0

	introStartLeadSeconds     = 0.90
	introEndLeadSeconds       = -0.20
	introZeroStartSnapSeconds = 1.5
)

// Pair-matching and consensus parameters, tuned in replay against authored
// intro chapters.
const (
	introCandidateShiftLimit    = 10
	introShiftDedupPoints       = 4
	introMatchTolerancePoints   = 1
	introMaxGapSeconds          = 3.5
	introHitBits                = 7
	introBitThreshold           = 7.5
	introEdgeBitThreshold       = 5
	introMinHitDensity          = 0.45
	introPairMinSlackSeconds    = 0.5
	introClusterEdgeTolSeconds  = 4.0
	introLogoStartLimitSeconds  = 10.0
	introLogoMaxDurationSeconds = 28.0
	introLogoMinAdvantageSec    = 8.0
	introShortQualitySeconds    = 9.5
	introMinimumShortQuality    = 3.0
)

type pairMatch struct {
	Left    Segment
	Right   Segment
	Quality float64
}

// comparePairRefined finds the stretches of audio left and right share. Each
// candidate alignment is refined to a single point, and every run at it is
// kept, so a logo and a theme the two files share at different offsets both
// come back.
func comparePairRefined(left, right []uint32, minPairSeconds, maxSeconds float64) []pairMatch {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	step := max(1, int(math.Round(1.0/exactChromaprintHopSeconds)))
	coarse := candidateShiftsWithLimit(left, right, step, introCandidateShiftLimit)
	shifts := make([]int, 0, len(coarse))
	for _, cs := range coarse {
		sh := refineShift(left, right, cs, step)
		dup := false
		for _, prev := range shifts {
			if absInt(sh-prev) <= introShiftDedupPoints {
				dup = true
				break
			}
		}
		if !dup {
			shifts = append(shifts, sh)
		}
	}

	minPts := max(10, int(math.Floor((minPairSeconds-1.5)/exactChromaprintHopSeconds)))
	maxGapPts := int(math.Ceil(introMaxGapSeconds / exactChromaprintHopSeconds))
	var all []pairMatch
	for _, sh := range shifts {
		for _, run := range findRefinedRunsAtShift(left, right, sh, minPts, maxGapPts, minPairSeconds, maxSeconds) {
			all = mergePairMatch(all, run)
		}
	}
	return all
}

// refineShift returns the alignment within span points of coarse at which the
// most points of left and right agree.
func refineShift(left, right []uint32, coarse, span int) int {
	bestSh, bestHits := coarse, -1
	for sh := coarse - span; sh <= coarse+span; sh++ {
		from := max(0, -sh)
		to := min(len(left), len(right)-sh)
		hits := 0
		for i := from; i < to; i += 2 {
			if bits.OnesCount32(left[i]^right[i+sh]) <= 6 {
				hits++
			}
		}
		if hits > bestHits {
			bestHits = hits
			bestSh = sh
		}
	}
	return bestSh
}

// findRefinedRunsAtShift returns the runs left and right share at one
// alignment. A run is a stretch of agreeing points with no gap longer than
// maxGapPts.
func findRefinedRunsAtShift(left, right []uint32, shift, minPts, maxGapPts int, minPairSeconds, maxSeconds float64) []pairMatch {
	from := max(0, -shift)
	to := min(len(left), len(right)-shift)
	if to-from < 20 {
		return nil
	}
	n := to - from
	dists := make([]int, n)
	for k := 0; k < n; k++ {
		i := from + k
		j0 := i + shift
		lo := max(0, j0-introMatchTolerancePoints)
		hi := min(len(right)-1, j0+introMatchTolerancePoints)
		bestD := 32
		a := left[i]
		for j := lo; j <= hi; j++ {
			if d := bits.OnesCount32(a ^ right[j]); d < bestD {
				bestD = d
			}
		}
		dists[k] = bestD
	}

	var matches []pairMatch
	k := 0
	for k < n {
		for k < n && dists[k] > introHitBits {
			k++
		}
		if k >= n {
			break
		}
		blockStart := k
		lastHit := k
		for k < n {
			if dists[k] <= introHitBits {
				lastHit = k
				k++
				continue
			}
			if k-lastHit > maxGapPts {
				break
			}
			k++
		}
		if m, ok := buildRefinedRun(dists, from, shift, blockStart, lastHit, minPts, minPairSeconds, maxSeconds); ok {
			matches = append(matches, m)
		}
	}
	return matches
}

// buildRefinedRun reduces a block of agreeing points to its best-scoring
// stretch (each point scores by how many bits agree, so a stretch stops
// growing where the points stop agreeing), trims weak points off both edges,
// and converts it to seconds in both files.
func buildRefinedRun(dists []int, from, shift, blockStart, lastHit, minPts int, minPairSeconds, maxSeconds float64) (pairMatch, bool) {
	bestSum, cur := 0.0, 0.0
	cs := blockStart
	bs, be := blockStart, blockStart
	for idx := blockStart; idx <= lastHit; idx++ {
		sc := introBitThreshold - float64(dists[idx])
		if cur+sc <= 0 {
			cur = 0
			cs = idx + 1
		} else {
			cur += sc
			if cur > bestSum {
				bestSum = cur
				bs = cs
				be = idx
			}
		}
	}
	if bestSum <= 0 || be < bs {
		return pairMatch{}, false
	}
	meanScore := bestSum / float64(be-bs+1)
	for bs < be && dists[bs] > introEdgeBitThreshold {
		bs++
	}
	for be > bs && dists[be] > introEdgeBitThreshold {
		be--
	}
	if be-bs+1 < minPts {
		return pairMatch{}, false
	}
	hits := 0
	for idx := bs; idx <= be; idx++ {
		if dists[idx] <= introHitBits {
			hits++
		}
	}
	if float64(hits) < introMinHitDensity*float64(be-bs+1) {
		return pairMatch{}, false
	}

	// A run that starts at either file's first point was cut off by the start
	// of the window, not by the audio, so it gets no start lead.
	lStart := float64(from+bs) * exactChromaprintHopSeconds
	if from+bs > 0 && from+bs+shift > 0 {
		lStart += introStartLeadSeconds
	}
	lEnd := float64(from+be+1)*exactChromaprintHopSeconds + introEndLeadSeconds
	shiftSec := float64(shift) * exactChromaprintHopSeconds
	dur := lEnd - lStart
	if dur < minPairSeconds || dur > maxSeconds {
		return pairMatch{}, false
	}
	return pairMatch{
		Left:    Segment{Start: lStart, End: lEnd},
		Right:   Segment{Start: lStart + shiftSec, End: lEnd + shiftSec},
		Quality: meanScore,
	}, true
}

// mergePairMatch adds run to all. Two alignments a few points apart find the
// same audio, so a run that overlaps one already kept replaces it only when
// it scores better, or scores about the same and is longer.
func mergePairMatch(all []pairMatch, run pairMatch) []pairMatch {
	for idx, prev := range all {
		ov := math.Min(prev.Left.End, run.Left.End) - math.Max(prev.Left.Start, run.Left.Start)
		shorter := math.Min(prev.Left.End-prev.Left.Start, run.Left.End-run.Left.Start)
		if ov > 0 && ov >= 0.5*shorter {
			if run.Quality > prev.Quality+0.3 ||
				(math.Abs(run.Quality-prev.Quality) <= 0.3 && run.Left.End-run.Left.Start > prev.Left.End-prev.Left.Start) {
				all[idx] = run
			}
			return all
		}
	}
	return append(all, run)
}

// tightConsensusSegment clusters partner matches within ±4 s on both edges
// (counting each partner episode once) and prefers a confirmed theme over a
// short near-zero studio bumper.
func tightConsensusSegment(byPartner map[string][]pairVote) (Segment, int, float64) {
	type item struct {
		partner string
		vote    pairVote
	}
	var all []item
	for partner, votes := range byPartner {
		for _, v := range votes {
			all = append(all, item{partner: partner, vote: v})
		}
	}
	if len(all) == 0 {
		return Segment{}, 0, 0
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].vote.Segment.Start != all[j].vote.Segment.Start {
			return all[i].vote.Segment.Start < all[j].vote.Segment.Start
		}
		if all[i].vote.Segment.End != all[j].vote.Segment.End {
			return all[i].vote.Segment.End < all[j].vote.Segment.End
		}
		return all[i].partner < all[j].partner
	})

	type cluster struct {
		seg   Segment
		votes int
		qual  float64
	}
	var clusters []cluster
	for i := range all {
		anchor := all[i].vote.Segment
		seen := map[string]struct{}{}
		var starts, ends, quals []float64
		for _, other := range all {
			if math.Abs(other.vote.Segment.Start-anchor.Start) > introClusterEdgeTolSeconds ||
				math.Abs(other.vote.Segment.End-anchor.End) > introClusterEdgeTolSeconds {
				continue
			}
			if _, dup := seen[other.partner]; dup {
				continue
			}
			seen[other.partner] = struct{}{}
			starts = append(starts, other.vote.Segment.Start)
			ends = append(ends, other.vote.Segment.End)
			quals = append(quals, other.vote.Quality)
		}
		if len(starts) == 0 {
			continue
		}
		clusters = append(clusters, cluster{
			seg:   Segment{Start: medianSeconds(starts), End: medianSeconds(ends)},
			votes: len(starts),
			qual:  medianSeconds(quals),
		})
	}
	if len(clusters) == 0 {
		return Segment{}, 0, 0
	}

	best := clusters[0]
	for _, cl := range clusters[1:] {
		if betterIntroCluster(cl.seg, cl.votes, best.seg, best.votes) {
			best = cl
		}
	}
	return best.seg, best.votes, best.qual
}

func betterIntroCluster(a Segment, aVotes int, b Segment, bVotes int) bool {
	aDur, bDur := a.End-a.Start, b.End-b.Start
	if aVotes >= 2 && bVotes >= 2 {
		if win, decided := preferNonLogoOrLongerIntro(a, aDur, b, bDur); decided {
			return win
		}
	}
	if aVotes != bVotes {
		return aVotes > bVotes
	}
	return aDur > bDur
}

func preferNonLogoOrLongerIntro(a Segment, aDur float64, b Segment, bDur float64) (bool, bool) {
	aLogo := a.Start < introLogoStartLimitSeconds && aDur < introLogoMaxDurationSeconds
	bLogo := b.Start < introLogoStartLimitSeconds && bDur < introLogoMaxDurationSeconds
	if aLogo && !bLogo && bDur >= aDur+introLogoMinAdvantageSec {
		return false, true
	}
	if bLogo && !aLogo && aDur >= bDur+introLogoMinAdvantageSec {
		return true, true
	}
	if math.Abs(a.Start-b.Start) <= introClusterEdgeTolSeconds && math.Abs(aDur-bDur) > introLogoMinAdvantageSec {
		return aDur > bDur, true
	}
	return false, false
}

func refineAndValidateIntro(seg Segment, conf int, quality float64, input fingerprintInput, profile matchProfile) (Segment, bool) {
	seg = refineEdgesBySilence(seg, input.Silences)
	if seg.Start > 0 && seg.Start <= profile.ZeroStartSnapSeconds &&
		!hasLeadingSilence(input.Silences, seg.Start, leadingSilenceSnapGuardSeconds) {
		seg.Start = 0
	}
	if profile.SnapToChapters {
		for _, chapter := range input.Candidate.Chapters {
			seg.Start = snapBoundary(seg.Start, chapter.StartSeconds)
			seg.End = snapBoundary(seg.End, chapter.StartSeconds)
			seg.End = snapBoundary(seg.End, chapter.EndSeconds)
		}
	}
	if seg.Start < 0 {
		seg.Start = 0
	}
	if input.Candidate.DurationSeconds > 0 && seg.End > input.Candidate.DurationSeconds {
		seg.End = input.Candidate.DurationSeconds
	}
	if !validAdjustedSegment(seg, profile) {
		return Segment{}, false
	}
	if profile.weakShortMatch(seg, conf) {
		return Segment{}, false
	}
	if profile.MinimumShortQuality > 0 && seg.End-seg.Start < profile.ShortQualitySeconds && quality < profile.MinimumShortQuality {
		return Segment{}, false
	}
	return seg, true
}
