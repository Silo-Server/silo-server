package intromarkers

import (
	"math"
	"math/bits"
	"sort"
)

type fingerprintInput struct {
	Candidate Candidate
	Points    []uint32
}

// Chromaprint points each summarize a window of roughly 2.4 seconds that
// starts at the point's timestamp, so a segment two files share starts matching
// before it begins and stops matching before it ends. These leads were measured
// against authored intro chapters and shift both boundaries back into place.
const (
	chromaprintStartLeadSeconds = 1.35
	chromaprintEndLeadSeconds   = 1.05
)

// zeroStartSnapSeconds treats a detected start this close to the beginning of
// the file as the beginning. Intros that start after a short logo or cold open
// keep their real start.
const zeroStartSnapSeconds = 2.0

// compareNeighborEpisodes bounds how many following episodes, in episode
// order, each file is compared with. Neighbors share the season's current
// intro even when it changes mid-season, and the bound keeps long seasons
// linear rather than quadratic.
const compareNeighborEpisodes = 8

// compareFallbackEpisodes bounds the second pass for a file no neighbor
// matched: it is compared with up to this many more episodes, nearest first,
// which finds partners elsewhere in a season without comparing every pair.
const compareFallbackEpisodes = 48

// minimumConsensusOverlap is the overlap, as intersection over union, a pair
// result needs with a file's anchor segment to count toward its consensus.
const minimumConsensusOverlap = 0.3

// CompareFingerprints matches each file against its neighboring episodes, and
// an unmatched file against a wider set of the season, and
// reduces the pair results for a file to a consensus: the median boundaries of
// the results that agree with the most-confirmed one. Taking the longest pair
// result instead let a single over-extended match set the boundaries.
func CompareFingerprints(inputs []fingerprintInput, cfg Config) map[int]Segment {
	cfg = cfg.normalized()
	ordered := append([]fingerprintInput(nil), inputs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].Candidate, ordered[j].Candidate
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		if a.EpisodeNumber != b.EpisodeNumber {
			return a.EpisodeNumber < b.EpisodeNumber
		}
		return a.FileID < b.FileID
	})

	// Each partner episode casts one vote per file, however many versions of
	// it the season holds.
	votes := map[int]map[string]Segment{}
	vote := func(input fingerprintInput, partner string, segment Segment) {
		if !validAdjustedSegment(segment) {
			return
		}
		fileVotes := votes[input.Candidate.FileID]
		if fileVotes == nil {
			fileVotes = map[string]Segment{}
			votes[input.Candidate.FileID] = fileVotes
		}
		if _, ok := fileVotes[partner]; !ok {
			fileVotes[partner] = segment
		}
	}
	compared := map[[2]int]struct{}{}
	compare := func(i, j int) {
		left, right := ordered[min(i, j)], ordered[max(i, j)]
		compared[[2]int{min(i, j), max(i, j)}] = struct{}{}
		leftSeg, rightSeg, ok := comparePair(left.Points, right.Points, cfg)
		if !ok {
			return
		}
		vote(left, right.Candidate.EpisodeID, adjustSegment(leftSeg, left.Candidate))
		vote(right, left.Candidate.EpisodeID, adjustSegment(rightSeg, right.Candidate))
	}
	comparable := func(i, j int) bool {
		a, b := ordered[i].Candidate.EpisodeID, ordered[j].Candidate.EpisodeID
		return a != "" && b != "" && a != b
	}
	for i := range ordered {
		// Count neighboring episodes, not files: every version of a
		// neighbor is compared, but they share one place in the window.
		neighbors := map[string]struct{}{}
		for j := i + 1; j < len(ordered); j++ {
			if !comparable(i, j) {
				continue
			}
			episode := ordered[j].Candidate.EpisodeID
			if _, seen := neighbors[episode]; !seen {
				if len(neighbors) == compareNeighborEpisodes {
					break
				}
				neighbors[episode] = struct{}{}
			}
			compare(i, j)
		}
	}
	// A file whose intro its neighbors lack, such as one sharing an opening
	// with episodes elsewhere in the season, gets a wider search.
	for i := range ordered {
		if len(votes[ordered[i].Candidate.FileID]) > 0 {
			continue
		}
		extra := 0
		for distance := 1; distance < len(ordered) && extra < compareFallbackEpisodes; distance++ {
			for _, j := range [2]int{i - distance, i + distance} {
				if j < 0 || j >= len(ordered) || extra >= compareFallbackEpisodes || !comparable(i, j) {
					continue
				}
				if _, done := compared[[2]int{min(i, j), max(i, j)}]; done {
					continue
				}
				extra++
				compare(i, j)
			}
		}
	}

	matches := make(map[int][]Segment, len(votes))
	for fileID, fileVotes := range votes {
		partners := make([]string, 0, len(fileVotes))
		for partner := range fileVotes {
			partners = append(partners, partner)
		}
		sort.Strings(partners)
		segments := make([]Segment, 0, len(partners))
		for _, partner := range partners {
			segments = append(segments, fileVotes[partner])
		}
		matches[fileID] = segments
	}

	best := make(map[int]Segment, len(matches))
	for fileID, segments := range matches {
		segment, confirmations := consensusSegment(segments)
		confidence := 0.65
		if segment.End-segment.Start >= 30 {
			confidence += 0.10
		}
		if confirmations >= 2 {
			confidence += 0.10
		}
		if segment.Start == 0 {
			confidence += 0.05
		}
		if confidence > 0.90 {
			confidence = 0.90
		}
		segment.Confidence = confidence
		segment.Algorithm = ChromaprintAlgorithm
		best[fileID] = segment
	}
	return best
}

// consensusSegment picks the pair result that the most other results overlap,
// preferring the longer one on a tie, and returns the median boundaries of the
// results that overlap it together with how many there were.
func consensusSegment(segments []Segment) (Segment, int) {
	anchor, anchorVotes := 0, -1
	for i, candidate := range segments {
		votes := 0
		for _, other := range segments {
			if segmentOverlap(candidate, other) >= minimumConsensusOverlap {
				votes++
			}
		}
		if votes > anchorVotes || (votes == anchorVotes &&
			candidate.End-candidate.Start > segments[anchor].End-segments[anchor].Start) {
			anchor, anchorVotes = i, votes
		}
	}
	starts := make([]float64, 0, anchorVotes)
	ends := make([]float64, 0, anchorVotes)
	for _, other := range segments {
		if segmentOverlap(segments[anchor], other) >= minimumConsensusOverlap {
			starts = append(starts, other.Start)
			ends = append(ends, other.End)
		}
	}
	return Segment{Start: medianSeconds(starts), End: medianSeconds(ends)}, len(starts)
}

func segmentOverlap(a, b Segment) float64 {
	intersection := math.Min(a.End, b.End) - math.Max(a.Start, b.Start)
	if intersection <= 0 {
		return 0
	}
	return intersection / (math.Max(a.End, b.End) - math.Min(a.Start, b.Start))
}

func medianSeconds(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func comparePair(left, right []uint32, cfg Config) (Segment, Segment, bool) {
	if len(left) == 0 || len(right) == 0 {
		return Segment{}, Segment{}, false
	}

	shifts := candidateShifts(left, right)
	var bestLeft, bestRight Segment
	bestDuration := 0.0
	for _, shift := range shifts {
		leftSeg, rightSeg, ok := comparePairAtShift(left, right, cfg, shift)
		if !ok {
			continue
		}
		if duration := leftSeg.End - leftSeg.Start; duration > bestDuration {
			bestLeft = leftSeg
			bestRight = rightSeg
			bestDuration = duration
		}
	}
	if bestDuration == 0 {
		return Segment{}, Segment{}, false
	}
	return bestLeft, bestRight, true
}

func comparePairAtShift(left, right []uint32, cfg Config, shift int) (Segment, Segment, bool) {
	type pair struct {
		left  int
		right int
	}
	var matches []pair
	tolerance := candidateShiftSamplePoints()
	for i, lp := range left {
		center := i + shift
		from := max(0, center-tolerance)
		to := min(len(right)-1, center+tolerance)
		if to < from {
			continue
		}
		for j := from; j <= to; j++ {
			if bits.OnesCount32(lp^right[j]) <= 6 {
				matches = append(matches, pair{left: i, right: j})
				break
			}
		}
	}
	if len(matches) == 0 {
		return Segment{}, Segment{}, false
	}

	maxGapPoints := int(math.Ceil(3.5 / DefaultPointHopSeconds))
	bestStart := 0
	bestEnd := 0
	bestRunStart := 0
	runStart := 0
	for i := 1; i < len(matches); i++ {
		leftGap := matches[i].left - matches[i-1].left
		rightGap := matches[i].right - matches[i-1].right
		if leftGap <= maxGapPoints && rightGap >= -candidateShiftSamplePoints() && rightGap <= maxGapPoints {
			continue
		}
		if matches[i-1].left-matches[runStart].left > bestEnd-bestStart {
			bestStart = matches[runStart].left
			bestEnd = matches[i-1].left
			bestRunStart = runStart
		}
		runStart = i
	}
	if matches[len(matches)-1].left-matches[runStart].left > bestEnd-bestStart {
		bestStart = matches[runStart].left
		bestEnd = matches[len(matches)-1].left
		bestRunStart = runStart
	}

	start := float64(bestStart) * DefaultPointHopSeconds
	end := float64(bestEnd+1) * DefaultPointHopSeconds
	duration := end - start
	if duration < float64(cfg.MinimumIntroDurationSeconds) || duration > float64(cfg.MaximumIntroDurationSeconds) {
		return Segment{}, Segment{}, false
	}

	rightShift := matches[bestRunStart].right - matches[bestRunStart].left
	rightStart := float64(max(0, bestStart+rightShift)) * DefaultPointHopSeconds
	rightEnd := rightStart + duration
	return Segment{Start: start, End: end}, Segment{Start: rightStart, End: rightEnd}, true
}

func candidateShifts(left, right []uint32) []int {
	step := candidateShiftSamplePoints()
	counts := map[int]int{}
	for i := 0; i < len(left); i += step {
		for j := 0; j < len(right); j += step {
			if bits.OnesCount32(left[i]^right[j]) <= 6 {
				counts[j-i]++
			}
		}
	}

	type candidate struct {
		shift int
		count int
	}
	candidates := []candidate{{shift: 0, count: counts[0]}}
	for shift, count := range counts {
		if shift == 0 || count < 3 {
			continue
		}
		candidates = append(candidates, candidate{shift: shift, count: count})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count != candidates[j].count {
			return candidates[i].count > candidates[j].count
		}
		if absInt(candidates[i].shift) != absInt(candidates[j].shift) {
			return absInt(candidates[i].shift) < absInt(candidates[j].shift)
		}
		return candidates[i].shift < candidates[j].shift
	})

	limit := min(8, len(candidates))
	shifts := make([]int, 0, limit)
	seen := map[int]struct{}{}
	for _, candidate := range candidates {
		if len(shifts) >= limit {
			break
		}
		if _, ok := seen[candidate.shift]; ok {
			continue
		}
		seen[candidate.shift] = struct{}{}
		shifts = append(shifts, candidate.shift)
	}
	return shifts
}

func candidateShiftSamplePoints() int {
	return max(1, int(math.Round(1/DefaultPointHopSeconds)))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func adjustSegment(segment Segment, candidate Candidate) Segment {
	segment.Start += chromaprintStartLeadSeconds
	segment.End += chromaprintEndLeadSeconds
	if segment.Start <= zeroStartSnapSeconds {
		segment.Start = 0
	}
	for _, chapter := range candidate.Chapters {
		segment.Start = snapBoundary(segment.Start, chapter.StartSeconds)
		segment.End = snapBoundary(segment.End, chapter.StartSeconds)
		segment.End = snapBoundary(segment.End, chapter.EndSeconds)
	}
	if segment.Start < 0 {
		segment.Start = 0
	}
	if candidate.DurationSeconds > 0 && segment.End > candidate.DurationSeconds {
		segment.End = candidate.DurationSeconds
	}
	return segment
}

func snapBoundary(value, boundary float64) float64 {
	delta := boundary - value
	if delta >= -5 && delta <= 2 {
		return boundary
	}
	return value
}

func validAdjustedSegment(segment Segment) bool {
	duration := segment.End - segment.Start
	return segment.Start >= 0 && segment.End > segment.Start && duration >= 10 && duration <= 180
}
