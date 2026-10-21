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

func TestTryAcquireNilPoolReportsNotAcquired(t *testing.T) {
	lock, acquired, err := TryAcquire(context.Background(), nil, pglockTestKey)
	if err != nil || acquired || lock != nil {
		t.Fatalf("TryAcquire(nil pool) = (%v, %v, %v), want (nil, false, nil)", lock, acquired, err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release on nil lock: %v", err)
	}
}

// pglockOtherTestKey is a second key for the tests that hold two locks.
const pglockOtherTestKey int64 = 0x70676C6F636B02

func TestAcquireAlsoWithoutALockFails(t *testing.T) {
	var lock *Lock
	if acquired, err := lock.AcquireAlso(context.Background(), pglockOtherTestKey, time.Minute); err == nil || acquired {
		t.Fatalf("AcquireAlso on a nil Lock = (%v, %v), want an error", acquired, err)
	}
}

func TestReleaseAlsoUnlocksOnlyThatKey(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = lock.Release(ctx) }()
	if acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 0); err != nil || !acquired {
		t.Fatalf("AcquireAlso = (%v, %v), want acquired", acquired, err)
	}
	if err := lock.ReleaseAlso(ctx, pglockOtherTestKey); err != nil {
		t.Fatalf("ReleaseAlso: %v", err)
	}
	if lockHeld(t, pool, pglockOtherTestKey) {
		t.Fatal("the released key is still held")
	}
	if !lockHeld(t, pool, pglockTestKey) {
		t.Fatal("ReleaseAlso dropped the lock's own key")
	}
	// A second release, or a key never taken, changes nothing.
	if err := lock.ReleaseAlso(ctx, pglockOtherTestKey); err != nil {
		t.Fatalf("second ReleaseAlso: %v", err)
	}
	// The key can be taken again on the same session, and Release unlocks
	// everything still held.
	if acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 0); err != nil || !acquired {
		t.Fatalf("AcquireAlso again = (%v, %v), want acquired", acquired, err)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lockHeld(t, pool, pglockTestKey) || lockHeld(t, pool, pglockOtherTestKey) {
		t.Fatal("Release left a key held")
	}
}

// A lock whose session dies fails Check and is no longer held, and
// AcquireAlso on it fails.
func TestHeldIsFalseAfterTheSessionDies(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = lock.Release(ctx) }()
	if !lock.Held() {
		t.Fatal("Held = false for a fresh lock")
	}
	var pid int
	if err := lock.conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	if err := lock.Check(ctx); err == nil {
		t.Fatal("Check succeeded on a dead session")
	}
	if lock.Held() {
		t.Fatal("Held = true after the session died")
	}
	if _, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 0); err == nil {
		t.Fatal("AcquireAlso succeeded on a dead session")
	}
}
