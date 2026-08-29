package pglock

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pglockTestKey is outside the ranges any product code uses, so these tests
// cannot collide with a real lock in a shared test database.
const pglockTestKey int64 = 0x70676C6F636B01

func testPool(t *testing.T) *pgxpool.Pool {
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

func lockHeld(t *testing.T, pool *pgxpool.Pool, key int64) bool {
	t.Helper()
	var held bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory'
				AND granted
				AND ((classid::bigint << 32) | objid::bigint) = $1
		)`, key).Scan(&held)
	if err != nil {
		t.Fatalf("inspect pg_locks: %v", err)
	}
	return held
}

func TestTryAcquireExcludesSecondHolder(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}

	second, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil {
		t.Fatalf("second TryAcquire: %v", err)
	}
	if acquired {
		_ = second.Release(ctx)
		t.Fatal("second TryAcquire acquired a lock already held")
	}

	if err := lock.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lockHeld(t, pool, pglockTestKey) {
		t.Fatal("advisory lock still held after Release")
	}

	third, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire after Release = (%v, %v), want acquired", acquired, err)
	}
	if err := third.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestReleaseDoesNotReturnAStrandedLockToThePool covers the failure the shared
// helper exists to prevent: if the unlock does not confirm, the connection must
// not go back into the pool still holding a session-level lock.
func TestReleaseDoesNotReturnAStrandedLockToThePool(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}

	// Unlock out of band so the helper's own unlock reports "was not held".
	var unlocked bool
	if err := lock.Conn().QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, pglockTestKey).Scan(&unlocked); err != nil {
		t.Fatalf("out of band unlock: %v", err)
	}
	if !unlocked {
		t.Fatal("out of band unlock reported the lock was not held")
	}

	if err := lock.Release(ctx); err == nil {
		t.Fatal("Release reported success for an unheld lock")
	}
	if lockHeld(t, pool, pglockTestKey) {
		t.Fatal("advisory lock still held after Release")
	}

	// A second Release is a no-op rather than a double free.
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestTryAcquireNilPoolReportsNotAcquired(t *testing.T) {
	lock, acquired, err := TryAcquire(context.Background(), nil, pglockTestKey)
	if err != nil || acquired || lock != nil {
		t.Fatalf("TryAcquire(nil pool) = (%v, %v, %v), want (nil, false, nil)", lock, acquired, err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release on nil lock: %v", err)
	}
}
