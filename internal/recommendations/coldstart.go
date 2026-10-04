package recommendations

import (
	"sort"
	"time"
)

// coldStartLevel returns the cold-start graduation level for a number of
// positive titles. Higher levels indicate more personalization is
// appropriate.
//
//	0 titles → level 0 (global rows only)
//	1-2      → level 1 (global rows, then one personal row)
//	3-9      → level 2 (personal and global rows interleaved, personal first)
//	10+      → level 3 (personal rows first)
func coldStartLevel(positiveTitles int) int {
	switch {
	case positiveTitles >= ColdStartFullPersonalized:
		return 3
	case positiveTitles >= ColdStartMixed:
		return 2
	case positiveTitles >= ColdStartMinimal:
		return 1
	default:
		return 0
	}
}

// signal_counts kinds. A 1-2 star rating (rated_low) and a title abandoned
// below lowProgressThreshold (watch_low) record a dislike; a 3-star rating
// carries no weight of its own (see ratingSignal).
const (
	signalKindRated5   = "rated_5"
	signalKindRated4   = "rated_4"
	signalKindRated3   = "rated_3"
	signalKindRatedLow = "rated_low"
	signalKindWatchLow = "watch_low"
)

// signalCountPositiveTitles is the signal_counts entry a taste refresh writes
// with the number of titles behind the profile's taste vector that count
// toward its level: canonical titles with an embedding and a positive weight,
// each once however many signals it has, leaving out titles that are only on
// the watchlist (see countsTowardLevel). It is not a kind of signal, so the
// taste-profile summary leaves it out (summarySignalCounts).
const signalCountPositiveTitles = "positive_titles"

// coldStartLevelOf returns the cold-start level of a profile from its taste
// profile metadata. A profile with no taste profile is level 0. The level
// comes from the profile's positive titles; a row stored before refreshes
// recorded them, which has no signalCountPositiveTitles entry, falls back to
// its positive signal count until its next refresh.
func coldStartLevelOf(meta *TasteProfileMeta) int {
	if meta == nil {
		return 0
	}
	if titles, ok := meta.SignalCounts[signalCountPositiveTitles]; ok {
		return coldStartLevel(titles)
	}
	return coldStartLevel(positiveSignalCount(meta))
}

// positiveSignalCount sums a profile's positive signal counts: every kind
// but the dislikes and 3-star ratings. A profile with none has nothing a
// refresh could build a taste vector from.
func positiveSignalCount(meta *TasteProfileMeta) int {
	if meta == nil {
		return 0
	}
	positive := 0
	for kind, count := range meta.SignalCounts {
		switch kind {
		case signalKindRatedLow, signalKindWatchLow, signalKindRated3, signalCountPositiveTitles:
			continue
		}
		positive += count
	}
	return positive
}

// buildColdStartRows builds the set of non-personalized recommendation rows
// used during cold-start (and appended to warm profiles for discovery).
// Rows with empty item slices are omitted.
func buildColdStartRows(popular, recentlyAdded []ScoredItem, genreSamplers map[string][]ScoredItem) []ForYouRow {
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
// are left unchanged. The returned slice is a new copy in the order of items:
// re-sorting by score would undo the order MMR chose.
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

	return boosted
}
