package repository

import (
	"context"
	"fmt"
	"os"
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
