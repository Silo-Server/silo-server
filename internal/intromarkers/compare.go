package intromarkers

import (
	"math"
	"math/bits"
	"sort"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

type fingerprintInput struct {
	Candidate Candidate
	Points    []uint32
	// WindowStart is where the fingerprinted window begins in the file, in
	// seconds. Points count from it.
	WindowStart float64
	// Silences are the silences of the window in media seconds, when the
	// fingerprint pass recorded them.
	Silences []mediasample.Interval
}

// Chromaprint points each summarize a window of roughly 2.4 seconds that
// starts at the point's timestamp, so a segment two files share starts matching
// before it begins and stops matching before it ends. These leads were measured
// against authored intro chapters and shift both boundaries back into place.
// Credits matching uses them with DefaultPointHopSeconds; intro matching has
// its own in compare_refined.go.
const (
	chromaprintStartLeadSeconds = 1.35
	chromaprintEndLeadSeconds   = 1.05
)

// zeroStartSnapSeconds treats a detected start this close to the beginning of
// the file as the beginning in the legacy matcher.
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
// result needs with a file's anchor segment to count toward its consensus in
// the legacy matcher.
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
	// minimumShortIntroPartners is how many partner episodes must agree on a
	// short match before it is written. A recurring music cue is often shared
	// by a single pair of episodes; a title card recurs across the season.
	minimumShortIntroPartners = 2
	// seasonDurationToleranceSeconds is how far a file's intro duration may
	// be from the season's usual intro duration and still agree with it.
	seasonDurationToleranceSeconds = 1.5
	// minimumSeasonCoverage is the share of a season's fingerprinted episodes
	// that must share the usual intro duration for any file to count as
	// season-consistent.
	minimumSeasonCoverage = 0.5
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
	// MinimumShortPartners is how many partner episodes must agree on a short
	// match before it is written; zero writes every short match. A file whose
	// neighbors give it only a short match with fewer partners also gets the
	// wider search, which may find the rest.
	MinimumShortPartners int
	// ShortQualitySeconds and MinimumShortQuality reject very short matches
	// whose median pair score is too noisy to be a genuine title card.
	ShortQualitySeconds float64
	MinimumShortQuality float64
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
	// RefinedMatch enables exact Chromaprint hop timing, single-point shift
	// refinement, Kadane multi-run matching, tight edge clustering with logo
	// preference, and silence-based splitting and edge trimming.
	RefinedMatch bool
}

// introProfile is the intro comparison for cfg.
func introProfile(cfg Config) matchProfile {
	cfg = cfg.normalized()
	return matchProfile{
		MinSeconds:             float64(cfg.MinimumIntroDurationSeconds),
		MaxSeconds:             float64(cfg.MaximumIntroDurationSeconds),
		AdjustedMinSeconds:     float64(cfg.MinimumIntroDurationSeconds),
		AdjustedMaxSeconds:     float64(cfg.MaximumIntroDurationSeconds),
		ShortSeconds:           shortIntroSeconds,
		MinimumShortPartners:   minimumShortIntroPartners,
		ShortQualitySeconds:    introShortQualitySeconds,
		MinimumShortQuality:    introMinimumShortQuality,
		SeasonToleranceSeconds: seasonDurationToleranceSeconds,
		Algorithm:              ChromaprintAlgorithm,
		ConsistentConfidence:   chromaprintConsistentConfidence,
		InconsistentConfidence: chromaprintInconsistentConfidence,
		ShortConfidence:        chromaprintShortConfidence,
		ZeroStartSnapSeconds:   introZeroStartSnapSeconds,
		SnapToChapters:         true,
		RefinedMatch:           true,
	}
}

// CompareFingerprints matches each file against its neighboring episodes, and
// an unmatched file against a wider set of the season, and reduces the pair
// results for a file to a consensus. A file's confidence depends on whether
// its intro agrees with the season: a real intro runs the same length in most
// episodes.
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
		// A short match only one partner episode supports is as likely a
		// recurring music cue as an intro, so it is not written.
		if profile.weakShortMatch(segment, match.Confirmations) {
			continue
		}
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

type pairVote struct {
	Segment Segment
	Quality float64
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

	results := collectSeasonPairVotes(ordered, profile)
	return finalizeSeasonMatches(inputs, ordered, results, profile)
}

func collectSeasonPairVotes(ordered []fingerprintInput, profile matchProfile) map[int]map[string][]pairVote {
	c := &seasonPairCollector{
		ordered:    ordered,
		profile:    profile,
		minPairSec: math.Max(0, profile.MinSeconds-introPairMinSlackSeconds),
		results:    map[int]map[string][]pairVote{},
		compared:   map[[2]int]struct{}{},
		pairCache:  map[[2]int][]pairMatch{},
	}
	c.runNeighbors()
	c.runFallback()
	return c.results
}

type seasonPairCollector struct {
	ordered    []fingerprintInput
	profile    matchProfile
	minPairSec float64
	// results keeps pair votes per partner episode so a partner with several
	// versions still casts a single vote in the consensus.
	results   map[int]map[string][]pairVote
	compared  map[[2]int]struct{}
	pairCache map[[2]int][]pairMatch
}

func (c *seasonPairCollector) addVote(fileID int, partner string, vote pairVote) {
	byPartner := c.results[fileID]
	if byPartner == nil {
		byPartner = map[string][]pairVote{}
		c.results[fileID] = byPartner
	}
	byPartner[partner] = append(byPartner[partner], vote)
}

func (c *seasonPairCollector) recordLegacy(input fingerprintInput, partner string, segment Segment) {
	adj := adjustSegment(segment, input, c.profile)
	if !validAdjustedSegment(adj, c.profile) {
		return
	}
	c.addVote(input.Candidate.FileID, partner, pairVote{Segment: adj})
}

func (c *seasonPairCollector) recordRefined(input fingerprintInput, partner string, segment Segment, quality float64) {
	segment.Start += input.WindowStart
	segment.End += input.WindowStart
	if segment.Start < 0 {
		segment.Start = 0
	}
	if input.Candidate.DurationSeconds > 0 && segment.End > input.Candidate.DurationSeconds {
		segment.End = input.Candidate.DurationSeconds
	}
	for _, piece := range splitSegmentBySilences(segment, input.Silences, c.minPairSec) {
		dur := piece.End - piece.Start
		if piece.Start < 0 || piece.End <= piece.Start || dur < c.minPairSec || dur > c.profile.MaxSeconds {
			continue
		}
		c.addVote(input.Candidate.FileID, partner, pairVote{Segment: piece, Quality: quality})
	}
}

func (c *seasonPairCollector) getRefinedPair(a, b int) []pairMatch {
	key := [2]int{a, b}
	if cached, ok := c.pairCache[key]; ok {
		return cached
	}
	matches := comparePairRefined(c.ordered[a].Points, c.ordered[b].Points, c.minPairSec, c.profile.MaxSeconds)
	c.pairCache[key] = matches
	return matches
}

func (c *seasonPairCollector) compare(i, j int) {
	a, b := min(i, j), max(i, j)
	c.compared[[2]int{a, b}] = struct{}{}
	left, right := c.ordered[a], c.ordered[b]
	if c.profile.RefinedMatch {
		for _, m := range c.getRefinedPair(a, b) {
			c.recordRefined(left, right.Candidate.EpisodeID, m.Left, m.Quality)
			c.recordRefined(right, left.Candidate.EpisodeID, m.Right, m.Quality)
		}
		return
	}
	leftSeg, rightSeg, ok := comparePair(left.Points, right.Points, c.profile)
	if !ok {
		return
	}
	c.recordLegacy(left, right.Candidate.EpisodeID, leftSeg)
	c.recordLegacy(right, left.Candidate.EpisodeID, rightSeg)
}

// compareFor records only file i's side of a pair and leaves the pair
// unmarked, so a wider search for a weak match changes no other file's
// results, and the partner's own wider search still runs as usual.
func (c *seasonPairCollector) compareFor(i, j int) {
	a, b := min(i, j), max(i, j)
	left, right := c.ordered[a], c.ordered[b]
	if c.profile.RefinedMatch {
		for _, m := range c.getRefinedPair(a, b) {
			if i < j {
				c.recordRefined(left, right.Candidate.EpisodeID, m.Left, m.Quality)
			} else {
				c.recordRefined(right, left.Candidate.EpisodeID, m.Right, m.Quality)
			}
		}
		return
	}
	leftSeg, rightSeg, ok := comparePair(left.Points, right.Points, c.profile)
	if !ok {
		return
	}
	if i < j {
		c.recordLegacy(left, right.Candidate.EpisodeID, leftSeg)
	} else {
		c.recordLegacy(right, left.Candidate.EpisodeID, rightSeg)
	}
}

func (c *seasonPairCollector) comparable(i, j int) bool {
	a, b := c.ordered[i].Candidate.EpisodeID, c.ordered[j].Candidate.EpisodeID
	return a != "" && b != "" && a != b
}

// withinEpisodeBudget counts episodes, not files: every version of an episode
// in reach is compared, but they share one slot.
func withinEpisodeBudget(episodes map[string]struct{}, limit int, episode string) bool {
	if _, seen := episodes[episode]; seen {
		return true
	}
	if len(episodes) == limit {
		return false
	}
	episodes[episode] = struct{}{}
	return true
}

func (c *seasonPairCollector) runNeighbors() {
	for i := range c.ordered {
		neighbors := map[string]struct{}{}
		for j := i + 1; j < len(c.ordered); j++ {
			if !c.comparable(i, j) {
				continue
			}
			if !withinEpisodeBudget(neighbors, compareNeighborEpisodes, c.ordered[j].Candidate.EpisodeID) {
				break
			}
			c.compare(i, j)
		}
	}
}

func (c *seasonPairCollector) runFallback() {
	// A file whose intro its neighbors lack, such as one sharing an opening
	// with episodes elsewhere in the season, gets a wider search, and so does
	// one whose neighbors give it only a short match too few partners confirm.
	// Eligibility is fixed after the neighbor pass: a result another file's
	// wider search records for this one must not cancel its own.
	neighborMatched := make(map[int]bool, len(c.results))
	weakShort := map[int]bool{}
	for fileID, byPartner := range c.results {
		if c.profile.RefinedMatch {
			// An intro match is settled once two partners confirm one that
			// is not short. Any other file searches wider, for its own
			// result only.
			seg, conf, _ := tightConsensusSegment(byPartner)
			neighborMatched[fileID] = conf >= 2 && seg.End-seg.Start >= c.profile.ShortSeconds
			weakShort[fileID] = true
			continue
		}
		weakShort[fileID] = c.profile.weakShortMatch(consensusSegmentVotes(byPartner))
		neighborMatched[fileID] = !weakShort[fileID]
	}

	// The search walks episodes, not files, outward from the file's own: a
	// file among many versions of its episode would otherwise reach every
	// episode on one side before the nearer ones on the other.
	groups := episodeGroups(c.ordered)
	groupOf := make([]int, len(c.ordered))
	for g, group := range groups {
		for i := group[0]; i < group[1]; i++ {
			groupOf[i] = g
		}
	}
	for i := range c.ordered {
		if neighborMatched[c.ordered[i].Candidate.FileID] {
			continue
		}
		c.runFallbackForFile(i, groupOf[i], groups, weakShort[c.ordered[i].Candidate.FileID])
	}
}

func (c *seasonPairCollector) runFallbackForFile(i, g int, groups [][2]int, selfOnly bool) {
	extra := map[string]struct{}{}
	for distance := 1; distance < len(groups) && len(extra) < compareFallbackEpisodes; distance++ {
		for _, h := range [2]int{g - distance, g + distance} {
			if h < 0 || h >= len(groups) {
				continue
			}
			for j := groups[h][0]; j < groups[h][1]; j++ {
				if !c.comparable(i, j) {
					continue
				}
				if _, done := c.compared[[2]int{min(i, j), max(i, j)}]; done {
					continue
				}
				if !withinEpisodeBudget(extra, compareFallbackEpisodes, c.ordered[j].Candidate.EpisodeID) {
					continue
				}
				if selfOnly {
					c.compareFor(i, j)
				} else {
					c.compare(i, j)
				}
			}
		}
	}
}

func finalizeSeasonMatches(
	inputs []fingerprintInput,
	ordered []fingerprintInput,
	results map[int]map[string][]pairVote,
	profile matchProfile,
) map[int]seasonMatch {
	type fileResult struct {
		segment       Segment
		confirmations int
	}
	inputByID := make(map[int]fingerprintInput, len(ordered))
	for _, input := range ordered {
		inputByID[input.Candidate.FileID] = input
	}
	scored := make(map[int]fileResult, len(results))
	durations := make([]episodeDuration, 0, len(results))
	for fileID, byPartner := range results {
		input := inputByID[fileID]
		if profile.RefinedMatch {
			seg, conf, qual := tightConsensusSegment(byPartner)
			if conf == 0 {
				continue
			}
			seg, ok := refineAndValidateIntro(seg, conf, qual, input, profile)
			if !ok {
				continue
			}
			scored[fileID] = fileResult{segment: seg, confirmations: conf}
			durations = append(durations, episodeDuration{episode: input.Candidate.EpisodeID, seconds: seg.End - seg.Start})
			continue
		}
		seg, conf := consensusSegmentVotes(byPartner)
		scored[fileID] = fileResult{segment: seg, confirmations: conf}
		durations = append(durations, episodeDuration{episode: input.Candidate.EpisodeID, seconds: seg.End - seg.Start})
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

// weakShortMatch reports a short match fewer partner episodes confirm than
// the profile requires before writing one.
func (p matchProfile) weakShortMatch(segment Segment, confirmations int) bool {
	return p.MinimumShortPartners > 0 && segment.End-segment.Start < p.ShortSeconds && confirmations < p.MinimumShortPartners
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

// consensusSegmentVotes picks the pair result that the most partner episodes agree
// with, preferring the longer one on a tie. Each partner then contributes its
// result closest to that anchor, and the consensus is the median of those
// boundaries, together with how many partners agreed.
func consensusSegmentVotes(byPartner map[string][]pairVote) (Segment, int) {
	partners := make([]string, 0, len(byPartner))
	for partner := range byPartner {
		partners = append(partners, partner)
	}
	sort.Strings(partners)

	agreeing := func(candidate Segment) int {
		votes := 0
		for _, partner := range partners {
			for _, other := range byPartner[partner] {
				if segmentOverlap(candidate, other.Segment) >= minimumConsensusOverlap {
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
			votes := agreeing(candidate.Segment)
			if votes > anchorVotes || (votes == anchorVotes && candidate.Segment.End-candidate.Segment.Start > anchor.End-anchor.Start) {
				anchor, anchorVotes = candidate.Segment, votes
			}
		}
	}

	starts := make([]float64, 0, len(partners))
	ends := make([]float64, 0, len(partners))
	for _, partner := range partners {
		best, bestOverlap := Segment{}, 0.0
		for _, candidate := range byPartner[partner] {
			if overlap := segmentOverlap(anchor, candidate.Segment); overlap > bestOverlap {
				best, bestOverlap = candidate.Segment, overlap
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
	return candidateShiftsWithLimit(left, right, candidateShiftSamplePoints(), 8)
}

func candidateShiftsWithLimit(left, right []uint32, step, maxShifts int) []int {
	counts := map[int]int{}
	for i := 0; i < len(left); i += step {
		a := left[i]
		for j := 0; j < len(right); j += step {
			if bits.OnesCount32(a^right[j]) <= 6 {
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

	limit := min(maxShifts, len(candidates))
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
