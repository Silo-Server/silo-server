package handlers

import (
	"testing"
	"time"
)

func TestPopulateInsightsLibraryFirstPlays(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	t.Run("empty", func(t *testing.T) {
		out := &AdminInsightsLibrary{}
		populateInsightsLibraryFirstPlays(nil, nil, out)
		if out.Summary.AddedTitles != 0 || out.Summary.AddedPlayed != 0 || out.Summary.MedianDaysToFirstPlay != nil {
			t.Fatalf("summary = %+v, want zeros and nil median", out.Summary)
		}
		if !labeledCountsEqual(out.TimeToFirstPlay, map[string]int64{
			"day": 0, "week": 0, "month": 0, "later": 0, "not_yet": 0,
		}) {
			t.Fatalf("buckets = %+v", out.TimeToFirstPlay)
		}
	})

	t.Run("buckets, clamped negative wait, and even median interpolation", func(t *testing.T) {
		added := []insightsAddedTitle{
			{contentID: "neg", addedAt: base},
			{contentID: "day", addedAt: base},
			{contentID: "week-edge", addedAt: base},
			{contentID: "month-edge", addedAt: base},
			{contentID: "later-edge", addedAt: base},
			{contentID: "unplayed", addedAt: base},
		}
		// Waits in days: 0, 0.5, 1, 7, 30 -> median (odd=5) = 1.0 day.
		titles := map[string]insightsTitlePlays{
			"neg":        {firstPlayAt: base.Add(-2 * time.Hour)},
			"day":        {firstPlayAt: base.Add(12 * time.Hour)},
			"week-edge":  {firstPlayAt: base.Add(24 * time.Hour)},
			"month-edge": {firstPlayAt: base.Add(7 * 24 * time.Hour)},
			"later-edge": {firstPlayAt: base.Add(30 * 24 * time.Hour)},
		}
		out := &AdminInsightsLibrary{}
		populateInsightsLibraryFirstPlays(added, titles, out)
		if out.Summary.AddedTitles != 6 || out.Summary.AddedPlayed != 5 ||
			out.Summary.MedianDaysToFirstPlay == nil || *out.Summary.MedianDaysToFirstPlay != 1.0 {
			t.Fatalf("summary = %+v (median=%v)", out.Summary, out.Summary.MedianDaysToFirstPlay)
		}
		if !labeledCountsEqual(out.TimeToFirstPlay, map[string]int64{
			"day": 2, "week": 1, "month": 1, "later": 1, "not_yet": 1,
		}) {
			t.Fatalf("buckets = %+v", out.TimeToFirstPlay)
		}

		// Even count (4): waits 0, 0.5, 2.0, 30.0 -> percentile_cont(0.5) = 1.25 -> rounds to 1.3.
		evenAdded := []insightsAddedTitle{
			{contentID: "a", addedAt: base},
			{contentID: "b", addedAt: base},
			{contentID: "c", addedAt: base},
			{contentID: "d", addedAt: base},
		}
		evenTitles := map[string]insightsTitlePlays{
			"a": {firstPlayAt: base},
			"b": {firstPlayAt: base.Add(12 * time.Hour)},
			"c": {firstPlayAt: base.Add(48 * time.Hour)},
			"d": {firstPlayAt: base.Add(30 * 24 * time.Hour)},
		}
		evenOut := &AdminInsightsLibrary{}
		populateInsightsLibraryFirstPlays(evenAdded, evenTitles, evenOut)
		if evenOut.Summary.MedianDaysToFirstPlay == nil || *evenOut.Summary.MedianDaysToFirstPlay != 1.3 {
			t.Fatalf("even median = %v, want 1.3", evenOut.Summary.MedianDaysToFirstPlay)
		}

		// PostgreSQL's percentile_cont gives 4319.999999999999 s here (0.0
		// days); a plain (lo+hi)/2 gives 4320 s and rounds up to 0.1.
		roundAdded := []insightsAddedTitle{{contentID: "a", addedAt: base}, {contentID: "b", addedAt: base}}
		roundTitles := map[string]insightsTitlePlays{
			"a": {firstPlayAt: base.Add(84302132 * time.Microsecond)},
			"b": {firstPlayAt: base.Add(8555697868 * time.Microsecond)},
		}
		roundOut := &AdminInsightsLibrary{}
		populateInsightsLibraryFirstPlays(roundAdded, roundTitles, roundOut)
		if m := roundOut.Summary.MedianDaysToFirstPlay; m == nil {
			t.Fatal("median missing")
		} else if *m != 0 {
			t.Fatalf("median = %v, want 0 as percentile_cont rounds it", *m)
		}

		// Added on a fractional second: EXTRACT(EPOCH ...) gives 2808.018777 s,
		// and percentile_cont 12959.999999999998 s (0.1 days); seconds and
		// fractions added as two floats give 12960 s (0.2 days).
		frac := base.Add(999999 * time.Microsecond)
		fracAdded := []insightsAddedTitle{{contentID: "a", addedAt: frac}, {contentID: "b", addedAt: frac}}
		fracTitles := map[string]insightsTitlePlays{
			"a": {firstPlayAt: frac.Add(2808018777 * time.Microsecond)},
			"b": {firstPlayAt: frac.Add(23111981223 * time.Microsecond)},
		}
		fracOut := &AdminInsightsLibrary{}
		populateInsightsLibraryFirstPlays(fracAdded, fracTitles, fracOut)
		if m := fracOut.Summary.MedianDaysToFirstPlay; m == nil {
			t.Fatal("median missing")
		} else if *m != 0.1 {
			t.Fatalf("median = %v, want 0.1 as percentile_cont rounds it", *m)
		}
	})
}
