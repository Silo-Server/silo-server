package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// wakeScopeFixture is a series library whose roots each hold one episode file
// and a queued row.
type wakeScopeFixture struct {
	pool     *pgxpool.Pool
	series   *SeriesRootMatchQueueRepository
	folderID int
}

func newWakeScopeFixture(t *testing.T, roots ...string) wakeScopeFixture {
	t.Helper()
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()
	f := wakeScopeFixture{pool: pool, series: NewSeriesRootMatchQueueRepository(pool), folderID: insertTestFolder(t, pool, "series")}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM series_root_match_queue WHERE media_folder_id = $1`, f.folderID)
	})
	for _, root := range roots {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, observed_root_path, base_type, content_group_key, file_size)
			VALUES ($1, $2, $3, 'series', $4, 0)
		`, f.folderID, root+"/Season 01/Show.S01E01.mkv", root, "group-"+root); err != nil {
			t.Fatalf("seed series file: %v", err)
		}
		if err := f.series.EnqueueSeriesRoot(ctx, f.folderID, root); err != nil {
			t.Fatalf("enqueue %s: %v", root, err)
		}
	}
	return f
}

// backOff backs every row off as an earlier no_candidates failure does, and
// leases the leased roots to a worker.
func (f wakeScopeFixture) backOff(t *testing.T, leased ...string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE series_root_match_queue SET available_at = NOW() + interval '24 hours',
		failure_kind = 'no_candidates', last_error = 'no candidates', deterministic_attempt_count = 3, lease_token = ''
		WHERE media_folder_id = $1`, f.folderID); err != nil {
		t.Fatalf("back off rows: %v", err)
	}
	for _, root := range leased {
		if _, err := f.pool.Exec(context.Background(), `UPDATE series_root_match_queue SET lease_token = 'held'
			WHERE media_folder_id = $1 AND observed_root_path = $2`, f.folderID, root); err != nil {
			t.Fatalf("lease row: %v", err)
		}
	}
}

// row reads a root's available_at, failure_kind and rerun_requested.
func (f wakeScopeFixture) row(t *testing.T, root string) (time.Time, string, bool) {
	t.Helper()
	var availableAt time.Time
	var failureKind string
	var rerun bool
	if err := f.pool.QueryRow(context.Background(), `SELECT available_at, failure_kind, rerun_requested FROM series_root_match_queue
		WHERE media_folder_id = $1 AND observed_root_path = $2`, f.folderID, root).Scan(&availableAt, &failureKind, &rerun); err != nil {
		t.Fatalf("load %s: %v", root, err)
	}
	return availableAt, failureKind, rerun
}

func (f wakeScopeFixture) awake(t *testing.T, root string) bool {
	t.Helper()
	availableAt, failureKind, _ := f.row(t, root)
	return !availableAt.After(time.Now().Add(time.Minute)) && failureKind == ""
}

// A complete item refresh wakes the backed-off series roots of its scope, so
// its own scan matches them (#5337): a root at or under the scope, and a root
// above a season folder scope whose files are in it. A root outside the scope
// stays backed off.
func TestSeriesRootQueueWakeInScopeDB(t *testing.T) {
	prefix := fmt.Sprintf("/test/wake-scope-%d", time.Now().UnixNano())
	show, other := prefix+"/Example Show", prefix+"/Other Show"
	f := newWakeScopeFixture(t, show, other)
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		scope string
	}{
		{name: "series root scope", scope: show},
		{name: "season folder scope", scope: show + "/Season 01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.backOff(t)
			if err := f.series.WakeInScope(ctx, f.folderID, tc.scope); err != nil {
				t.Fatalf("WakeInScope: %v", err)
			}
			if !f.awake(t, show) {
				t.Fatalf("the scope's root %s stayed backed off", show)
			}
			if f.awake(t, other) {
				t.Fatalf("a root outside the scope woke")
			}
		})
	}

	// The refresh's scoped match claims only a row that is due, so a backed-off
	// root is matched only once it is woken.
	t.Run("scoped claim", func(t *testing.T) {
		f.backOff(t)
		scope := show + "/Season 01"
		jobs, err := f.series.ClaimByFolderAndPathPrefix(ctx, f.folderID, scope, 10, time.Time{})
		if err != nil || len(jobs) != 0 {
			t.Fatalf("claim of a backed-off root = %+v, %v; want none", jobs, err)
		}
		if err := f.series.WakeInScope(ctx, f.folderID, scope); err != nil {
			t.Fatalf("WakeInScope: %v", err)
		}
		jobs, err = f.series.ClaimByFolderAndPathPrefix(ctx, f.folderID, scope, 10, time.Time{})
		if err != nil || len(jobs) != 1 || jobs[0].ObservedRootPath != show {
			t.Fatalf("claim after the wake = %+v, %v; want the root %s", jobs, err, show)
		}
	})
}

// A row a worker holds while the refresh wakes its scope keeps its
// available_at and is asked to rerun, so the worker's failure leaves it due
// rather than backing it off again.
func TestSeriesRootQueueWakeInScopeRerunsALeasedRowDB(t *testing.T) {
	leased := fmt.Sprintf("/test/wake-scope-leased-%d/Leased Show", time.Now().UnixNano())
	f := newWakeScopeFixture(t, leased)
	ctx := context.Background()
	f.backOff(t, leased)

	if err := f.series.WakeInScope(ctx, f.folderID, leased); err != nil {
		t.Fatalf("WakeInScope: %v", err)
	}
	if availableAt, _, rerun := f.row(t, leased); !availableAt.After(time.Now().Add(time.Hour)) || !rerun {
		t.Fatalf("leased row available_at = %v rerun_requested = %v, want available_at kept and a rerun requested", availableAt, rerun)
	}
	if err := f.series.UpdateFailure(ctx, f.folderID, leased, "held", MatchFailure{Kind: MatchOutcomeNoCandidates, Message: "no candidates"}); err != nil {
		t.Fatalf("UpdateFailure: %v", err)
	}
	if !f.awake(t, leased) {
		t.Fatalf("the leased worker's failure restored the back-off")
	}
}
