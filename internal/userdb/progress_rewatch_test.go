package userdb

import (
	"database/sql"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newRewatchTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func rewatchGetProgress(t *testing.T, db *sql.DB, profileID, itemID string) *WatchProgress {
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

func rewatchInProgressIDs(t *testing.T, db *sql.DB, profileID string) []string {
	t.Helper()
	entries, err := ListProgress(db, profileID, "in_progress", 50, 0)
	if err != nil {
		t.Fatalf("ListProgress: %v", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.MediaItemID)
	}
	return ids
}

// TestProgressRewatchLifecycle pins the watch → complete → rewatch → complete
// cycle of the position-based Continue Watching model: completion resets the
// resume point to 0 and latches completed; a later heartbeat re-enters the
// in-progress set through plain MAX while completed stays true; finishing the
// rewatch resets the resume point again.
func TestProgressRewatchLifecycle(t *testing.T) {
	db := newRewatchTestDB(t)
	th := userstore.ProgressThresholds{WatchedPct: 90, MinResumePct: 5}

	// First watch in progress.
	if err := UpdateProgress(db, "p", "m", 600, 7200, th); err != nil {
		t.Fatalf("UpdateProgress(first watch): %v", err)
	}
	wp := rewatchGetProgress(t, db, "p", "m")
	if wp.Completed || wp.PositionSeconds != 600 {
		t.Fatalf("first watch: completed=%v pos=%v, want false/600", wp.Completed, wp.PositionSeconds)
	}
	if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 1 || ids[0] != "m" {
		t.Fatalf("in_progress = %v, want [m]", ids)
	}

	// Completion: watched latch set, resume point cleared, leaves Continue Watching.
	if err := UpdateProgress(db, "p", "m", 7000, 7200, th); err != nil {
		t.Fatalf("UpdateProgress(completion): %v", err)
	}
	wp = rewatchGetProgress(t, db, "p", "m")
	if !wp.Completed || wp.PositionSeconds != 0 {
		t.Fatalf("completion: completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
	}
	if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 0 {
		t.Fatalf("in_progress after completion = %v, want empty", ids)
	}

	// Rewatch: re-enters Continue Watching, watched state survives.
	if err := UpdateProgress(db, "p", "m", 900, 7200, th); err != nil {
		t.Fatalf("UpdateProgress(rewatch): %v", err)
	}
	wp = rewatchGetProgress(t, db, "p", "m")
	if !wp.Completed || wp.PositionSeconds != 900 {
		t.Fatalf("rewatch: completed=%v pos=%v, want true/900", wp.Completed, wp.PositionSeconds)
	}
	if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 1 || ids[0] != "m" {
		t.Fatalf("in_progress during rewatch = %v, want [m]", ids)
	}

	// Rewatch completes: resume point cleared again, still watched.
	if err := UpdateProgress(db, "p", "m", 7100, 7200, th); err != nil {
		t.Fatalf("UpdateProgress(rewatch completion): %v", err)
	}
	wp = rewatchGetProgress(t, db, "p", "m")
	if !wp.Completed || wp.PositionSeconds != 0 {
		t.Fatalf("rewatch completion: completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
	}
	if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 0 {
		t.Fatalf("in_progress after rewatch completion = %v, want empty", ids)
	}
}

func TestProgressRewatchGuards(t *testing.T) {
	th := userstore.ProgressThresholds{WatchedPct: 90, MinResumePct: 5}

	t.Run("zero position heartbeat does not disturb watched item", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		// Opening a watched item and backing out reports position 0.
		if err := UpdateProgress(db, "p", "m", 0, 7200, th); err != nil {
			t.Fatalf("UpdateProgress(zero): %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 0 {
			t.Fatalf("got completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
		}
		if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 0 {
			t.Fatalf("in_progress = %v, want empty", ids)
		}
	})

	t.Run("below min-resume heartbeat is discarded", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		// 100/7200 = 1.4% < 5% floor: no write, no resume entry.
		if err := UpdateProgress(db, "p", "m", 100, 7200, th); err != nil {
			t.Fatalf("UpdateProgress(below floor): %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 0 {
			t.Fatalf("got completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("mark watched cancels rewatch resume point", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		if err := UpdateProgress(db, "p", "m", 900, 7200, th); err != nil {
			t.Fatalf("UpdateProgress(rewatch): %v", err)
		}
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched(again): %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 0 {
			t.Fatalf("got completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("mark progress batch clears stale resume points", func(t *testing.T) {
		db := newRewatchTestDB(t)
		// In-progress row at 40%, then series-level mark played.
		if err := UpdateProgress(db, "p", "ep", 2880, 7200, th); err != nil {
			t.Fatalf("UpdateProgress: %v", err)
		}
		if err := MarkProgressBatch(db, "p", []string{"ep"}, time.Time{}); err != nil {
			t.Fatalf("MarkProgressBatch: %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "ep")
		if !wp.Completed || wp.PositionSeconds != 0 {
			t.Fatalf("got completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
		}
		if ids := rewatchInProgressIDs(t, db, "p"); len(ids) != 0 {
			t.Fatalf("in_progress = %v, want empty", ids)
		}
	})

	t.Run("playback stop mid-rewatch keeps the watched latch", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		// RecordPlaybackStop persists the final position via SetProgress with
		// completed=false (below the watched threshold). The latch must hold.
		if err := SetProgress(db, "p", "m", 2880, 7200, th); err != nil {
			t.Fatalf("SetProgress: %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 2880 {
			t.Fatalf("got completed=%v pos=%v, want true/2880 (latch + resume point)", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("set progress if newer keeps the watched latch", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "m", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		wrote, err := SetProgressIfNewer(db, "p", "m", 1800, 7200, false, time.Now().UTC().Add(time.Hour))
		if err != nil {
			t.Fatalf("SetProgressIfNewer: %v", err)
		}
		if !wrote {
			t.Fatalf("SetProgressIfNewer wrote = false, want true")
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 1800 {
			t.Fatalf("got completed=%v pos=%v, want true/1800 (latch + resume point)", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("stale mark batch does not zero a newer rewatch", func(t *testing.T) {
		db := newRewatchTestDB(t)
		if err := MarkWatched(db, "p", "ep", 7200); err != nil {
			t.Fatalf("MarkWatched: %v", err)
		}
		if err := UpdateProgress(db, "p", "ep", 900, 7200, th); err != nil {
			t.Fatalf("UpdateProgress(rewatch): %v", err)
		}
		// A delayed batch mark carrying an old timestamp must not clobber the
		// live rewatch resume point.
		if err := MarkProgressBatch(db, "p", []string{"ep"}, time.Now().UTC().Add(-time.Hour)); err != nil {
			t.Fatalf("MarkProgressBatch(stale): %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "ep")
		if !wp.Completed || wp.PositionSeconds != 900 {
			t.Fatalf("got completed=%v pos=%v, want true/900 (stale batch ignored)", wp.Completed, wp.PositionSeconds)
		}
	})

	t.Run("set progress at completed normalizes position to zero", func(t *testing.T) {
		db := newRewatchTestDB(t)
		// Import path (trakt/jellyfin) reporting a completed watch with a
		// raw end-of-file position.
		if err := SetProgressAt(db, "p", "m", 7200, 7200, true, time.Time{}); err != nil {
			t.Fatalf("SetProgressAt: %v", err)
		}
		wp := rewatchGetProgress(t, db, "p", "m")
		if !wp.Completed || wp.PositionSeconds != 0 {
			t.Fatalf("got completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
		}
	})
}

// TestMigrateToV11ResetsCompletedPositions verifies the v11 userdb migration:
// an existing DB (user_version 10) gets its legacy completed rows (position
// pinned to duration) reset to 0, and the version gate prevents re-runs from
// wiping rewatch state.
func TestMigrateToV11ResetsCompletedPositions(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Simulate an existing pre-v11 database: schema already at v10 with a
	// legacy completed row written under the old model.
	if _, err := db.Exec("PRAGMA user_version = 10"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ('p', 'legacy', 7200, 7200, 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	wp := rewatchGetProgress(t, db, "p", "legacy")
	if !wp.Completed || wp.PositionSeconds != 0 {
		t.Fatalf("after migration: completed=%v pos=%v, want true/0", wp.Completed, wp.PositionSeconds)
	}
	version, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// A rewatch in flight must survive subsequent runs (the version gate
	// blocks re-runs).
	th := userstore.ProgressThresholds{WatchedPct: 90, MinResumePct: 5}
	if err := UpdateProgress(db, "p", "legacy", 900, 7200, th); err != nil {
		t.Fatalf("UpdateProgress(rewatch): %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations(again): %v", err)
	}
	wp = rewatchGetProgress(t, db, "p", "legacy")
	if !wp.Completed || wp.PositionSeconds != 900 {
		t.Fatalf("after re-run: completed=%v pos=%v, want true/900 (rewatch preserved)", wp.Completed, wp.PositionSeconds)
	}
}
