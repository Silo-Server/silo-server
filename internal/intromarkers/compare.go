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

	// Results are kept per partner episode so a partner with several versions
	// still casts a single vote in the consensus.
	results := map[int]map[string][]Segment{}
	record := func(input fingerprintInput, partner string, segment Segment) {
		if !validAdjustedSegment(segment) {
			return
		}
		byPartner := results[input.Candidate.FileID]
		if byPartner == nil {
			byPartner = map[string][]Segment{}
			results[input.Candidate.FileID] = byPartner
		}
		byPartner[partner] = append(byPartner[partner], segment)
	}
	compared := map[[2]int]struct{}{}
	compare := func(i, j int) {
		left, right := ordered[min(i, j)], ordered[max(i, j)]
		compared[[2]int{min(i, j), max(i, j)}] = struct{}{}
		leftSeg, rightSeg, ok := comparePair(left.Points, right.Points, cfg)
		if !ok {
			return
		}
		record(left, right.Candidate.EpisodeID, adjustSegment(leftSeg, left.Candidate))
		record(right, left.Candidate.EpisodeID, adjustSegment(rightSeg, right.Candidate))
	}
	comparable := func(i, j int) bool {
		a, b := ordered[i].Candidate.EpisodeID, ordered[j].Candidate.EpisodeID
		return a != "" && b != "" && a != b
	}
	// Windows count episodes, not files: every version of an episode in reach
	// is compared, but they share one place.
	within := func(episodes map[string]struct{}, limit int, episode string) bool {
		if _, seen := episodes[episode]; seen {
			return true
		}
		if len(episodes) == limit {
			return false
		}
		episodes[episode] = struct{}{}
		return true
	}
	for i := range ordered {
		neighbors := map[string]struct{}{}
		for j := i + 1; j < len(ordered); j++ {
			if !comparable(i, j) {
				continue
			}
			if !within(neighbors, compareNeighborEpisodes, ordered[j].Candidate.EpisodeID) {
				break
			}
			compare(i, j)
		}
	}
	// A file whose intro its neighbors lack, such as one sharing an opening
	// with episodes elsewhere in the season, gets a wider search. Eligibility
	// is fixed after the neighbor pass: a result another file's wider search
	// records for this one must not cancel its own.
	neighborMatched := make(map[int]bool, len(results))
	for fileID := range results {
		neighborMatched[fileID] = true
	}
	// The search walks episodes, not files, outward from the file's own: a
	// file among many versions of its episode would otherwise reach every
	// episode on one side before the nearer ones on the other.
	groups := episodeGroups(ordered)
	groupOf := make([]int, len(ordered))
	for g, group := range groups {
		for i := group[0]; i < group[1]; i++ {
			groupOf[i] = g
		}
	}
	for i := range ordered {
		if neighborMatched[ordered[i].Candidate.FileID] {
			continue
		}
		extra := map[string]struct{}{}
		g := groupOf[i]
		for distance := 1; distance < len(groups) && len(extra) < compareFallbackEpisodes; distance++ {
			for _, h := range [2]int{g - distance, g + distance} {
				if h < 0 || h >= len(groups) {
					continue
				}
				for j := groups[h][0]; j < groups[h][1]; j++ {
					if !comparable(i, j) {
						continue
					}
					if _, done := compared[[2]int{min(i, j), max(i, j)}]; done {
						continue
					}
					if within(extra, compareFallbackEpisodes, ordered[j].Candidate.EpisodeID) {
						compare(i, j)
					}
				}
			}
		}
	}

	best := make(map[int]Segment, len(results))
	for fileID, byPartner := range results {
		segment, confirmations := consensusSegment(byPartner)
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

// episodeGroups splits files sorted in episode order into [start, end) index
// ranges, one per episode. A file with no episode ID is a group of its own.
func episodeGroups(ordered []fingerprintInput) [][2]int {
	var groups [][2]int
	for i := range ordered {
		episode := ordered[i].Candidate.EpisodeID
		if i > 0 && episode != "" && episode == ordered[i-1].Candidate.EpisodeID {
			groups[len(groups)-1][1] = i + 1
			continue
		}
		groups = append(groups, [2]int{i, i + 1})
	}
	return groups
}

// consensusSegment picks the pair result that the most partner episodes agree
// with, preferring the longer one on a tie. Each partner then contributes its
// result closest to that anchor, and the consensus is the median of those
// boundaries, together with how many partners agreed.
func consensusSegment(byPartner map[string][]Segment) (Segment, int) {
	partners := make([]string, 0, len(byPartner))
	for partner := range byPartner {
		partners = append(partners, partner)
	}
	sort.Strings(partners)

	agreeing := func(candidate Segment) int {
		votes := 0
		for _, partner := range partners {
			for _, other := range byPartner[partner] {
				if segmentOverlap(candidate, other) >= minimumConsensusOverlap {
					votes++
					break
				}
			}
		}
		return votes
	}
	var anchor Segment
	anchorVotes := -1
	for _, partner := range partners {
		for _, candidate := range byPartner[partner] {
			votes := agreeing(candidate)
			if votes > anchorVotes || (votes == anchorVotes && candidate.End-candidate.Start > anchor.End-anchor.Start) {
				anchor, anchorVotes = candidate, votes
			}
		}
	}

	starts := make([]float64, 0, len(partners))
	ends := make([]float64, 0, len(partners))
	for _, partner := range partners {
		best, bestOverlap := Segment{}, 0.0
		for _, candidate := range byPartner[partner] {
			if overlap := segmentOverlap(anchor, candidate); overlap > bestOverlap {
				best, bestOverlap = candidate, overlap
			}
		}
		if bestOverlap >= minimumConsensusOverlap {
			starts = append(starts, best.Start)
			ends = append(ends, best.End)
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
