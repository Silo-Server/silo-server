package recommendations

import (
	"fmt"
	"time"
)

type canonicalContentKind string

const (
	canonicalKindMovie   canonicalContentKind = "movie"
	canonicalKindSeries  canonicalContentKind = "series"
	canonicalKindSeason  canonicalContentKind = "season"
	canonicalKindEpisode canonicalContentKind = "episode"
	canonicalKindEbook   canonicalContentKind = "ebook"
)

const (
	maxSeasonSampleCount       = 10
	maxSeasonContributionShare = 0.35
)

// lowProgressThreshold is the progress below which a title left unfinished
// counts as abandoned, once it is older than abandonedProgressAge.
const lowProgressThreshold = 0.15

// abandonedProgressAge is how long a low-progress title must go untouched
// before it counts against the profile's taste. A title paused early tonight
// is more likely being watched than abandoned.
const abandonedProgressAge = 14 * 24 * time.Hour

// A series that has gone quiet for longer than seriesInactiveAge counts in
// proportion to how many of its episodes were watched with a positive weight:
// in full from seriesDepthEpisodes episodes, and at least seriesDepthFloor.
// A pilot or a show dropped after an episode or two was sampled, not liked.
// A series still being watched is not scaled, so a show started last night
// counts like a movie watched last night.
const (
	seriesInactiveAge   = 30 * 24 * time.Hour
	seriesDepthEpisodes = 5
	seriesDepthFloor    = 0.25
)

type canonicalContentRef struct {
	Kind         canonicalContentKind
	CanonicalID  string
	SeriesID     string
	SeasonNumber int
	HasSeason    bool
}

func (r canonicalContentRef) isCanonicalTasteItem() bool {
	return r.Kind == canonicalKindMovie || r.Kind == canonicalKindSeries
}

func (r canonicalContentRef) seasonKey() string {
	if !r.HasSeason || r.SeriesID == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", r.SeriesID, r.SeasonNumber)
}

type canonicalWeightComponents struct {
	Rating         *int
	ExplicitWeight float64
	ImplicitWeight float64
	IntentWeight   float64
	Favorited      bool
	Genres         []string
}

// countsTowardLevel reports whether a title counts toward the profile's
// cold-start level: it was rated, favorited, or watched with a positive
// weight. A title that is only on the watchlist does not. Its weight is a
// small intent signal, and the watchlist stays recommendable, so a profile of
// a few watchlist adds would otherwise open on a row of its own watchlist.
func (s *canonicalWeightComponents) countsTowardLevel() bool {
	return s.Rating != nil || s.Favorited || s.ImplicitWeight > 0
}

type rawImplicitSignal struct {
	Ref       canonicalContentRef
	Weight    float64
	Timestamp time.Time
	Completed bool
}

type seasonAggregate struct {
	Score        float64
	SampleCount  int
	LastSignalAt time.Time
}

func parseSignalTime(raw string, fallback time.Time) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed
	}
	return fallback
}

// implicitWatchWeight returns the taste weight of one progress row at now,
// and false when the row carries none. Progress below lowProgressThreshold is
// negative only once the row is older than abandonedProgressAge; a row with no
// timestamp counts as recent, as it does for time decay.
func implicitWatchWeight(wp WatchProgressRow, now time.Time) (float64, bool) {
	var progressPct float64
	if wp.Completed {
		progressPct = 1.0
	} else if wp.DurationSeconds > 0 {
		progressPct = wp.PositionSeconds / wp.DurationSeconds
	} else {
		return 0, false
	}

	switch {
	case progressPct >= 0.9:
		return WeightWatchHigh, true
	case progressPct >= 0.5:
		return WeightWatchMed, true
	case progressPct < lowProgressThreshold:
		if wp.UpdatedAt.IsZero() || now.Sub(wp.UpdatedAt) < abandonedProgressAge {
			return 0, false
		}
		return WeightWatchLow, true
	default:
		return 0, false
	}
}

func combineCanonicalWeight(rating *int, explicitWeight, implicitWeight, intentWeight float64) float64 {
	if rating == nil {
		return implicitWeight + intentWeight
	}

	switch {
	case *rating >= 4:
		total := explicitWeight + implicitWeight + intentWeight
		if total < 0 {
			return 0
		}
		return total
	case *rating == 3:
		// "Meh" halves what watching and intent say, so a 3-star completion
		// weighs less than an unrated one; the rating adds nothing itself.
		return 0.5 * (implicitWeight + intentWeight)
	default:
		total := explicitWeight
		if implicitWeight < 0 {
			total += implicitWeight
		}
		return total
	}
}

// aggregateSeriesImplicitScore combines a series' season scores into its
// implicit weight. Recent, well-sampled seasons get larger shares of the mean,
// and the mean decays once by the series' most recent signal, as a movie
// decays by its last watch. Decay inside the shares alone cancels out, since
// the shares always sum to 1. A positive score of a series inactive for
// longer than seriesInactiveAge is scaled by seriesDepth(positiveEpisodes);
// a negative one is not, so a dislike keeps its weight.
func aggregateSeriesImplicitScore(seasons []seasonAggregate, positiveEpisodes int, now time.Time, halfLife float64) float64 {
	if len(seasons) == 0 {
		return 0
	}

	rawWeights := make([]float64, len(seasons))
	var latest time.Time
	for i, season := range seasons {
		sampleCount := season.SampleCount
		if sampleCount <= 0 {
			continue
		}
		if sampleCount > maxSeasonSampleCount {
			sampleCount = maxSeasonSampleCount
		}
		lastSignalAt := season.LastSignalAt
		if lastSignalAt.IsZero() {
			lastSignalAt = now
		}
		if lastSignalAt.After(latest) {
			latest = lastSignalAt
		}
		rawWeights[i] = float64(sampleCount) * timeDecay(lastSignalAt, now, halfLife)
	}
	if latest.IsZero() {
		return 0
	}

	shares := cappedNormalizedWeights(rawWeights, maxSeasonContributionShare)
	score := 0.0
	for i, share := range shares {
		score += share * seasons[i].Score
	}
	score *= timeDecay(latest, now, halfLife)
	if score > 0 && now.Sub(latest) > seriesInactiveAge {
		score *= seriesDepth(positiveEpisodes)
	}
	return score
}

// seriesDepth is the share of its weight an inactive series keeps for
// positiveEpisodes episodes watched with a positive weight.
func seriesDepth(positiveEpisodes int) float64 {
	return min(1, max(seriesDepthFloor, float64(positiveEpisodes)/seriesDepthEpisodes))
}

func cappedNormalizedWeights(rawWeights []float64, cap float64) []float64 {
	shares := make([]float64, len(rawWeights))

	total := 0.0
	positiveCount := 0
	for _, weight := range rawWeights {
		if weight <= 0 {
			continue
		}
		total += weight
		positiveCount++
	}
	if total == 0 {
		return shares
	}
	if cap <= 0 || float64(positiveCount)*cap < 1 {
		for i, weight := range rawWeights {
			if weight > 0 {
				shares[i] = weight / total
			}
		}
		return shares
	}

	remaining := make([]int, 0, positiveCount)
	for i, weight := range rawWeights {
		if weight > 0 {
			remaining = append(remaining, i)
		}
	}

	remainingShare := 1.0
	for len(remaining) > 0 {
		remainingRaw := 0.0
		for _, idx := range remaining {
			remainingRaw += rawWeights[idx]
		}
		if remainingRaw == 0 {
			return shares
		}

		nextRemaining := make([]int, 0, len(remaining))
		cappedAny := false
		for _, idx := range remaining {
			proposed := remainingShare * rawWeights[idx] / remainingRaw
			if proposed > cap {
				shares[idx] = cap
				remainingShare -= cap
				cappedAny = true
				continue
			}
			nextRemaining = append(nextRemaining, idx)
		}

		if !cappedAny {
			for _, idx := range remaining {
				shares[idx] = remainingShare * rawWeights[idx] / remainingRaw
			}
			return shares
		}

		remaining = nextRemaining
		if remainingShare <= 0 {
			return shares
		}
	}

	return shares
}

func buildCanonicalImplicitSignals(progress []WatchProgressRow, rewatches []RewatchCount, refs map[string]canonicalContentRef, now time.Time, halfLife float64) (map[string]float64, map[string]struct{}) {
	rawSignals := make(map[string]*rawImplicitSignal)
	ensureRawSignal := func(sourceID string, ref canonicalContentRef) *rawImplicitSignal {
		if existing, ok := rawSignals[sourceID]; ok {
			return existing
		}
		signal := &rawImplicitSignal{Ref: ref}
		rawSignals[sourceID] = signal
		return signal
	}

	for _, wp := range progress {
		ref, ok := refs[wp.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}
		weight, ok := implicitWatchWeight(wp, now)
		if !ok {
			continue
		}

		signal := ensureRawSignal(wp.MediaItemID, ref)
		signal.Weight += weight
		if wp.Completed {
			signal.Completed = true
		}
		if wp.UpdatedAt.After(signal.Timestamp) {
			signal.Timestamp = wp.UpdatedAt
		}
	}

	for _, rc := range rewatches {
		if rc.Count < 2 {
			continue
		}
		ref, ok := refs[rc.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}

		signal := ensureRawSignal(rc.MediaItemID, ref)
		signal.Weight += WeightRewatch
		lastWatchedAt := rc.LastWatchedAt
		if lastWatchedAt.IsZero() {
			lastWatchedAt = now
		}
		if lastWatchedAt.After(signal.Timestamp) {
			signal.Timestamp = lastWatchedAt
		}
	}

	signals := make(map[string]float64)
	completedSet := make(map[string]struct{})

	type seasonBuilder struct {
		sumWeight    float64
		sampleCount  int
		lastSignalAt time.Time
	}

	seriesBuilders := make(map[string]map[string]*seasonBuilder)
	positiveEpisodes := make(map[string]int)

	for _, signal := range rawSignals {
		if signal.Weight == 0 || signal.Ref.CanonicalID == "" {
			continue
		}

		timestamp := signal.Timestamp
		if timestamp.IsZero() {
			timestamp = now
		}

		if signal.Completed {
			completedSet[signal.Ref.CanonicalID] = struct{}{}
		}

		switch signal.Ref.Kind {
		case canonicalKindEpisode, canonicalKindSeason:
			seasonKey := signal.Ref.seasonKey()
			if seasonKey == "" {
				continue
			}
			bySeason := seriesBuilders[signal.Ref.CanonicalID]
			if bySeason == nil {
				bySeason = make(map[string]*seasonBuilder)
				seriesBuilders[signal.Ref.CanonicalID] = bySeason
			}
			builder := bySeason[seasonKey]
			if builder == nil {
				builder = &seasonBuilder{}
				bySeason[seasonKey] = builder
			}
			builder.sumWeight += signal.Weight
			builder.sampleCount++
			if signal.Ref.Kind == canonicalKindEpisode && signal.Weight > 0 {
				positiveEpisodes[signal.Ref.CanonicalID]++
			}
			if timestamp.After(builder.lastSignalAt) {
				builder.lastSignalAt = timestamp
			}
		case canonicalKindMovie, canonicalKindSeries, canonicalKindEbook:
			// Ebooks are their own canonical entity. Reader progress rows carry
			// progress as position with duration 1, so implicitWatchWeight
			// treats the reading ratio exactly like a movie's
			// position/duration ratio (finished book == finished movie).
			signals[signal.Ref.CanonicalID] += signal.Weight * timeDecay(timestamp, now, halfLife)
		}
	}

	for canonicalID, bySeason := range seriesBuilders {
		seasons := make([]seasonAggregate, 0, len(bySeason))
		for _, season := range bySeason {
			if season.sampleCount == 0 {
				continue
			}
			seasons = append(seasons, seasonAggregate{
				Score:        season.sumWeight / float64(season.sampleCount),
				SampleCount:  season.sampleCount,
				LastSignalAt: season.lastSignalAt,
			})
		}
		if len(seasons) == 0 {
			continue
		}
		signals[canonicalID] += aggregateSeriesImplicitScore(seasons, positiveEpisodes[canonicalID], now, halfLife)
	}

	return signals, completedSet
}
