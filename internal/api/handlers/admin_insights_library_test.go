package handlers

import (
	"strings"
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
	})
}

func TestViewingRowsSQLMediaFilesJoinGuard(t *testing.T) {
	win := resolvedInsightsWindow{
		From: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
	}

	var args []any
	with, _ := newInsightsPlaysScope(win, AdminInsightsWindowQuery{}).viewingRowsSQL(&args)
	if strings.Contains(with, "media_files") || strings.Contains(with, "file_library_id") {
		t.Fatalf("unfiltered viewingRowsSQL unexpectedly joined media_files")
	}

	args = nil
	withLib, countedLib := newInsightsPlaysScope(win, AdminInsightsWindowQuery{LibraryID: 42}).viewingRowsSQL(&args)
	if !strings.Contains(withLib, "LEFT JOIN media_files mf") || !strings.Contains(countedLib, "file_library_id") {
		t.Fatalf("library-filtered viewingRowsSQL missing media_files join or attribution")
	}

	args = nil
	scope := newInsightsPlaysScope(win, AdminInsightsWindowQuery{})
	scope.needFileLibrary = true
	withProj, _ := scope.viewingRowsSQL(&args)
	if !strings.Contains(withProj, "LEFT JOIN media_files mf") || !strings.Contains(withProj, "file_library_id") {
		t.Fatalf("needFileLibrary viewingRowsSQL missing media_files join or projection")
	}
}
