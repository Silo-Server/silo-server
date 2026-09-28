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

// lockWaiters counts the sessions queued for advisory lock key.
func lockWaiters(t *testing.T, pool *pgxpool.Pool, key int64) int {
	t.Helper()
	var waiters int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory'
			AND NOT granted
			AND ((classid::bigint << 32) | objid::bigint) = $1`, key).Scan(&waiters)
	if err != nil {
		t.Fatalf("inspect pg_locks: %v", err)
	}
	return waiters
}

func TestAcquireWaitsForTheHolder(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	held, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	type result struct {
		lock     *Lock
		acquired bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		lock, acquired, err := Acquire(ctx, pool, pglockTestKey, 20*time.Second)
		done <- result{lock, acquired, err}
	}()
	for lockWaiters(t, pool, pglockTestKey) == 0 {
		select {
		case got := <-done:
			t.Fatalf("Acquire returned (%v, %v) while the lock was held", got.acquired, got.err)
		case <-ctx.Done():
			t.Fatal("Acquire never queued for the lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := held.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got := <-done
	if got.err != nil || !got.acquired {
		t.Fatalf("Acquire = (%v, %v), want acquired once the holder released", got.acquired, got.err)
	}
	// The lock outlives the transaction that set the wait.
	if !lockHeld(t, pool, pglockTestKey) {
		t.Fatal("advisory lock not held after Acquire")
	}
	if _, again, err := TryAcquire(ctx, pool, pglockTestKey); err != nil || again {
		t.Fatalf("TryAcquire beside Acquire's lock = (%v, %v), want not acquired", again, err)
	}
	if err := got.lock.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lockHeld(t, pool, pglockTestKey) {
		t.Fatal("advisory lock still held after Release")
	}
}

// A wait that runs out reports not acquired and hands the connection back
// without the lock_timeout it set. The single-connection pool makes the next
// borrower get the same session.
func TestAcquireGivesUpAfterTheWait(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config := pool.Config().Copy()
	config.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect single-connection pool: %v", err)
	}
	t.Cleanup(single.Close)

	held, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = held.Release(ctx) }()

	lock, acquired, err := Acquire(ctx, single, pglockTestKey, 100*time.Millisecond)
	if err != nil || acquired || lock != nil {
		t.Fatalf("Acquire = (%v, %v, %v), want (nil, false, nil) after the wait", lock, acquired, err)
	}
	if waiters := lockWaiters(t, pool, pglockTestKey); waiters != 0 {
		t.Fatalf("%d sessions still queued for the lock, want none", waiters)
	}
	var timeout string
	if err := single.QueryRow(ctx, `SHOW lock_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	if timeout != "0" {
		t.Fatalf("pooled session lock_timeout = %q, want the default 0", timeout)
	}
}

func TestAcquireNilPoolReportsNotAcquired(t *testing.T) {
	lock, acquired, err := Acquire(context.Background(), nil, pglockTestKey, time.Minute)
	if err != nil || acquired || lock != nil {
		t.Fatalf("Acquire(nil pool) = (%v, %v, %v), want (nil, false, nil)", lock, acquired, err)
	}
}
