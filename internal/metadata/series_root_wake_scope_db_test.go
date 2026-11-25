package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A complete item refresh wakes the backed-off series roots of its scope, so
// its own scan matches them (#5337): a root at or under the scope, and a root
// above a season folder scope whose files are in it. A root outside the scope
// stays backed off, and a leased row keeps its available_at.
func TestSeriesRootQueueWakeInScopeDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("/test/wake-scope-%d", time.Now().UnixNano())
	folderID := insertTestFolder(t, pool, "series")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM series_root_match_queue WHERE media_folder_id = $1`, folderID)
	})
	series := NewSeriesRootMatchQueueRepository(pool)

	show, other, leased := prefix+"/Example Show", prefix+"/Other Show", prefix+"/Leased Show"
	for _, root := range []string{show, other, leased} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, observed_root_path, base_type, content_group_key, file_size)
			VALUES ($1, $2, $3, 'series', $4, 0)
		`, folderID, root+"/Season 01/Show.S01E01.mkv", root, "group-"+root); err != nil {
			t.Fatalf("seed series file: %v", err)
		}
		if err := series.EnqueueSeriesRoot(ctx, folderID, root); err != nil {
			t.Fatalf("enqueue %s: %v", root, err)
		}
	}
	backOff := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET available_at = NOW() + interval '24 hours',
			failure_kind = 'no_candidates', last_error = 'no candidates', deterministic_attempt_count = 3, lease_token = ''
			WHERE media_folder_id = $1`, folderID); err != nil {
			t.Fatalf("back off rows: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET lease_token = 'held'
			WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, leased); err != nil {
			t.Fatalf("lease row: %v", err)
		}
	}
	awake := func(t *testing.T, root string) bool {
		t.Helper()
		var availableAt time.Time
		var failureKind string
		if err := pool.QueryRow(ctx, `SELECT available_at, failure_kind FROM series_root_match_queue
			WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, root).Scan(&availableAt, &failureKind); err != nil {
			t.Fatalf("load %s: %v", root, err)
		}
		return !availableAt.After(time.Now().Add(time.Minute)) && failureKind == ""
	}

	for _, tc := range []struct {
		name  string
		scope string
	}{
		{name: "series root scope", scope: show},
		{name: "season folder scope", scope: show + "/Season 01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backOff(t)
			if err := series.WakeInScope(ctx, folderID, tc.scope); err != nil {
				t.Fatalf("WakeInScope: %v", err)
			}
			if !awake(t, show) {
				t.Fatalf("the scope's root %s stayed backed off", show)
			}
			if awake(t, other) {
				t.Fatalf("a root outside the scope woke")
			}
		})
	}

	// The refresh's scoped match claims only a row that is due, so a backed-off
	// root is matched only once it is woken.
	t.Run("scoped claim", func(t *testing.T) {
		backOff(t)
		scope := show + "/Season 01"
		jobs, err := series.ClaimByFolderAndPathPrefix(ctx, folderID, scope, 10, time.Time{})
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim of a backed-off root = %+v, %v; want none", jobs, err)
		}
		if err := series.WakeInScope(ctx, folderID, scope); err != nil {
			t.Fatalf("WakeInScope: %v", err)
		}
		jobs, err = series.ClaimByFolderAndPathPrefix(ctx, folderID, scope, 10, time.Time{})
		if err != nil || len(jobs) != 1 || jobs[0].ObservedRootPath != show {
			t.Fatalf("claim after the wake = %+v, %v; want the root %s", jobs, err, show)
		}
	})

	t.Run("leased row", func(t *testing.T) {
		backOff(t)
		if err := series.WakeInScope(ctx, folderID, leased); err != nil {
			t.Fatalf("WakeInScope: %v", err)
		}
		var availableAt time.Time
		if err := pool.QueryRow(ctx, `SELECT available_at FROM series_root_match_queue
			WHERE media_folder_id = $1 AND observed_root_path = $2`, folderID, leased).Scan(&availableAt); err != nil {
			t.Fatalf("load leased row: %v", err)
		}
		if !availableAt.After(time.Now().Add(time.Hour)) {
			t.Fatalf("a leased row's available_at moved to %v, want it kept", availableAt)
		}
	})
}
