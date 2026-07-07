package userdb

import (
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestWatchHistoryIdentityRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	seasonNumber := 2
	episodeNumber := 7
	entry := userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     "episode-1",
		WatchedAt:       "2026-04-25T12:00:00Z",
		DurationSeconds: 1800,
		Completed:       true,
		Source:          userstore.WatchHistorySourcePlayback,
		Identity: userstore.WatchIdentity{
			StableType:        "episode",
			SeriesProviderIDs: map[string]string{"tmdb": "123", "tvdb": "456"},
			Season:            &seasonNumber,
			Episode:           &episodeNumber,
		},
	}
	if err := AddHistory(db, entry); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	history, err := ListHistory(db, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	got := history[0]
	if got.Identity.StableType != "episode" {
		t.Fatalf("Identity.StableType = %q, want episode", got.Identity.StableType)
	}
	if len(got.Identity.ProviderIDs) != 0 {
		t.Fatalf("Identity.ProviderIDs = %#v, want empty", got.Identity.ProviderIDs)
	}
	if !reflect.DeepEqual(got.Identity.SeriesProviderIDs, entry.Identity.SeriesProviderIDs) {
		t.Fatalf("Identity.SeriesProviderIDs = %#v, want %#v", got.Identity.SeriesProviderIDs, entry.Identity.SeriesProviderIDs)
	}
	if got.Identity.Season == nil || *got.Identity.Season != seasonNumber {
		t.Fatalf("Identity.Season = %v, want %d", got.Identity.Season, seasonNumber)
	}
	if got.Identity.Episode == nil || *got.Identity.Episode != episodeNumber {
		t.Fatalf("Identity.Episode = %v, want %d", got.Identity.Episode, episodeNumber)
	}
}

func TestAddHistoryIfMissingKeepsExistingSemantics(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	entry := userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     "movie-1",
		WatchedAt:       "2026-04-25T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceImport,
		Identity: userstore.WatchIdentity{
			StableType:  "movie",
			ProviderIDs: map[string]string{"tmdb": "603"},
		},
	}
	created, err := AddHistoryIfMissing(db, entry)
	if err != nil {
		t.Fatalf("AddHistoryIfMissing(first): %v", err)
	}
	if !created {
		t.Fatal("AddHistoryIfMissing(first) created = false, want true")
	}
	entry.Identity.ProviderIDs = map[string]string{"tmdb": "999"}
	created, err = AddHistoryIfMissing(db, entry)
	if err != nil {
		t.Fatalf("AddHistoryIfMissing(second): %v", err)
	}
	if created {
		t.Fatal("AddHistoryIfMissing(second) created = true, want false")
	}

	history, err := ListHistory(db, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if got := history[0].Identity.ProviderIDs["tmdb"]; got != "603" {
		t.Fatalf("stored tmdb id = %q, want 603", got)
	}
}

func TestListCompletedHistoryAppliesScopedFilters(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	entries := []userstore.WatchHistoryEntry{
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-1",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourcePlayback,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-2",
			WatchedAt:       "2026-04-26T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceTrakt,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-3",
			WatchedAt:       "2026-04-27T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceImport,
		},
	}
	for _, entry := range entries {
		if err := AddHistory(db, entry); err != nil {
			t.Fatalf("AddHistory(%s): %v", entry.MediaItemID, err)
		}
	}

	history, err := ListCompletedHistory(db, userstore.CompletedHistoryQuery{
		ProfileID:      "profile-1",
		MediaItemIDs:   []string{"movie-1", "movie-2"},
		IncludeSources: []userstore.WatchHistorySource{userstore.WatchHistorySourcePlayback, userstore.WatchHistorySourceTrakt},
		ExcludeSources: []userstore.WatchHistorySource{userstore.WatchHistorySourceTrakt},
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("ListCompletedHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1: %+v", len(history), history)
	}
	if history[0].MediaItemID != "movie-1" {
		t.Fatalf("media item = %q, want movie-1", history[0].MediaItemID)
	}
}

func TestListCompletedHistoryItemsAppliesScopedFilters(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	entries := []userstore.WatchHistoryEntry{
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-history-only",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceTrakt,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-hidden",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceSimkl,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-future-hidden",
			WatchedAt:       "2026-04-25T12:10:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceTrakt,
		},
		{
			ProfileID:       "profile-2",
			MediaItemID:     "movie-other-profile",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceTrakt,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-incomplete",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       false,
			Source:          userstore.WatchHistorySourceTrakt,
		},
		{
			ProfileID:       "profile-1",
			MediaItemID:     "movie-playback",
			WatchedAt:       "2026-04-25T12:00:00Z",
			DurationSeconds: 7200,
			Completed:       true,
			Source:          userstore.WatchHistorySourcePlayback,
		},
	}
	for _, entry := range entries {
		if err := AddHistory(db, entry); err != nil {
			t.Fatalf("AddHistory(%s): %v", entry.MediaItemID, err)
		}
	}
	if err := RemoveHistoryItems(db, "profile-1", []string{"movie-hidden"}, time.Date(2026, 4, 25, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("RemoveHistoryItems: %v", err)
	}
	if err := RemoveHistoryItems(db, "profile-1", []string{"movie-future-hidden"}, time.Date(2026, 4, 25, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("RemoveHistoryItems(future): %v", err)
	}

	items, err := ListCompletedHistoryItems(db, userstore.CompletedHistoryItemQuery{
		ProfileID: "profile-1",
		MediaItemIDs: []string{
			"movie-history-only",
			"movie-hidden",
			"movie-future-hidden",
			"movie-other-profile",
			"movie-incomplete",
			"movie-missing",
		},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems: %v", err)
	}
	if len(items) != 1 || items[0].MediaItemID != "movie-history-only" || items[0].WatchedAt != "2026-04-25T12:00:00Z" {
		t.Fatalf("ListCompletedHistoryItems = %v, want movie-history-only with latest watched_at", items)
	}

	if err := AddHistory(db, userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     "movie-future-hidden",
		WatchedAt:       "2026-04-25T12:11:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourcePlayback,
	}); err != nil {
		t.Fatalf("AddHistory(future replacement): %v", err)
	}
	items, err = ListCompletedHistoryItems(db, userstore.CompletedHistoryItemQuery{
		ProfileID:    "profile-1",
		MediaItemIDs: []string{"movie-future-hidden"},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems(future replacement): %v", err)
	}
	if len(items) != 1 || items[0].MediaItemID != "movie-future-hidden" || items[0].WatchedAt != "2026-04-25T12:11:00Z" {
		t.Fatalf("ListCompletedHistoryItems(future replacement) = %v, want movie-future-hidden with latest watched_at", items)
	}

	items, err = ListCompletedHistoryItems(db, userstore.CompletedHistoryItemQuery{
		ProfileID:      "profile-1",
		MediaItemIDs:   []string{"movie-history-only", "movie-playback"},
		IncludeSources: []userstore.WatchHistorySource{userstore.WatchHistorySourceTrakt, userstore.WatchHistorySourcePlayback},
		ExcludeSources: []userstore.WatchHistorySource{userstore.WatchHistorySourcePlayback},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems(source filters): %v", err)
	}
	if len(items) != 1 || items[0].MediaItemID != "movie-history-only" {
		t.Fatalf("ListCompletedHistoryItems(source filters) = %v, want [movie-history-only]", items)
	}
}

func TestMarkProgressBatch_CompactsDirtyInput(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	at := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if err := MarkProgressBatch(db, "p1", []string{"a", "", "a", "  "}, at); err != nil {
		t.Fatalf("MarkProgressBatch: %v", err)
	}

	wpA, err := GetProgress(db, "p1", "a")
	if err != nil {
		t.Fatalf("GetProgress(a): %v", err)
	}
	if wpA == nil || !wpA.Completed {
		t.Fatalf("GetProgress(a) = %+v, want completed", wpA)
	}

	if wpEmpty, err := GetProgress(db, "p1", ""); err != nil {
		t.Fatalf("GetProgress(empty): %v", err)
	} else if wpEmpty != nil {
		t.Fatalf("GetProgress(empty) = %+v, want nil — empty IDs must be compacted", wpEmpty)
	}
	if wpWS, err := GetProgress(db, "p1", "  "); err != nil {
		t.Fatalf("GetProgress(whitespace): %v", err)
	} else if wpWS != nil {
		t.Fatalf("GetProgress(whitespace) = %+v, want nil — whitespace IDs must be compacted", wpWS)
	}

	// Sanity: only one row should exist for profile p1.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM watch_progress WHERE profile_id = ?`, "p1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("watch_progress row count = %d, want 1 (dirty IDs leaked into the table)", count)
	}
}

// TestClearProgressBatch_ClearsPartiallyWatchedRows pins that the batch
// path resets resume position even on rows where completed=false. The
// prior single-item ClearProgress path DELETEd the row unconditionally;
// when handlePlayedMutation routes single-item unplayed actions through
// the batch path, partially-watched rows must still have their resume
// position cleared. Otherwise "mark unplayed" leaves the item in
// Continue Watching with the user's last position intact.
//
// Regression guard for the post-perf-overhaul code review (Codex P1).
func TestClearProgressBatch_ClearsPartiallyWatchedRows(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Seed a partially-watched row: completed=false, position=300.
	thresholds := userstore.ProgressThresholds{WatchedPct: 90}
	if err := UpdateProgress(db, "p1", "m1", 300, 1800, thresholds); err != nil {
		t.Fatalf("seed UpdateProgress: %v", err)
	}
	wp, err := GetProgress(db, "p1", "m1")
	if err != nil {
		t.Fatalf("seed GetProgress: %v", err)
	}
	if wp == nil || wp.Completed || wp.PositionSeconds == 0 {
		t.Fatalf("seed expected partially-watched (completed=false, position>0); got %+v", wp)
	}

	// Mark unplayed via the batch path.
	at := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if err := ClearProgressBatch(db, "p1", []string{"m1"}, at); err != nil {
		t.Fatalf("ClearProgressBatch: %v", err)
	}

	// Resume position must be cleared. Without the fix, position_seconds
	// would still be 300 because the prior `AND completed = 1` filter
	// excluded this row.
	wp, err = GetProgress(db, "p1", "m1")
	if err != nil {
		t.Fatalf("post-clear GetProgress: %v", err)
	}
	if wp == nil {
		t.Fatalf("expected row to still exist after batch clear (UPDATE, not DELETE)")
	}
	if wp.PositionSeconds != 0 {
		t.Fatalf("position_seconds = %v, want 0 (partial-progress not cleared by batch path)", wp.PositionSeconds)
	}
	if wp.Completed {
		t.Fatalf("completed = true, want false")
	}
}

func TestClearProgressBatch_CompactsDirtyInput(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	at := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if err := MarkProgressBatch(db, "p1", []string{"a"}, at); err != nil {
		t.Fatalf("seed MarkProgressBatch: %v", err)
	}

	if err := ClearProgressBatch(db, "p1", []string{"a", "", "a", "  "}, at); err != nil {
		t.Fatalf("ClearProgressBatch: %v", err)
	}

	wpA, err := GetProgress(db, "p1", "a")
	if err != nil {
		t.Fatalf("GetProgress(a): %v", err)
	}
	if wpA == nil || wpA.Completed {
		t.Fatalf("GetProgress(a) = %+v, want completed=false", wpA)
	}

	// Sanity: still only one row, and no dirty placeholder leaked in.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM watch_progress WHERE profile_id = ?`, "p1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("watch_progress row count = %d, want 1", count)
	}
}

// Backward seeks are legitimate resume points: UpdateProgress is
// last-write-wins on position (the old MAX clamp made "rewind and stop"
// resume at the stale later position). Mirrors the pgstore expectation.
func TestUpdateProgressBackwardSeekPersists(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	noThreshold := userstore.ProgressThresholds{}
	if err := UpdateProgress(db, "p1", "movie-1", 2400, 7200, noThreshold); err != nil {
		t.Fatalf("UpdateProgress(forward): %v", err)
	}
	if err := UpdateProgress(db, "p1", "movie-1", 600, 7200, noThreshold); err != nil {
		t.Fatalf("UpdateProgress(backward): %v", err)
	}

	wp, err := GetProgress(db, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if wp.PositionSeconds != 600 {
		t.Errorf("PositionSeconds = %v, want 600 (last write wins)", wp.PositionSeconds)
	}
}
