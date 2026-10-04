package recommendations

import (
	"sort"
	"time"
)

// coldStartLevel returns the cold-start graduation level based on positive
// signal count. Higher levels indicate more personalization is appropriate.
//
//	0 signals → level 0 (100% non-personalized)
//	1-4       → level 1 (non-personalized + one personal row)
//	5-14      → level 2 (50/50 mix)
//	15+       → level 3 (fully personalized)
func coldStartLevel(positiveSignalCount int) int {
	switch {
	case positiveSignalCount >= ColdStartFullPersonalized:
		return 3
	case positiveSignalCount >= ColdStartMixed:
		return 2
	case positiveSignalCount >= ColdStartMinimal:
		return 1
	default:
		return 0
	}
}

// The signal_counts kinds that record a dislike: a 1-2 star rating, and a
// title abandoned below lowProgressThreshold.
const (
	signalKindRatedLow = "rated_low"
	signalKindWatchLow = "watch_low"
)

// coldStartLevelOf returns the cold-start level of a profile from its taste
// profile metadata, counting only positive signals. A profile with no taste
// profile is level 0.
func coldStartLevelOf(meta *TasteProfileMeta) int {
	if meta == nil {
		return 0
	}
	positive := 0
	for kind, count := range meta.SignalCounts {
		switch kind {
		case signalKindRatedLow, signalKindWatchLow:
		default:
			positive += count
		}
	}
	return coldStartLevel(positive)
}

// buildColdStartRows builds the set of non-personalized recommendation rows
// used during cold-start (and appended to warm profiles for discovery).
// Rows with empty item slices are omitted.
func buildColdStartRows(popular, recentlyAdded, topRated []ScoredItem, genreSamplers map[string][]ScoredItem) []ForYouRow {
	var rows []ForYouRow

	if len(popular) > 0 {
		rows = append(rows, ForYouRow{
			Type:  RecTypePopular,
			Label: "Popular on This Server",
			Items: popular,
		})
	}

	if len(recentlyAdded) > 0 {
		rows = append(rows, ForYouRow{
			Type:  RecTypeRecentlyAdded,
			Label: "Recently Added",
			Items: recentlyAdded,
		})
	}

	if len(topRated) > 0 {
		rows = append(rows, ForYouRow{
			Type:  RecTypeTopRated,
			Label: "Top Rated",
			Items: topRated,
		})
	}

	// Sort genre names for deterministic row order.
	genres := make([]string, 0, len(genreSamplers))
	for g := range genreSamplers {
		genres = append(genres, g)
	}
	sort.Strings(genres)

	for _, genre := range genres {
		items := genreSamplers[genre]
		if len(items) > 0 {
			rows = append(rows, ForYouRow{
				Type:  "genre_sampler",
				Label: "Top " + genre,
				Items: items,
			})
		}
	}

	return rows
}

// mergePersonalizedAndColdStart combines personalized rows with cold-start rows
// according to the user's cold-start graduation level.
//
//	Level 0: cold-start rows only
//	Level 1: cold-start rows first, then up to 1 personal row
//	Level 2: interleave personal and cold-start rows (alternating)
//	Level 3: personal rows first, cold-start rows appended at the end
func mergePersonalizedAndColdStart(personalRows, coldStartRows []ForYouRow, level int) []ForYouRow {
	switch level {
	case 0:
		return coldStartRows

	case 1:
		limited := personalRows
		if len(limited) > 1 {
			limited = limited[:1]
		}
		merged := make([]ForYouRow, 0, len(coldStartRows)+len(limited))
		merged = append(merged, coldStartRows...)
		merged = append(merged, limited...)
		return merged

	case 2:
		merged := make([]ForYouRow, 0, len(personalRows)+len(coldStartRows))
		pi, ci := 0, 0
		for pi < len(personalRows) || ci < len(coldStartRows) {
			if pi < len(personalRows) {
				merged = append(merged, personalRows[pi])
				pi++
			}
			if ci < len(coldStartRows) {
				merged = append(merged, coldStartRows[ci])
				ci++
			}
		}
		return merged

	default: // level 3
		merged := make([]ForYouRow, 0, len(personalRows)+len(coldStartRows))
		merged = append(merged, personalRows...)
		merged = append(merged, coldStartRows...)
		return merged
	}
}

// applyRecencyBoost multiplies the score of recently added items by a boost
// factor that decays linearly from RecencyBoostMultiplier to 1.0 over
// RecencyBoostDays. Items not present in addedDates or older than the window
// are left unchanged. The returned slice is a new copy sorted by descending
// boosted score.
func applyRecencyBoost(items []ScoredItem, addedDates map[string]time.Time, now time.Time) []ScoredItem {
	boostWindow := time.Duration(RecencyBoostDays) * 24 * time.Hour

	boosted := make([]ScoredItem, len(items))
	for i, item := range items {
		boosted[i] = item

		addedAt, ok := addedDates[item.MediaItemID]
		if !ok {
			continue
		}

		age := now.Sub(addedAt)
		if age < 0 {
			// Added in the future (clock skew) — apply full boost.
			age = 0
		}
		if age >= boostWindow {
			continue
		}

		// Linear decay: fraction goes from 1.0 (just added) to 0.0 (at window edge).
		fraction := 1.0 - float64(age)/float64(boostWindow)
		// Multiplier ranges from RecencyBoostMultiplier down to 1.0.
		multiplier := 1.0 + (RecencyBoostMultiplier-1.0)*fraction
		boosted[i].Score *= multiplier
	}

	sort.Slice(boosted, func(i, j int) bool {
		return boosted[i].Score > boosted[j].Score
	})

	return boosted
}
