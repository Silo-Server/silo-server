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

// newProgressDB opens an in-memory SQLite DB with the schema initialised.
func newProgressDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

// seedProgressRow inserts/overwrites a watch_progress row with an explicit
// updated_at so the RestartMinGapSeconds time-guard can be exercised
// deterministically.
func seedProgressRow(t *testing.T, db *sql.DB, profileID, itemID string, pos, dur float64, completed bool, updatedAt time.Time) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = excluded.completed,
			updated_at = excluded.updated_at`,
		profileID, itemID, pos, dur, completed, updatedAt.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func mustGetProgress(t *testing.T, db *sql.DB, profileID, itemID string) *WatchProgress {
	t.Helper()
	wp, err := GetProgress(db, profileID, itemID)
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if wp == nil {
		t.Fatalf("GetProgress(%s) = nil, want row", itemID)
	}
	return wp
}

// TestUpdateProgress_RewatchRestartDetect pins the rewatch latch-release
// behaviour added to fix "Continue Watching" not showing re-watched items.
// A finished item must re-enter the in-progress set (completed=false) when an
// early-position heartbeat arrives well after completion, while genuine
// completions, late heartbeats, and recent-completion stale heartbeats must NOT
// release the latch.
func TestUpdateProgress_RewatchRestartDetect(t *testing.T) {
	// Pin the clock so the RestartMinGapSeconds time-gap guard is exercised
	// deterministically: UpdateProgress stamps rows with this fixed "now", and
	// the seed timestamps below are positioned relative to it.
	fixedNow := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	origNow := nowUTC
	nowUTC = func() string { return fixedNow.Format(time.RFC3339) }
	t.Cleanup(func() { nowUTC = origNow })

	th := userstore.ProgressThresholds{WatchedPct: 90, MinResumePct: 5}
	old := fixedNow.Add(-2 * time.Minute) // > RestartMinGapSeconds before fixedNow

	t.Run("in_progress row stays in progress", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 300, 3600, false, old)
		if err := UpdateProgress(db, "p", "m", 600, 3600, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if wp.Completed || wp.PositionSeconds != 600 {
			t.Fatalf("got completed=%v pos=%v, want completed=false pos=600", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("rewatch releases latch and adopts fresh position", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 7200, 7200, true, old)
		// 600/7200 = 8.3%: above the 5% min-resume floor, below the 50%
		// restart-detect threshold -> genuine rewatch, releases the latch.
		if err := UpdateProgress(db, "p", "m", 600, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if wp.Completed || wp.PositionSeconds != 600 {
			t.Fatalf("got completed=%v pos=%v, want completed=false pos=600", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("late heartbeat does not release latch", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 7200, 7200, true, old)
		// 5000/7200 = 69% > RestartDetectFraction (50%) -> not a restart.
		if err := UpdateProgress(db, "p", "m", 5000, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 7200 {
			t.Fatalf("got completed=%v pos=%v, want completed=true pos=7200", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("recent completion blocks restart (time guard)", func(t *testing.T) {
		db := newProgressDB(t)
		recent := fixedNow.Add(-5 * time.Second) // < RestartMinGapSeconds before fixedNow
		seedProgressRow(t, db, "p", "m", 7200, 7200, true, recent)
		// Early position but only seconds after completion -> stale heartbeat,
		// must NOT release the latch.
		if err := UpdateProgress(db, "p", "m", 300, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 7200 {
			t.Fatalf("got completed=%v pos=%v, want completed=true pos=7200 (time guard)", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("completion still latches forward", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 300, 7200, false, old)
		// 6600/7200 = 91.7% > WatchedFraction (90%) -> completes.
		if err := UpdateProgress(db, "p", "m", 6600, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 7200 {
			t.Fatalf("got completed=%v pos=%v, want completed=true pos=7200", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("multi-version duration change uses GREATEST duration", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 7200, 7200, true, old)
		// Director's cut: 4000 < MAX(9000,7200)*0.5 = 4500 -> restart.
		if err := UpdateProgress(db, "p", "m", 4000, 9000, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if wp.Completed || wp.PositionSeconds != 4000 {
			t.Fatalf("got completed=%v pos=%v, want completed=false pos=4000", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("stored duration zero falls back to heartbeat duration", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 0, 0, true, old)
		// 300/3600 = 8.3% passes min-resume; 300 < MAX(3600,0)*0.5 = 1800 -> restart.
		if err := UpdateProgress(db, "p", "m", 300, 3600, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if wp.Completed || wp.PositionSeconds != 300 {
			t.Fatalf("got completed=%v pos=%v, want completed=false pos=300", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("below min-resume heartbeat is discarded", func(t *testing.T) {
		db := newProgressDB(t)
		seedProgressRow(t, db, "p", "m", 7200, 7200, true, old)
		// 100/7200 = 1.4% < MinResumeFraction (5%) -> early return, no write.
		if err := UpdateProgress(db, "p", "m", 100, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		wp := mustGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 7200 {
			t.Fatalf("got completed=%v pos=%v, want unchanged completed=true pos=7200", wp.Completed, wp.PositionSeconds)
		}
	})
}
