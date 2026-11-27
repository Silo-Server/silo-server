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
	// stretch needs. A level trace of the same recording in two lossy
	// encodes agrees to about 0.7 in the quiet parts of an opening and to
	// 0.95 in the loud ones, and a season's episodes are not encoded alike:
	// on the Pioneer One episodes one episode's theme scores 0.74 to 0.78
	// against three partners whose own pairs score 0.82 to 0.86, so a gate
	// of 0.8 lost that episode's opening while the theme was plainly shared.
	levelSupportCorrelation = 0.72
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
	// introPositionBandSeconds is how much later than the season's own intros
	// an opening may start. Beyond it the shared audio is a reprise or a
	// trailer. It is generous: a cold open runs to a couple of minutes.
	introPositionBandSeconds = 110.0
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
	// PartnerStart is where the partner's own span starts, which is where the
	// season places this opening; it is what the position band reads.
	PartnerStart float64
	Partner      string
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
// levelAdjacentOffsetToleranceSeconds is how far two runs of one pair may
// differ in alignment before they are at different offsets: a run measured one
// frame off the other is the same correspondence, one measured a window off is
// not.
const levelAdjacentOffsetToleranceSeconds = 0.5

// levelAdjacentMergeSeconds is the gap within which two shared runs of one pair
// are one recording. A pair's runs break where a few windows dip under the
// correlation gate — one encode's quiet passage against another's — and those
// pieces carry the same music either side of them.
const levelAdjacentMergeSeconds = 2.5

// levelAdjacentDropDB is how far the level of the gap between two runs has to
// fall below the runs themselves before the gap is silence rather than a dip.
const levelAdjacentDropDB = 12.0

// mergeAdjacentPairs joins the runs of one pair that a dip in correlation split
// in two, keeping the length-weighted mean of their correlations. Three things
// keep runs apart: a gap longer than levelAdjacentMergeSeconds, silence in the
// gap (a studio logo and the theme after it are two recordings, however short
// the gap between them), and a different alignment between the two traces.
func mergeAdjacentPairs(env envelope, pairs []levelPair) []levelPair {
	if len(pairs) < 2 {
		return pairs
	}
	merged := make([]levelPair, 0, len(pairs))
	current := pairs[0]
	weight := current.End - current.Start
	for _, pair := range pairs[1:] {
		// Two runs found at different alignments are not one stretch: merging
		// them would report a partner span that does not cover the second run.
		sameOffset := math.Abs((pair.PartnerStart-pair.Start)-(current.PartnerStart-current.Start)) <= levelAdjacentOffsetToleranceSeconds
		if pair.Start-current.End > levelAdjacentMergeSeconds || !sameOffset || silenceBetween(env, current, pair) {
			merged = append(merged, current)
			current, weight = pair, pair.End-pair.Start
			continue
		}
		length := pair.End - pair.Start
		if total := weight + length; total > 0 {
			current.Correlation = (current.Correlation*weight + pair.Correlation*length) / total
		}
		current.End = math.Max(current.End, pair.End)
		weight += length
	}
	return append(merged, current)
}

// silenceBetween reports that the gap between two runs is quiet enough to be
// silence rather than a passage the correlation mis-scored.
func silenceBetween(env envelope, left, right levelPair) bool {
	if right.Start <= left.End {
		return false
	}
	before, okBefore := env.medianBetween(left.Start, left.End)
	after, okAfter := env.medianBetween(right.Start, right.End)
	gap, okGap := env.medianBetween(left.End, right.Start)
	if !okBefore || !okAfter || !okGap {
		return false
	}
	return gap < math.Min(before, after)-levelAdjacentDropDB
}

// levelCollector gathers what each pair of a season's files sees of one
// another, holding the pair results so the neighbor pass and the wider search
// compute each pair once.
type levelCollector struct {
	ordered   []levelsInput
	envelopes []envelope
	cfg       Config
	stretches map[int]map[string][]levelStretch
	pairs     map[[2]int][]levelPair
	compared  map[[2]int]struct{}
}

func collectLevelStretches(ordered []levelsInput, envelopes []envelope, cfg Config) map[int]map[string][]levelStretch {
	collector := &levelCollector{
		ordered:   ordered,
		envelopes: envelopes,
		cfg:       cfg,
		stretches: map[int]map[string][]levelStretch{},
		pairs:     map[[2]int][]levelPair{},
		compared:  map[[2]int]struct{}{},
	}
	collector.neighborPass()
	collector.wideSearch()
	return collector.stretches
}

// neighborPass compares each file with the episodes next to it, adding both
// files' sides of every match.
func (c *levelCollector) neighborPass() {
	for i := range c.ordered {
		neighbors := map[string]struct{}{}
		for j := i + 1; j < len(c.ordered); j++ {
			if !c.episodesDiffer(i, j) {
				continue
			}
			if !withinEpisodes(neighbors, compareNeighborEpisodes, c.ordered[j].Candidate.EpisodeID) {
				break
			}
			if i > j {
				continue
			}
			key := [2]int{i, j}
			if _, done := c.compared[key]; done {
				continue
			}
			c.compared[key] = struct{}{}
			c.recordFrom(i, j, true)
		}
	}
}

// wideSearch gives a file that its neighbors placed nothing for a wider look,
// walking episodes outward from its own. Only the file that asked is credited
// with what the search finds, so one file's search cannot move a partner's
// marker.
func (c *levelCollector) wideSearch() {
	groups := episodeGroups(levelsEpisodeOrder(c.ordered))
	groupOf := make([]int, len(c.ordered))
	for g, group := range groups {
		for i := group[0]; i < group[1]; i++ {
			groupOf[i] = g
		}
	}
	for i := range c.ordered {
		// Only a span several partners agree on ends the search: one
		// neighbor's recurring cue must not stand in for an opening two
		// episodes out carry.
		if stretch, ok := bestLevelCluster(c.stretches[c.ordered[i].Candidate.FileID], c.ordered[i].Candidate, c.cfg); ok && stretch.Supporters >= levelMinimumPartners {
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
					if !c.episodesDiffer(i, j) {
						continue
					}
					if !withinEpisodes(extra, levelWideSearchEpisodes, c.ordered[j].Candidate.EpisodeID) {
						continue
					}
					c.recordFrom(i, j, false)
				}
			}
		}
	}
}

func (c *levelCollector) episodesDiffer(i, j int) bool {
	a, b := c.ordered[i].Candidate.EpisodeID, c.ordered[j].Candidate.EpisodeID
	return a != "" && b != "" && a != b
}

// compute returns the runs the pair shares, at most once per pair.
func (c *levelCollector) compute(i, j int) []levelPair {
	if i > j {
		i, j = j, i
	}
	key := [2]int{i, j}
	if cached, ok := c.pairs[key]; ok {
		return cached
	}
	runs := mergeAdjacentPairs(c.envelopes[i], sharedRuns(c.envelopes[i], c.envelopes[j], defaultLevelMatchConfig()))
	c.pairs[key] = runs
	return runs
}

// recordFrom adds what the pair (i, j) says about the files, both sides or
// only the file at index i.
func (c *levelCollector) recordFrom(i, j int, both bool) {
	if i > j {
		for _, pair := range c.compute(j, i) {
			if both {
				c.addBoth(j, i, pair)
				continue
			}
			// The pair was measured in the other file's frame, and only the
			// file that asked for the search gets the evidence.
			c.addSide(i, j, pair.PartnerStart, pair.PartnerStart+(pair.End-pair.Start), pair.Start, pair.Correlation)
		}
		return
	}
	for _, pair := range c.compute(i, j) {
		if both {
			c.addBoth(i, j, pair)
			continue
		}
		c.addSide(i, j, pair.Start, pair.End, pair.PartnerStart, pair.Correlation)
	}
}

func (c *levelCollector) addBoth(i, j int, pair levelPair) {
	c.addSide(i, j, pair.Start, pair.End, pair.PartnerStart, pair.Correlation)
	c.addSide(j, i, pair.PartnerStart, pair.PartnerStart+(pair.End-pair.Start), pair.Start, pair.Correlation)
}

// addSide records what one file sees of one run. A run that reached the end of
// either trace has not established where the recording ends, whichever of the
// two it was found in, so the clipping applies to both sides.
func (c *levelCollector) addSide(fileIndex, partnerIndex int, start, end, partnerStart, correlation float64) {
	file, partner := c.ordered[fileIndex], c.ordered[partnerIndex]
	fileWindow := levelsWindow(file.Candidate, c.cfg)
	partnerWindow := levelsWindow(partner.Candidate, c.cfg)
	partnerEnd := partnerStart + (end - start)
	clipped := end >= fileWindow.End-levelEdgeMarginSeconds ||
		partnerEnd >= partnerWindow.End-levelEdgeMarginSeconds
	addLevelStretch(c.stretches, file.Candidate.FileID, partner.Candidate.EpisodeID, levelStretch{
		Start:        start,
		End:          end,
		PartnerStart: partnerStart,
		Correlation:  correlation,
		ClippedEnd:   clipped,
	})
}

// withinEpisodes reports whether episode still has room under limit, counting
// episodes rather than files, so every version of an episode in reach is
// compared but they share one place.
func withinEpisodes(episodes map[string]struct{}, limit int, episode string) bool {
	if _, seen := episodes[episode]; seen {
		return true
	}
	if len(episodes) == limit {
		return false
	}
	episodes[episode] = struct{}{}
	return true
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
	if len(byPartner) < levelMinimumPartners {
		// One partner can only ever be a pairing, so it is held to the
		// pairing's gate from the start: a file with a single partner must
		// not slip through the season's looser one.
		return strongestLevelCluster(byPartner, candidate, cfg, 1, levelPairCorrelation, levelPairMinimumSeconds)
	}
	if stretch, ok := strongestLevelCluster(byPartner, candidate, cfg, levelMinimumPartners, levelSupportCorrelation, max(5.0, float64(cfg.MinimumIntroDurationSeconds))); ok {
		return stretch, true
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
			// A weak stretch is neither a supporter nor a veto: counting it
			// would let it fail the consensus for the partners that agree.
			if other.Correlation < minimumCorrelation {
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
		// Equal support goes to the earlier occurrence: a season's opening
		// is what it plays first, and a longer reprise later in the episode
		// would otherwise win on length.
		if len(cluster) > bestSupport || (len(cluster) == bestSupport && stretch.Start < best.Start) {
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
	// An opening starts at the same point of a season's episodes, give or take
	// a cold open. Shared audio that starts well after the point the other
	// episodes start theirs is a reprise, or a theme reused as a trailer, not
	// the opening: the Pioneer One episodes carry their theme 33 seconds into
	// episode two, where the season's intros are 90 seconds earlier, and both
	// the Chromaprint path and this one placed it as an intro until the band
	// was read.
	partnerStarts := make([]float64, 0, len(cluster))
	for _, stretch := range cluster {
		partnerStarts = append(partnerStarts, stretch.PartnerStart)
	}
	if later := start - medianSeconds(partnerStarts); later > introPositionBandSeconds {
		return levelStretch{}, false
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
