package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// taskHistoryTestPool connects to the migrated test database. Tests here share
// one real task_executions table, so every test namespaces its rows behind
// unique task keys and deletes them again on cleanup.
func taskHistoryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// taskKey returns a key unique to this test run and registers its cleanup.
func taskKey(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	key := fmt.Sprintf("test_%s_%s_%d", t.Name(), name, time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM task_executions WHERE task_key = $1`, key)
	})
	return key
}

func insertTaskExecution(t *testing.T, pool *pgxpool.Pool, key string, completedAt time.Time) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO task_executions (
			task_key, started_at, completed_at, status, duration_ms
		) VALUES ($1, $2, $2, 'completed', 0)
		RETURNING id`, key, completedAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert task execution: %v", err)
	}
	return id
}

// clearTaskHistory empties the table so a whole-table Prune has a deterministic
// batch budget. The migrated SILO_TEST_DATABASE_URL database is dedicated to
// tests, and package tests run sequentially.
func clearTaskHistory(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DELETE FROM task_executions`); err != nil {
		t.Fatalf("clear task execution history: %v", err)
	}
}

func remainingTaskExecutionIDs(t *testing.T, pool *pgxpool.Pool, key string) []int64 {
	t.Helper()
	return queryIDs(t, pool, `SELECT id FROM task_executions WHERE task_key = $1 ORDER BY id`, key)
}

func queryIDs(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query task execution IDs: %v", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan task execution ID: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task execution IDs: %v", err)
	}
	return ids
}

// pruneOneKey runs the bounded delete loop for a single key without taking the
// advisory lock, so the individual predicate cases stay readable.
func pruneOneKey(
	t *testing.T,
	repo *PgExecutionRepository,
	key string,
	keepPerTask int,
	cutoff time.Time,
	batchSize int,
) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bounds, ok, err := repo.loadDoomedBounds(ctx, key, keepPerTask, cutoff)
	if err != nil {
		t.Fatalf("loadDoomedBounds: %v", err)
	}
	if !ok {
		return 0
	}
	var total int64
	for {
		deleted, err := deleteTaskHistoryBatch(ctx, repo.pool, bounds, batchSize)
		if err != nil {
			t.Fatalf("deleteTaskHistoryBatch: %v", err)
		}
		total += deleted
		if deleted < int64(batchSize) {
			return total
		}
	}
}

func TestTaskHistoryDoomedPredicate(t *testing.T) {
	t.Run("keeps the newest executions per task", func(t *testing.T) {
		pool := taskHistoryTestPool(t)
		repo := NewPgExecutionRepository(pool)
		key := taskKey(t, pool, "count-capped")
		now := time.Now().UTC().Truncate(time.Microsecond)
		var ids []int64
		for i := range 5 {
			ids = append(ids, insertTaskExecution(t, pool, key, now.Add(time.Duration(i)*time.Minute)))
		}

		if deleted := pruneOneKey(t, repo, key, 2, now.Add(-24*time.Hour), 100); deleted != 3 {
			t.Fatalf("deleted = %d, want 3", deleted)
		}
		got := remainingTaskExecutionIDs(t, pool, key)
		if want := ids[len(ids)-2:]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("remaining IDs = %v, want %v", got, want)
		}
	})

	t.Run("keepPerTask boundary is off by exactly one", func(t *testing.T) {
		pool := taskHistoryTestPool(t)
		repo := NewPgExecutionRepository(pool)
		now := time.Now().UTC().Truncate(time.Microsecond)

		// Recent rows: only the count cap can delete them. With exactly
		// keepPerTask rows nothing is doomed; one more row and exactly one is.
		exact := taskKey(t, pool, "exact")
		for i := range 3 {
			insertTaskExecution(t, pool, exact, now.Add(time.Duration(i)*time.Minute))
		}
		if deleted := pruneOneKey(t, repo, exact, 3, now.Add(-24*time.Hour), 100); deleted != 0 {
			t.Fatalf("deleted = %d at the boundary, want 0", deleted)
		}

		over := taskKey(t, pool, "over")
		var ids []int64
		for i := range 4 {
			ids = append(ids, insertTaskExecution(t, pool, over, now.Add(time.Duration(i)*time.Minute)))
		}
		if deleted := pruneOneKey(t, repo, over, 3, now.Add(-24*time.Hour), 100); deleted != 1 {
			t.Fatalf("deleted = %d one past the boundary, want 1", deleted)
		}
		got := remainingTaskExecutionIDs(t, pool, over)
		if want := ids[1:]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("remaining IDs = %v, want %v", got, want)
		}
	})

	t.Run("age pruning always preserves the newest execution", func(t *testing.T) {
		pool := taskHistoryTestPool(t)
		repo := NewPgExecutionRepository(pool)
		key := taskKey(t, pool, "age-capped")
		now := time.Now().UTC().Truncate(time.Microsecond)
		olderID := insertTaskExecution(t, pool, key, now.Add(-48*time.Hour))
		newestID := insertTaskExecution(t, pool, key, now.Add(-47*time.Hour))

		if deleted := pruneOneKey(t, repo, key, 100, now.Add(-24*time.Hour), 100); deleted != 1 {
			t.Fatalf("deleted = %d, want 1", deleted)
		}
		got := remainingTaskExecutionIDs(t, pool, key)
		if fmt.Sprint(got) != fmt.Sprint([]int64{newestID}) {
			t.Fatalf("remaining IDs = %v, want [%d]; older ID was %d", got, newestID, olderID)
		}
	})

	t.Run("uses ID to break completion time ties", func(t *testing.T) {
		pool := taskHistoryTestPool(t)
		repo := NewPgExecutionRepository(pool)
		key := taskKey(t, pool, "tied")
		now := time.Now().UTC().Truncate(time.Microsecond)
		firstID := insertTaskExecution(t, pool, key, now)
		newestID := insertTaskExecution(t, pool, key, now)

		if deleted := pruneOneKey(t, repo, key, 1, now.Add(-24*time.Hour), 100); deleted != 1 {
			t.Fatalf("deleted = %d, want 1", deleted)
		}
		got := remainingTaskExecutionIDs(t, pool, key)
		if fmt.Sprint(got) != fmt.Sprint([]int64{newestID}) {
			t.Fatalf("remaining IDs = %v, want [%d]; first ID was %d", got, newestID, firstID)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		latest, err := repo.GetLatest(ctx, key)
		if err != nil {
			t.Fatalf("GetLatest: %v", err)
		}
		if latest == nil || latest.ID != newestID {
			t.Fatalf("GetLatest ID = %v, want %d", latest, newestID)
		}
	})

	t.Run("bounded batches converge and are idempotent", func(t *testing.T) {
		pool := taskHistoryTestPool(t)
		repo := NewPgExecutionRepository(pool)
		key := taskKey(t, pool, "batched")
		now := time.Now().UTC().Truncate(time.Microsecond)
		for i := range 7 {
			insertTaskExecution(t, pool, key, now.Add(time.Duration(i)*time.Minute))
		}

		if total := pruneOneKey(t, repo, key, 1, now.Add(-24*time.Hour), 2); total != 6 {
			t.Fatalf("total deleted = %d, want 6", total)
		}
		if got := len(remainingTaskExecutionIDs(t, pool, key)); got != 1 {
			t.Fatalf("remaining rows = %d, want 1", got)
		}
		if again := pruneOneKey(t, repo, key, 1, now.Add(-24*time.Hour), 2); again != 0 {
			t.Fatalf("idempotent delete count = %d, want 0", again)
		}
	})
}

// TestTaskHistoryDoomedPredicateMatchesWindowRanking pins the tuple-comparison
// predicate to the window-function definition of the retention policy: delete a
// row iff its rank over (completed_at DESC, id DESC) exceeds keepPerTask, or it
// is not rank 1 and is older than the cutoff.
func TestTaskHistoryDoomedPredicateMatchesWindowRanking(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	key := taskKey(t, pool, "equivalence")

	// Deliberately messy: duplicate completion times, rows on both sides of
	// every plausible cutoff, and insertion order uncorrelated with time.
	offsets := []time.Duration{
		-72 * time.Hour, -72 * time.Hour, -48 * time.Hour, -24 * time.Hour,
		-24 * time.Hour, -12 * time.Hour, -time.Hour, 0, 0, time.Hour,
	}
	for _, offset := range offsets {
		insertTaskExecution(t, pool, key, now.Add(offset))
	}

	for _, keep := range []int{1, 2, 3, 5, 9, 10, 11} {
		for _, cutoffOffset := range []time.Duration{
			-96 * time.Hour, -72 * time.Hour, -36 * time.Hour, -24 * time.Hour, 0, 2 * time.Hour,
		} {
			cutoff := now.Add(cutoffOffset)
			bounds, ok, err := repo.loadDoomedBounds(ctx, key, keep, cutoff)
			if err != nil {
				t.Fatalf("loadDoomedBounds: %v", err)
			}
			if !ok {
				t.Fatal("loadDoomedBounds reported no rows")
			}
			clause, args := bounds.where()

			actual := queryIDs(t, pool, `SELECT id FROM task_executions WHERE `+clause+` ORDER BY id`, args...)
			expected := queryIDs(t, pool, `
				WITH ranked AS (
					SELECT
						id,
						completed_at,
						row_number() OVER (
							PARTITION BY task_key
							ORDER BY completed_at DESC, id DESC
						) AS recent_rank
					FROM task_executions
					WHERE task_key = $1
				)
				SELECT id FROM ranked
				WHERE recent_rank > $2 OR (recent_rank > 1 AND completed_at < $3)
				ORDER BY id`, key, keep, cutoff)

			if fmt.Sprint(actual) != fmt.Sprint(expected) {
				t.Fatalf("keep=%d cutoff=%v: tuple predicate doomed %v, window ranking doomed %v",
					keep, cutoffOffset, actual, expected)
			}
		}
	}
}

func TestPgExecutionRepositoryPrunePreservesEachTaskBoundary(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	clearTaskHistory(t, pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	counts := map[string]int{
		taskKey(t, pool, "task-a"): 5,
		taskKey(t, pool, "task-b"): 3,
		taskKey(t, pool, "task-c"): 1,
	}
	inserted := map[string][]int64{}
	for key, count := range counts {
		for i := range count {
			inserted[key] = append(inserted[key], insertTaskExecution(
				t, pool, key, now.Add(time.Duration(i)*time.Minute),
			))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := repo.Prune(ctx, 2, now.Add(-24*time.Hour), 2, 100)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.LimitReached || result.Skipped {
		t.Fatalf("Prune result = %#v, want a complete run", result)
	}

	for key, ids := range inserted {
		want := ids
		if len(want) > 2 {
			want = want[len(want)-2:]
		}
		got := remainingTaskExecutionIDs(t, pool, key)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s remaining IDs = %v, want %v", key, got, want)
		}
	}
}

func TestPgExecutionRepositoryPruneReportsBatchLimit(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	clearTaskHistory(t, pool)
	key := taskKey(t, pool, "limited")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 7 {
		insertTaskExecution(t, pool, key, now.Add(time.Duration(i)*time.Minute))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := repo.Prune(ctx, 1, now.Add(-24*time.Hour), 2, 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Deleted != 4 || !result.LimitReached || result.Skipped {
		t.Fatalf("Prune result = %#v, want 4 deleted with limit reached", result)
	}
	if got := len(remainingTaskExecutionIDs(t, pool, key)); got != 3 {
		t.Fatalf("remaining rows = %d, want 3", got)
	}

	result, err = repo.Prune(ctx, 1, now.Add(-24*time.Hour), 2, 100)
	if err != nil {
		t.Fatalf("finishing Prune: %v", err)
	}
	if result.Deleted != 2 || result.LimitReached || result.Skipped {
		t.Fatalf("finishing Prune result = %#v, want 2 deleted", result)
	}
}

// TestPgExecutionRepositoryPruneDoesNotReportLimitOnExactMultiple covers the
// case that made the old loop-exhaustion signal lie: the doomed count is an
// exact multiple of batchSize and fills the batch budget precisely, so the run
// finished even though every batch came back full.
func TestPgExecutionRepositoryPruneDoesNotReportLimitOnExactMultiple(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	clearTaskHistory(t, pool)
	key := taskKey(t, pool, "exact-multiple")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 7 {
		insertTaskExecution(t, pool, key, now.Add(time.Duration(i)*time.Minute))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 6 doomed rows, batches of 2, exactly 3 batches of budget.
	result, err := repo.Prune(ctx, 1, now.Add(-24*time.Hour), 2, 3)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Deleted != 6 || result.LimitReached || result.Skipped {
		t.Fatalf("Prune result = %#v, want 6 deleted without limit reached", result)
	}
	if got := len(remainingTaskExecutionIDs(t, pool, key)); got != 1 {
		t.Fatalf("remaining rows = %d, want 1", got)
	}
}

func TestPgExecutionRepositoryPruneSkipsWhenAnotherNodeHoldsLock(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	locker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock connection: %v", err)
	}
	defer locker.Release()
	if _, err := locker.Exec(ctx, `SELECT pg_advisory_lock($1)`, taskHistoryCleanupAdvisoryLock); err != nil {
		t.Fatalf("hold cleanup lock: %v", err)
	}
	defer func() {
		_, _ = locker.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, taskHistoryCleanupAdvisoryLock)
	}()

	result, err := repo.Prune(ctx, 1, time.Now(), 10, 10)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !result.Skipped || result.Deleted != 0 || result.LimitReached {
		t.Fatalf("Prune result = %#v, want skipped", result)
	}
}

// TestPgExecutionRepositoryPruneConcurrent proves the advisory lock serializes
// pruners: one wins and prunes, the other skips, and the surviving row count is
// the same either way. The lock is database-global, so this test also asserts
// that a skip is never mistaken for a completed run.
func TestPgExecutionRepositoryPruneConcurrent(t *testing.T) {
	pool := taskHistoryTestPool(t)
	repo := NewPgExecutionRepository(pool)
	clearTaskHistory(t, pool)
	key := taskKey(t, pool, "concurrent")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 20 {
		insertTaskExecution(t, pool, key, now.Add(time.Duration(i)*time.Minute))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Prune(ctx, 1, now.Add(-24*time.Hour), 100, 100)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Prune: %v", err)
		}
	}

	// Whichever goroutine won the lock, a second serialized run converges.
	if _, err := repo.Prune(ctx, 1, now.Add(-24*time.Hour), 100, 100); err != nil {
		t.Fatalf("settling Prune: %v", err)
	}
	if got := len(remainingTaskExecutionIDs(t, pool, key)); got != 1 {
		t.Fatalf("remaining rows = %d, want 1", got)
	}
}
