package intromarkers

import (
	"math"
	"sort"
)

// Season comparison over loudness envelopes.
//
// A pair result is only evidence; an intro is what most of the season agrees
// on. Stretches of one file that name the same span (within a few seconds)
// form a cluster, and a cluster is reported only when several partner episodes
// support it, every supporter correlates well, no edge was cut off by the
// window, and the cluster sits where an intro can sit. The rules are stricter
// than the Chromaprint path's for one reason: an envelope matches any two
// copies of one recording, so a reprise of the theme inside the episode — the
// cold open of a two-parter, a "next time" montage — is as matchable as the
// theme itself, and only position and agreement across the season tell them
// apart.
const (
	// levelAgreementSeconds is how far two stretches of one file may differ
	// and still count as the same span.
	levelAgreementSeconds = 3.0
	// levelTightSeconds is how close every supporter's edge must be for the
	// edge to be their median with no loss of confidence.
	levelTightSeconds = 0.2
	// levelMajoritySeconds is how close to the median a supporter's edge must
	// be to count toward a majority. A majority keeps the median, which a
	// degraded match cannot shorten; without one the supporters' intersection
	// is used, which is never wider than any of them.
	levelMajoritySeconds = 1.5
	// levelIntersectionPenalty is taken off the confidence of a cluster whose
	// edges had to be intersected.
	levelIntersectionPenalty = 0.10
	// levelSupportCorrelation is the least mean correlation a supporting
	// stretch needs.
	levelSupportCorrelation = 0.80
	// levelPairCorrelation is the least mean correlation a season of two
	// episodes accepts, where no third episode can confirm.
	levelPairCorrelation = 0.85
	// levelPairMinimumSeconds is the shortest stretch a season of two accepts.
	levelPairMinimumSeconds = 8.0
	// levelMinimumPartners is how many other episodes must support a cluster.
	levelMinimumPartners = 2
	// levelEdgeMarginSeconds is how close to the end of the window a stretch
	// may end before its end counts as cut off, which says nothing about
	// where the intro ends.
	levelEdgeMarginSeconds = 0.3
	// levelHeadStartFraction is the share of the episode before which an
	// intro must start. A shared theme later than this is a reprise or a
	// preview montage, not the opening.
	levelHeadStartFraction = 0.6
	// levelWideSearchEpisodes bounds the second pass for a file whose
	// neighbors gave it nothing.
	levelWideSearchEpisodes = 48
	// levelMaximumSeconds caps a reported intro, as the reference engine
	// does; the configured maximum can only make it shorter.
	levelMaximumSeconds = 240.0
)

// levelPair is one shared stretch of two episodes, in each file's own time.
type levelPair struct {
	Start, End   float64
	PartnerStart float64
	Correlation  float64
}

// levelStretch is a pair result as one file sees it.
type levelStretch struct {
	Start, End float64
	Partner    string
	// Supporters is how many partner episodes agreed on the span.
	Supporters int
	// Intersected reports that the edges are the supporters' intersection
	// rather than their median, which costs confidence.
	Intersected bool
	// Correlation is the mean similarity of the stretch.
	Correlation float64
	// ClippedEnd reports that the stretch ran to the end of the window, so
	// its end is where the window stopped, not where the recording did.
	ClippedEnd bool
}

// CompareLevels finds each file's intro by comparing the season's loudness
// envelopes. It returns the segments to write, keyed by file ID.
func CompareLevels(inputs []levelsInput, cfg Config) map[int]Segment {
	cfg = cfg.normalized()
	ordered := append([]levelsInput(nil), inputs...)
	sortLevelInputs(ordered)
	envelopes := make([]envelope, len(ordered))
	for i, input := range ordered {
		envelopes[i] = envelopeOf(input)
	}

	candidateOf := make(map[int]Candidate, len(inputs))
	for _, input := range inputs {
		candidateOf[input.Candidate.FileID] = input.Candidate
	}
	stretches := collectLevelStretches(ordered, envelopes, cfg)
	episodeOf := make(map[int]string, len(inputs))
	for _, input := range inputs {
		episodeOf[input.Candidate.FileID] = input.Candidate.EpisodeID
	}
	chosen := make(map[int]levelStretch, len(stretches))
	durations := make([]episodeDuration, 0, len(stretches))
	for fileID, byPartner := range stretches {
		stretch, ok := bestLevelCluster(byPartner, candidateOf[fileID], cfg)
		if !ok {
			continue
		}
		chosen[fileID] = stretch
		durations = append(durations, episodeDuration{episode: episodeOf[fileID], seconds: stretch.End - stretch.Start})
	}

	usualDuration, sharing := usualIntroDuration(durations, seasonDurationToleranceSeconds)
	episodes := distinctLevelEpisodeCount(inputs)
	seasonConsistent := episodes > 0 && float64(sharing)/float64(episodes) >= minimumSeasonCoverage

	segments := make(map[int]Segment, len(chosen))
	for _, input := range ordered {
		stretch, ok := chosen[input.Candidate.FileID]
		if !ok {
			continue
		}
		segment := Segment{Start: stretch.Start, End: stretch.End, Algorithm: LevelAlgorithm}
		duration := segment.End - segment.Start
		switch {
		case duration < shortIntroSeconds:
			segment.Confidence = chromaprintShortConfidence
		case stretch.Supporters < 2:
			// One partner is a pairing, not a season; it rates no higher
			// than a match the season does not agree with.
			segment.Confidence = chromaprintInconsistentConfidence
		case seasonConsistent && math.Abs(duration-usualDuration) <= seasonDurationToleranceSeconds:
			segment.Confidence = chromaprintConsistentConfidence
		default:
			segment.Confidence = chromaprintInconsistentConfidence
		}
		if stretch.Intersected {
			segment.Confidence = math.Max(chromaprintShortConfidence, segment.Confidence-levelIntersectionPenalty)
		}
		segment = refineIntroSegment(input, segment, cfg)
		if segment.End <= segment.Start || segment.Algorithm == "" {
			continue
		}
		segments[input.Candidate.FileID] = segment
	}
	return segments
}

// sortLevelInputs orders inputs by season, episode and file, as the
// Chromaprint path ordered fingerprint inputs.
func sortLevelInputs(ordered []levelsInput) {
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
}

// collectLevelStretches compares the season's envelopes and returns, per file,
// the stretches its partners share with it.
func collectLevelStretches(ordered []levelsInput, envelopes []envelope, cfg Config) map[int]map[string][]levelStretch {
	stretches := map[int]map[string][]levelStretch{}
	record := func(i, j int, pair levelPair) {
		left, right := ordered[i], ordered[j]
		leftWindow := levelsWindow(left.Candidate, cfg)
		rightWindow := levelsWindow(right.Candidate, cfg)
		addLevelStretch(stretches, left.Candidate.FileID, right.Candidate.EpisodeID, levelStretch{
			Start:       pair.Start,
			End:         pair.End,
			Correlation: pair.Correlation,
			ClippedEnd:  pair.End >= leftWindow.End-levelEdgeMarginSeconds,
		})
		partnerEnd := pair.PartnerStart + (pair.End - pair.Start)
		addLevelStretch(stretches, right.Candidate.FileID, left.Candidate.EpisodeID, levelStretch{
			Start:       pair.PartnerStart,
			End:         partnerEnd,
			Correlation: pair.Correlation,
			ClippedEnd:  partnerEnd >= rightWindow.End-levelEdgeMarginSeconds,
		})
	}
	comparable := func(i, j int) bool {
		a, b := ordered[i].Candidate.EpisodeID, ordered[j].Candidate.EpisodeID
		return a != "" && b != "" && a != b
	}
	compared := map[[2]int]struct{}{}
	compare := func(i, j int) {
		if i > j {
			i, j = j, i
		}
		key := [2]int{i, j}
		if _, done := compared[key]; done {
			return
		}
		compared[key] = struct{}{}
		for _, pair := range sharedRuns(envelopes[i], envelopes[j], defaultLevelMatchConfig()) {
			record(i, j, pair)
		}
	}
	// Counted in episodes, not files, so every version of an episode in
	// reach is compared but they share one place, as the Chromaprint path
	// does.
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

	// A file whose neighbors gave it no qualifying span gets the wider
	// search; one stretch that no partner confirms is not enough. The search
	// walks episodes outward from the file's own.
	groups := episodeGroups(levelsEpisodeOrder(ordered))
	groupOf := make([]int, len(ordered))
	for g, group := range groups {
		for i := group[0]; i < group[1]; i++ {
			groupOf[i] = g
		}
	}
	for i := range ordered {
		if _, ok := bestLevelCluster(stretches[ordered[i].Candidate.FileID], ordered[i].Candidate, cfg); ok {
			continue
		}
		extra := map[string]struct{}{}
		g := groupOf[i]
		for distance := 1; distance < len(groups) && len(extra) < levelWideSearchEpisodes; distance++ {
			for _, h := range [2]int{g - distance, g + distance} {
				if h < 0 || h >= len(groups) {
					continue
				}
				for j := groups[h][0]; j < groups[h][1]; j++ {
					if !comparable(i, j) {
						continue
					}
					if !within(extra, levelWideSearchEpisodes, ordered[j].Candidate.EpisodeID) {
						continue
					}
					compare(i, j)
				}
			}
		}
	}
	return stretches
}

// addLevelStretch records one file's side of a pair result.
func addLevelStretch(stretches map[int]map[string][]levelStretch, fileID int, partner string, stretch levelStretch) {
	stretch.Partner = partner
	byPartner := stretches[fileID]
	if byPartner == nil {
		byPartner = map[string][]levelStretch{}
		stretches[fileID] = byPartner
	}
	byPartner[partner] = append(byPartner[partner], stretch)
}

// levelsEpisodeOrder adapts levels inputs to the episode grouping helper,
// which reads fingerprint inputs.
func levelsEpisodeOrder(ordered []levelsInput) []fingerprintInput {
	out := make([]fingerprintInput, len(ordered))
	for i, input := range ordered {
		out[i] = fingerprintInput{Candidate: input.Candidate}
	}
	return out
}

// bestLevelCluster reduces one file's pair stretches to the span the most
// partners agree on, or reports that none qualifies. A cluster must sit where
// an intro can sit: an opening that starts in the last two fifths of the
// episode is a reprise of its theme, not the intro.
//
// A span only one partner carries is the last resort, written at the lower
// confidence that a single pairing earns: a season where two episodes share an
// opening the rest of the season does not still has an opening.
func bestLevelCluster(byPartner map[string][]levelStretch, candidate Candidate, cfg Config) (levelStretch, bool) {
	if len(byPartner) == 0 {
		return levelStretch{}, false
	}
	minimumPartners := levelMinimumPartners
	if len(byPartner) < levelMinimumPartners {
		minimumPartners = 1
	}
	if stretch, ok := strongestLevelCluster(byPartner, candidate, cfg, minimumPartners, levelSupportCorrelation, max(5.0, float64(cfg.MinimumIntroDurationSeconds))); ok {
		return stretch, true
	}
	if minimumPartners == 1 {
		return levelStretch{}, false
	}
	// One partner, and a better match and a longer span to make up for it.
	return strongestLevelCluster(byPartner, candidate, cfg, 1, levelPairCorrelation, levelPairMinimumSeconds)
}

// strongestLevelCluster returns the best-supported qualifying cluster at the
// given gates.
func strongestLevelCluster(byPartner map[string][]levelStretch, candidate Candidate, cfg Config, minimumPartners int, minimumCorrelation, minimumSeconds float64) (levelStretch, bool) {
	var all []levelStretch
	for _, stretches := range byPartner {
		all = append(all, stretches...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start < all[j].Start })

	headLimit := math.Inf(1)
	if candidate.DurationSeconds > 0 {
		headLimit = candidate.DurationSeconds * levelHeadStartFraction
	}

	var (
		best        levelStretch
		bestSupport int
	)
	for i := range all {
		anchor := all[i]
		var cluster []levelStretch
		seen := map[string]struct{}{}
		for _, other := range all[i:] {
			if other.Start-anchor.Start > levelAgreementSeconds {
				break
			}
			if math.Abs(other.Start-anchor.Start) > levelAgreementSeconds ||
				math.Abs(other.End-anchor.End) > levelAgreementSeconds {
				continue
			}
			if _, dup := seen[other.Partner]; dup {
				continue
			}
			seen[other.Partner] = struct{}{}
			cluster = append(cluster, other)
		}
		if len(cluster) < minimumPartners {
			continue
		}
		stretch, ok := consensusLevelStretch(cluster, minimumCorrelation, minimumSeconds, cfg)
		if !ok || stretch.Start > headLimit {
			continue
		}
		if len(cluster) > bestSupport || (len(cluster) == bestSupport && stretch.End-stretch.Start > best.End-best.Start) {
			best, bestSupport = stretch, len(cluster)
		}
	}
	if bestSupport == 0 {
		return levelStretch{}, false
	}
	return best, true
}

// consensusLevelStretch turns the stretches of one cluster into the span to
// report: their median edges when the supporters agree, else the edges a
// majority of them share, else their intersection, which is never wider than
// any of them.
func consensusLevelStretch(cluster []levelStretch, minimumCorrelation, minimumSeconds float64, cfg Config) (levelStretch, bool) {
	// A supporter whose stretch ran into the end of the window says nothing
	// about where the recording ends, so it counts toward agreement but not
	// toward the end.
	starts := make([]float64, 0, len(cluster))
	ends := make([]float64, 0, len(cluster))
	correlation := 1.0
	for _, stretch := range cluster {
		if stretch.Correlation < minimumCorrelation {
			return levelStretch{}, false
		}
		correlation = math.Min(correlation, stretch.Correlation)
		if stretch.ClippedEnd {
			continue
		}
		starts = append(starts, stretch.Start)
		ends = append(ends, stretch.End)
	}
	if len(starts) == 0 {
		return levelStretch{}, false
	}
	start, end := medianSeconds(starts), medianSeconds(ends)
	intersected := false
	tight := true
	for i := range starts {
		if math.Abs(starts[i]-start) > levelTightSeconds || math.Abs(ends[i]-end) > levelTightSeconds {
			tight = false
			break
		}
	}
	if !tight {
		// One supporter whose match stopped early, as a degraded encode's
		// does, must not shorten the span the rest agree on, so a majority
		// keeps the median. Without a majority the intersection decides.
		agreeing := 0
		for i := range starts {
			if math.Abs(starts[i]-start) <= levelMajoritySeconds && math.Abs(ends[i]-end) <= levelMajoritySeconds {
				agreeing++
			}
		}
		if 2*agreeing <= len(starts) {
			start, end = innerIntersection(starts, ends)
			intersected = true
		}
	}
	duration := end - start
	maximum := math.Min(levelMaximumSeconds, float64(cfg.MaximumIntroDurationSeconds))
	if duration < minimumSeconds || duration > maximum {
		return levelStretch{}, false
	}
	return levelStretch{
		Start:       start,
		End:         end,
		Correlation: correlation,
		Supporters:  len(cluster),
		Intersected: intersected,
	}, true
}

// innerIntersection returns the latest start and the earliest end, so the span
// is never wider than any supporter's.
func innerIntersection(starts, ends []float64) (float64, float64) {
	start, end := starts[0], ends[0]
	for i := range starts {
		start = math.Max(start, starts[i])
		end = math.Min(end, ends[i])
	}
	return start, end
}
