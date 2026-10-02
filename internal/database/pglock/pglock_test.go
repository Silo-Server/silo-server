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

// pglockOtherTestKey is a second key for the tests that hold two locks.
const pglockOtherTestKey int64 = 0x70676C6F636B02

// AcquireAlso waits for another session's hold on the second key, then holds
// both keys on one session, and Release frees both.
func TestAcquireAlsoWaitsForTheHolder(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	other, acquired, err := TryAcquire(ctx, pool, pglockOtherTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire(other) = (%v, %v), want acquired", acquired, err)
	}
	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	type result struct {
		acquired bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 20*time.Second)
		done <- result{acquired, err}
	}()
	for lockWaiters(t, pool, pglockOtherTestKey) == 0 {
		select {
		case got := <-done:
			t.Fatalf("AcquireAlso returned (%v, %v) while the key was held", got.acquired, got.err)
		case <-ctx.Done():
			t.Fatal("AcquireAlso never queued for the key")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := other.Release(ctx); err != nil {
		t.Fatalf("Release(other): %v", err)
	}
	if got := <-done; got.err != nil || !got.acquired {
		t.Fatalf("AcquireAlso = (%v, %v), want acquired once the holder released", got.acquired, got.err)
	}
	// Both keys outlive the transaction that set the wait, on one session.
	var sessions int
	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted
			AND ((classid::bigint << 32) | objid::bigint) = ANY($1)`, []int64{pglockTestKey, pglockOtherTestKey}).Scan(&sessions); err != nil {
		t.Fatalf("inspect pg_locks: %v", err)
	}
	if !lockHeld(t, pool, pglockTestKey) || !lockHeld(t, pool, pglockOtherTestKey) || sessions != 1 {
		t.Fatalf("held = %v and %v on %d sessions, want both keys on one session",
			lockHeld(t, pool, pglockTestKey), lockHeld(t, pool, pglockOtherTestKey), sessions)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lockHeld(t, pool, pglockTestKey) || lockHeld(t, pool, pglockOtherTestKey) {
		t.Fatal("a key is still held after Release")
	}
}

// A wait that runs out reports not acquired, keeps the session's own lock, and
// leaves the session without the lock_timeout it set.
func TestAcquireAlsoGivesUpAfterTheWait(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	other, acquired, err := TryAcquire(ctx, pool, pglockOtherTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire(other) = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = other.Release(ctx) }()
	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = lock.Release(ctx) }()

	if acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 100*time.Millisecond); err != nil || acquired {
		t.Fatalf("AcquireAlso = (%v, %v), want (false, nil) after the wait", acquired, err)
	}
	if waiters := lockWaiters(t, pool, pglockOtherTestKey); waiters != 0 {
		t.Fatalf("%d sessions still queued for the key, want none", waiters)
	}
	if !lockHeld(t, pool, pglockTestKey) {
		t.Fatal("the session lost its own lock when the wait ran out")
	}
	var timeout string
	if err := lock.Conn().QueryRow(ctx, `SHOW lock_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	if timeout != "0" {
		t.Fatalf("session lock_timeout = %q, want the default 0", timeout)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if lockHeld(t, pool, pglockTestKey) {
		t.Fatal("advisory lock still held after Release")
	}
}

// Without a wait, AcquireAlso only tries.
func TestAcquireAlsoWithoutAWaitOnlyTries(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lock, acquired, err := TryAcquire(ctx, pool, pglockTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = (%v, %v), want acquired", acquired, err)
	}
	defer func() { _ = lock.Release(ctx) }()
	other, acquired, err := TryAcquire(ctx, pool, pglockOtherTestKey)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire(other) = (%v, %v), want acquired", acquired, err)
	}
	if acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 0); err != nil || acquired {
		t.Fatalf("AcquireAlso while held = (%v, %v), want (false, nil)", acquired, err)
	}
	if err := other.Release(ctx); err != nil {
		t.Fatalf("Release(other): %v", err)
	}
	if acquired, err := lock.AcquireAlso(ctx, pglockOtherTestKey, 0); err != nil || !acquired {
		t.Fatalf("AcquireAlso once free = (%v, %v), want acquired", acquired, err)
	}
}

func TestAcquireAlsoWithoutALockFails(t *testing.T) {
	var lock *Lock
	if acquired, err := lock.AcquireAlso(context.Background(), pglockOtherTestKey, time.Minute); err == nil || acquired {
		t.Fatalf("AcquireAlso on a nil Lock = (%v, %v), want an error", acquired, err)
	}
}
