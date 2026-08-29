package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const taskHistoryCleanupAdvisoryLock int64 = 0x53494C4F48495354 // "SILOHIST"

type taskHistoryExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PgExecutionRepository implements taskmanager.ExecutionRepository using PostgreSQL.
type PgExecutionRepository struct {
	pool *pgxpool.Pool
}

func NewPgExecutionRepository(pool *pgxpool.Pool) *PgExecutionRepository {
	return &PgExecutionRepository{pool: pool}
}

func (r *PgExecutionRepository) Insert(ctx context.Context, result taskmanager.ExecutionResult) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_executions (task_key, started_at, completed_at, status, error_message, result_data, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		result.TaskKey, result.StartedAt, result.CompletedAt,
		result.Status, result.ErrorMessage, result.ResultData, result.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("inserting task execution: %w", err)
	}
	return nil
}

func (r *PgExecutionRepository) GetLatest(ctx context.Context, taskKey string) (*taskmanager.ExecutionResult, error) {
	var result taskmanager.ExecutionResult
	err := r.pool.QueryRow(ctx, `
		SELECT id, task_key, started_at, completed_at, status, COALESCE(error_message, ''), result_data, duration_ms
		FROM task_executions
		WHERE task_key = $1
		ORDER BY completed_at DESC, id DESC
		LIMIT 1`, taskKey,
	).Scan(
		&result.ID, &result.TaskKey, &result.StartedAt, &result.CompletedAt,
		&result.Status, &result.ErrorMessage, &result.ResultData, &result.DurationMs,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting latest task execution: %w", err)
	}
	return &result, nil
}

func (r *PgExecutionRepository) List(ctx context.Context, taskKey string, limit int) ([]taskmanager.ExecutionResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, task_key, started_at, completed_at, status, COALESCE(error_message, ''), result_data, duration_ms
		FROM task_executions
		WHERE task_key = $1
		ORDER BY completed_at DESC, id DESC
		LIMIT $2`, taskKey, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing task executions: %w", err)
	}
	defer rows.Close()

	var results []taskmanager.ExecutionResult
	for rows.Next() {
		var r taskmanager.ExecutionResult
		if err := rows.Scan(
			&r.ID, &r.TaskKey, &r.StartedAt, &r.CompletedAt,
			&r.Status, &r.ErrorMessage, &r.ResultData, &r.DurationMs,
		); err != nil {
			return nil, fmt.Errorf("scanning task execution: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Prune deletes bounded batches of execution history outside the retention
// policy. A database advisory lock ensures only one Silo node prunes at a time.
// It always preserves the newest execution for every task, including tasks
// that are no longer registered.
//
// The work is driven per task key rather than by ranking the whole table.
// After one pass to enumerate the (handful of) distinct task keys, every
// boundary lookup and delete is a range scan of
// idx_task_executions_key_completed (task_key, completed_at DESC). A run with
// nothing to delete costs two index probes per task, where ranking the whole
// table cost a full-table window sort per batch — up to maxBatches of them.
func (r *PgExecutionRepository) Prune(
	ctx context.Context,
	keepPerTask int,
	cutoff time.Time,
	batchSize int,
	maxBatches int,
) (pruneResult taskmanager.HistoryPruneResult, returnErr error) {
	lock, acquired, err := pglock.TryAcquire(ctx, r.pool, taskHistoryCleanupAdvisoryLock)
	if err != nil {
		return pruneResult, fmt.Errorf("acquiring task history cleanup lock: %w", err)
	}
	if !acquired {
		pruneResult.Skipped = true
		return pruneResult, nil
	}
	defer func() {
		if err := lock.Release(ctx); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("task history cleanup: %w", err)
		}
	}()

	keys, err := r.listTaskKeys(ctx)
	if err != nil {
		return pruneResult, err
	}

	batches := 0
	for i, key := range keys {
		if err := ctx.Err(); err != nil {
			return pruneResult, err
		}
		doomed, ok, err := r.loadDoomedBounds(ctx, key, keepPerTask, cutoff)
		if err != nil {
			return pruneResult, err
		}
		if !ok {
			continue
		}
		for {
			if batches >= maxBatches {
				// The budget ran out before this key was proven clean. Only
				// report the cap when work actually remains, so a doomed count
				// that happens to be an exact multiple of batchSize does not
				// look like a truncated run.
				remaining, err := r.anyDoomedRemaining(ctx, keys[i:], keepPerTask, cutoff)
				if err != nil {
					return pruneResult, err
				}
				pruneResult.LimitReached = remaining
				return pruneResult, nil
			}
			deleted, err := deleteTaskHistoryBatch(ctx, r.pool, doomed, batchSize)
			batches++
			pruneResult.Deleted += deleted
			if err != nil {
				return pruneResult, err
			}
			if deleted < int64(batchSize) {
				break
			}
		}
	}
	return pruneResult, nil
}

func (r *PgExecutionRepository) listTaskKeys(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT task_key FROM task_executions`)
	if err != nil {
		return nil, fmt.Errorf("listing task history keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scanning task history key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing task history keys: %w", err)
	}
	return keys, nil
}

// taskHistoryDoomedBounds is the delete predicate for one task key, expressed
// as tuple comparisons against two boundary rows instead of a window rank.
//
// Ranking rows by (completed_at DESC, id DESC) makes "rank > n" identical to
// "sorts strictly before the rank-n row in ascending (completed_at, id)
// order", which is exactly the row-value comparison (completed_at, id) < (ts,
// id) that Postgres can answer from the (task_key, completed_at DESC) index.
type taskHistoryDoomedBounds struct {
	taskKey string

	// newest is the rank-1 row. Every doomed row sorts strictly below it, which
	// is how the newest execution of a task always survives.
	newestAt time.Time
	newestID int64

	// boundary is the rank-keepPerTask row, absent when the task has fewer than
	// keepPerTask executions (nothing exceeds the count cap then).
	boundaryAt  time.Time
	boundaryID  int64
	hasBoundary bool

	cutoff time.Time
}

// where renders the doomed predicate and its arguments. A row is doomed when
// it is not the newest for its task AND it is either older than the cutoff or
// beyond the count cap.
func (b taskHistoryDoomedBounds) where() (string, []any) {
	args := []any{b.taskKey, b.newestAt, b.newestID, b.cutoff}
	clause := `task_key = $1
		AND (completed_at, id) < ($2, $3)
		AND completed_at < $4`
	if b.hasBoundary {
		args = append(args, b.boundaryAt, b.boundaryID)
		clause = `task_key = $1
		AND (completed_at, id) < ($2, $3)
		AND (completed_at < $4 OR (completed_at, id) < ($5, $6))`
	}
	return clause, args
}

// loadDoomedBounds resolves the two boundary rows for a task key. It reports
// false when the task has no executions at all.
func (r *PgExecutionRepository) loadDoomedBounds(
	ctx context.Context,
	taskKey string,
	keepPerTask int,
	cutoff time.Time,
) (taskHistoryDoomedBounds, bool, error) {
	bounds := taskHistoryDoomedBounds{taskKey: taskKey, cutoff: cutoff}
	err := r.pool.QueryRow(ctx, `
		SELECT completed_at, id
		FROM task_executions
		WHERE task_key = $1
		ORDER BY completed_at DESC, id DESC
		LIMIT 1`, taskKey,
	).Scan(&bounds.newestAt, &bounds.newestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bounds, false, nil
		}
		return bounds, false, fmt.Errorf("reading newest task execution for %s: %w", taskKey, err)
	}

	// keepPerTask is clamped by the settings reader; guard the OFFSET anyway so
	// a stray zero degrades to "keep the newest one" rather than erroring.
	offset := max(keepPerTask-1, 0)
	err = r.pool.QueryRow(ctx, `
		SELECT completed_at, id
		FROM task_executions
		WHERE task_key = $1
		ORDER BY completed_at DESC, id DESC
		OFFSET $2
		LIMIT 1`, taskKey, offset,
	).Scan(&bounds.boundaryAt, &bounds.boundaryID)
	switch {
	case err == nil:
		bounds.hasBoundary = true
	case errors.Is(err, pgx.ErrNoRows):
		bounds.hasBoundary = false
	default:
		return bounds, false, fmt.Errorf("reading task execution retention boundary for %s: %w", taskKey, err)
	}
	return bounds, true, nil
}

// anyDoomedRemaining probes the given keys for leftover work. The key list is
// bounded by the number of task keys ever recorded, so this stays a handful of
// index probes even in the worst case.
func (r *PgExecutionRepository) anyDoomedRemaining(
	ctx context.Context,
	taskKeys []string,
	keepPerTask int,
	cutoff time.Time,
) (bool, error) {
	for _, key := range taskKeys {
		doomed, ok, err := r.loadDoomedBounds(ctx, key, keepPerTask, cutoff)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		clause, args := doomed.where()
		var exists bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM task_executions WHERE `+clause+` LIMIT 1)`,
			args...,
		).Scan(&exists); err != nil {
			return false, fmt.Errorf("probing remaining task executions for %s: %w", key, err)
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

// deleteTaskHistoryBatch deletes up to batchSize doomed rows for one task key.
// The inner SELECT deliberately has no ORDER BY: the advisory lock makes this
// the only pruner, and the delete predicate is fixed for the whole run, so any
// subset converges in the same number of batches.
func deleteTaskHistoryBatch(
	ctx context.Context,
	execer taskHistoryExecer,
	doomed taskHistoryDoomedBounds,
	batchSize int,
) (int64, error) {
	clause, args := doomed.where()
	args = append(args, batchSize)
	result, err := execer.Exec(ctx, fmt.Sprintf(`
		DELETE FROM task_executions
		WHERE id IN (
			SELECT id
			FROM task_executions
			WHERE %s
			LIMIT $%d
		)`, clause, len(args)),
		args...,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning task executions for %s: %w", doomed.taskKey, err)
	}
	return result.RowsAffected(), nil
}
