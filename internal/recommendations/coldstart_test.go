package recommendations

import (
	"slices"
	"testing"
)

// Levels count positive titles: 1-2 stay cold-first, 3 (the taste-seed
// picker's minimum) put the profile's own row first, 10 make it fully
// personalized.
func TestColdStartLevelBoundaries(t *testing.T) {
	for titles, want := range map[int]int{0: 0, 1: 1, 2: 1, 3: 2, 9: 2, 10: 3, 40: 3} {
		if got := coldStartLevel(titles); got != want {
			t.Errorf("coldStartLevel(%d) = %d, want %d", titles, got, want)
		}
	}
}

// coldStartLevelOf uses the positive titles a refresh recorded, alone, and
// falls back to the positive signal count for a row stored before refreshes
// recorded them.
func TestColdStartLevelOfCountsPositiveTitles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts map[string]int
		want   int
	}{
		{"three taste-seed picks", map[string]int{"favorited": 3, signalCountPositiveTitles: 3}, 2},
		{"one title rated, favorited and finished", map[string]int{"rated_5": 1, "favorited": 1, "watch_high": 1, signalCountPositiveTitles: 1}, 1},
		{"one long series", map[string]int{"watch_high": 1, "rewatch": 1, signalCountPositiveTitles: 1}, 1},
		{"watchlist only", map[string]int{"watchlist": 3, signalCountPositiveTitles: 0}, 0},
		{"positive signals awaiting embeddings", map[string]int{"favorited": 5, signalCountPositiveTitles: 0}, 0},
		{"ten titles", map[string]int{"watch_high": 7, "rated_4": 3, signalCountPositiveTitles: 10}, 3},
		{"legacy row", map[string]int{"favorited": 2, "watch_high": 1, "watch_low": 4}, 2},
		{"legacy row of dislikes and 3-star ratings", map[string]int{"rated_low": 2, "watch_low": 3, "rated_3": 4}, 0},
		{"legacy row past the top threshold", map[string]int{"watch_high": ColdStartFullPersonalized}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coldStartLevelOf(&TasteProfileMeta{SignalCounts: tc.counts}); got != tc.want {
				t.Fatalf("level = %d, want %d", got, tc.want)
			}
		})
	}
	if got := coldStartLevelOf(nil); got != 0 {
		t.Fatalf("level without a taste profile = %d, want 0", got)
	}
}

// Level 0 serves global rows only, level 1 global rows then one personal
// row, level 2 alternates starting with a personal row, and level 3 puts
// every personal row first.
func TestMergePersonalizedAndColdStartOrderByLevel(t *testing.T) {
	rows := func(labels ...string) []ForYouRow {
		out := make([]ForYouRow, len(labels))
		for i, label := range labels {
			out[i] = ForYouRow{Label: label}
		}
		return out
	}
	personal := rows("main", "cluster-0", "cluster-1")
	cold := rows("popular", "recent")
	for level, want := range map[int][]string{
		0: {"popular", "recent"},
		1: {"popular", "recent", "main"},
		2: {"main", "popular", "cluster-0", "recent", "cluster-1"},
		3: {"main", "cluster-0", "cluster-1", "popular", "recent"},
	} {
		if got := rowLabels(mergePersonalizedAndColdStart(personal, cold, level)); !slices.Equal(got, want) {
			t.Errorf("level %d rows = %v, want %v", level, got, want)
		}
	}
}
