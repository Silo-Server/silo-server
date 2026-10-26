package intromarkers

import (
	"math"
	"math/bits"
	"sort"
)

type fingerprintInput struct {
	Candidate Candidate
	Points    []uint32
	// WindowStart is where the fingerprinted window begins in the file, in
	// seconds. Points count from it.
	WindowStart float64
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

// Confidence reflects how often markers of each kind covered at least 80
// percent of the authored intro chapter in replay: season-consistent intros
// did about nine times in ten, other intros of 20 seconds or more about two
// times in three, and shorter matches, which include recurring music cues
// mistaken for intros, under one time in three.
const (
	chromaprintConsistentConfidence   = 0.90
	chromaprintInconsistentConfidence = 0.65
	chromaprintShortConfidence        = 0.30

	// shortIntroSeconds is the duration below which a match is treated as
	// short.
	shortIntroSeconds = 20.0
	// seasonDurationToleranceSeconds is how far a file's intro duration may
	// be from the season's usual intro duration and still agree with it.
	seasonDurationToleranceSeconds = 1.5
	// minimumSeasonCoverage is the share of a season's fingerprinted episodes
	// that must share the usual intro duration for any file to count as
	// season-consistent.
	minimumSeasonCoverage = 0.5
)

// Adjusted intro bounds: a pair result outside them after the Chromaprint
// leads, zero snap, and chapter snapping is dropped.
const (
	minimumAdjustedIntroSeconds = 10.0
	maximumAdjustedIntroSeconds = 180.0
)

// matchProfile is what a marker kind's season comparison looks for.
type matchProfile struct {
	// MinSeconds and MaxSeconds bound a raw match between two files.
	MinSeconds float64
	MaxSeconds float64
	// AdjustedMinSeconds and AdjustedMaxSeconds bound a match once its
	// boundaries are adjusted into file time.
	AdjustedMinSeconds float64
	AdjustedMaxSeconds float64
	// ShortSeconds is the duration below which a match rates ShortConfidence.
	ShortSeconds float64
	// SeasonToleranceSeconds is how far a file's match duration may be from
	// the season's usual duration and still agree with it.
	SeasonToleranceSeconds float64
	Algorithm              string
	ConsistentConfidence   float64
	InconsistentConfidence float64
	ShortConfidence        float64
	// ZeroStartSnapSeconds moves an adjusted start this close to the start of
	// the file to the start of the file. Zero disables the snap.
	ZeroStartSnapSeconds float64
	// EOFSnapSeconds moves an adjusted end this close to the end of the file
	// to the end of the file. Zero disables the snap.
	EOFSnapSeconds float64
	// SnapToChapters moves adjusted boundaries onto nearby chapter
	// boundaries.
	SnapToChapters bool
}

// introProfile is the intro comparison for cfg.
func introProfile(cfg Config) matchProfile {
	cfg = cfg.normalized()
	return matchProfile{
		MinSeconds:             float64(cfg.MinimumIntroDurationSeconds),
		MaxSeconds:             float64(cfg.MaximumIntroDurationSeconds),
		AdjustedMinSeconds:     minimumAdjustedIntroSeconds,
		AdjustedMaxSeconds:     maximumAdjustedIntroSeconds,
		ShortSeconds:           shortIntroSeconds,
		SeasonToleranceSeconds: seasonDurationToleranceSeconds,
		Algorithm:              ChromaprintAlgorithm,
		ConsistentConfidence:   chromaprintConsistentConfidence,
		InconsistentConfidence: chromaprintInconsistentConfidence,
		ShortConfidence:        chromaprintShortConfidence,
		ZeroStartSnapSeconds:   zeroStartSnapSeconds,
		SnapToChapters:         true,
	}
}

// CompareFingerprints matches each file against its neighboring episodes, and
// an unmatched file against a wider set of the season, and
// reduces the pair results for a file to a consensus: the median boundaries of
// the results that agree with the most-confirmed one. Taking the longest pair
// result instead let a single over-extended match set the boundaries. A file's
// confidence depends on whether its intro agrees with the season: a real intro
// runs the same length in most episodes.
func CompareFingerprints(inputs []fingerprintInput, cfg Config) map[int]Segment {
	return compareFingerprints(inputs, introProfile(cfg))
}

// compareFingerprints is CompareFingerprints for any marker kind's profile.
func compareFingerprints(inputs []fingerprintInput, profile matchProfile) map[int]Segment {
	matches := matchSeason(inputs, profile)
	best := make(map[int]Segment, len(matches))
	for fileID, match := range matches {
		segment := match.Segment
		duration := segment.End - segment.Start
		switch {
		case duration < profile.ShortSeconds:
			segment.Confidence = profile.ShortConfidence
		case match.SeasonConsistent && match.Confirmations >= 2:
			segment.Confidence = profile.ConsistentConfidence
		default:
			segment.Confidence = profile.InconsistentConfidence
		}
		segment.Algorithm = profile.Algorithm
		best[fileID] = segment
	}
	return best
}

// seasonMatch is a file's consensus match before it is rated.
type seasonMatch struct {
	// Segment holds the consensus boundaries, without confidence or
	// algorithm.
	Segment Segment
	// Confirmations is how many partner episodes agreed with the consensus.
	Confirmations int
	// SeasonConsistent reports that at least half the season's episodes share
	// a usual match duration and this match is within tolerance of it.
	SeasonConsistent bool
}

// matchSeason compares the files of a season and reduces each file's pair
// results to a consensus; see CompareFingerprints.
func matchSeason(inputs []fingerprintInput, profile matchProfile) map[int]seasonMatch {
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
		if !validAdjustedSegment(segment, profile) {
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
		leftSeg, rightSeg, ok := comparePair(left.Points, right.Points, profile)
		if !ok {
			return
		}
		record(left, right.Candidate.EpisodeID, adjustSegment(leftSeg, left, profile))
		record(right, left.Candidate.EpisodeID, adjustSegment(rightSeg, right, profile))
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

	type fileResult struct {
		segment       Segment
		confirmations int
	}
	episodeOf := make(map[int]string, len(inputs))
	for _, input := range inputs {
		episodeOf[input.Candidate.FileID] = input.Candidate.EpisodeID
	}
	scored := make(map[int]fileResult, len(results))
	durations := make([]episodeDuration, 0, len(results))
	for fileID, byPartner := range results {
		segment, confirmations := consensusSegment(byPartner)
		scored[fileID] = fileResult{segment: segment, confirmations: confirmations}
		durations = append(durations, episodeDuration{episode: episodeOf[fileID], seconds: segment.End - segment.Start})
	}
	usualDuration, sharing := usualIntroDuration(durations, profile.SeasonToleranceSeconds)
	episodes := distinctFingerprintEpisodeCount(inputs)
	seasonConsistent := episodes > 0 && float64(sharing)/float64(episodes) >= minimumSeasonCoverage

	matches := make(map[int]seasonMatch, len(scored))
	for fileID, result := range scored {
		duration := result.segment.End - result.segment.Start
		matches[fileID] = seasonMatch{
			Segment:          result.segment,
			Confirmations:    result.confirmations,
			SeasonConsistent: seasonConsistent && math.Abs(duration-usualDuration) <= profile.SeasonToleranceSeconds,
		}
	}
	return matches
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

type episodeDuration struct {
	episode string
	seconds float64
}

// usualIntroDuration returns the match duration the most episodes share
// within tolerance seconds, preferring the longer on a tie, and how many
// episodes share it. Versions of one episode count once. A window slides over
// the sorted durations, so long seasons stay linear after the sort.
func usualIntroDuration(durations []episodeDuration, tolerance float64) (float64, int) {
	sorted := append([]episodeDuration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].seconds < sorted[j].seconds })
	inWindow := map[string]int{}
	add := func(d episodeDuration) { inWindow[d.episode]++ }
	remove := func(d episodeDuration) {
		if inWindow[d.episode]--; inWindow[d.episode] == 0 {
			delete(inWindow, d.episode)
		}
	}
	usual, sharing := 0.0, 0
	lo, hi := 0, 0
	for _, candidate := range sorted {
		for hi < len(sorted) && sorted[hi].seconds-candidate.seconds <= tolerance {
			add(sorted[hi])
			hi++
		}
		for candidate.seconds-sorted[lo].seconds > tolerance {
			remove(sorted[lo])
			lo++
		}
		if len(inWindow) >= sharing {
			usual, sharing = candidate.seconds, len(inWindow)
		}
	}
	return usual, sharing
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

func comparePair(left, right []uint32, profile matchProfile) (Segment, Segment, bool) {
	if len(left) == 0 || len(right) == 0 {
		return Segment{}, Segment{}, false
	}

	shifts := candidateShifts(left, right)
	var bestLeft, bestRight Segment
	bestDuration := 0.0
	for _, shift := range shifts {
		leftSeg, rightSeg, ok := comparePairAtShift(left, right, profile, shift)
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

func comparePairAtShift(left, right []uint32, profile matchProfile, shift int) (Segment, Segment, bool) {
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
	if duration < profile.MinSeconds || duration > profile.MaxSeconds {
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

// adjustSegment moves a pair result from the input's fingerprint points into
// file time: it adds the window start and the Chromaprint leads, then snaps
// the boundaries to the start and end of the file and, when the profile asks,
// to nearby chapter boundaries.
func adjustSegment(segment Segment, input fingerprintInput, profile matchProfile) Segment {
	candidate := input.Candidate
	segment.Start += input.WindowStart + chromaprintStartLeadSeconds
	segment.End += input.WindowStart + chromaprintEndLeadSeconds
	if segment.Start <= profile.ZeroStartSnapSeconds {
		segment.Start = 0
	}
	if profile.EOFSnapSeconds > 0 && candidate.DurationSeconds > 0 &&
		candidate.DurationSeconds-segment.End <= profile.EOFSnapSeconds {
		segment.End = candidate.DurationSeconds
	}
	if profile.SnapToChapters {
		for _, chapter := range candidate.Chapters {
			segment.Start = snapBoundary(segment.Start, chapter.StartSeconds)
			segment.End = snapBoundary(segment.End, chapter.StartSeconds)
			segment.End = snapBoundary(segment.End, chapter.EndSeconds)
		}
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

func validAdjustedSegment(segment Segment, profile matchProfile) bool {
	duration := segment.End - segment.Start
	return segment.Start >= 0 && segment.End > segment.Start &&
		duration >= profile.AdjustedMinSeconds && duration <= profile.AdjustedMaxSeconds
}
