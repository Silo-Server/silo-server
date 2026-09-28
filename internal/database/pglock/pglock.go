// Package pglock provides session-level PostgreSQL advisory locks that are
// safe to use with a connection pool.
//
// A session-level advisory lock lives on the connection that took it, so a
// connection returned to the pool while still holding the lock poisons every
// later borrower of that connection: the lock is never observable as free
// again and every node that guards work with it silently skips forever.
// [Lock.Release] therefore destroys the connection instead of returning it to
// the pool whenever the unlock cannot be confirmed.
package pglock

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseTimeout bounds the unlock round trip so a caller whose own context
// has already been canceled still gets a chance to release cleanly instead of
// always throwing the connection away.
const releaseTimeout = 5 * time.Second

// ArtworkReconcileLockKey serializes managed storage-transition recovery with
// the administrator-triggered artwork reconcile task across all API nodes.
const ArtworkReconcileLockKey int64 = 0x53494c4f535452

// Lock is a held session-level advisory lock and the connection holding it.
type Lock struct {
	conn *pgxpool.Conn
	key  int64
	// also holds the keys AcquireAlso took on the same session.
	also []int64
}

// TryAcquire takes advisory lock key without blocking. It reports acquired
// false (with a nil Lock and no error) when another session already holds the
// lock, or when pool is nil, so callers can treat "someone else is doing this"
// and "no database configured" as the same skip.
//
// The caller owns the returned Lock and must call Release exactly once.
func TryAcquire(ctx context.Context, pool *pgxpool.Pool, key int64) (*Lock, bool, error) {
	if pool == nil {
		return nil, false, nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring connection for advisory lock %d: %w", key, err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("acquiring advisory lock %d: %w", key, err)
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	return &Lock{conn: conn, key: key}, true, nil
}

// lockNotAvailable is the SQLSTATE PostgreSQL reports when lock_timeout ends a
// lock wait.
const lockNotAvailable = "55P03"

// AcquireAlso takes advisory lock key on the session that already holds l,
// waiting up to wait for another session holding key to release it, or not
// at all when wait is zero or less. Work guarded by two locks then holds one
// pooled connection instead of two. It reports false with no error when the
// wait runs out; l keeps its own lock. Release unlocks every key l holds.
//
// PostgreSQL enforces the wait with a transaction-local lock_timeout, so a
// wait that runs out leaves the lock queue and the session clean. The lock
// itself is session-level and outlives that transaction. Any other failure
// closes the session, since PostgreSQL may have granted the lock just before
// reporting the error; that drops l's lock too, and Release becomes a no-op.
func (l *Lock) AcquireAlso(ctx context.Context, key int64, wait time.Duration) (bool, error) {
	if l == nil || l.conn == nil {
		return false, fmt.Errorf("acquiring advisory lock %d: no session holds a lock", key)
	}
	var locked bool
	var err error
	if wait <= 0 {
		err = l.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked)
	} else {
		locked, err = lockWithin(ctx, l.conn, key, wait)
	}
	if err != nil {
		conn := l.conn
		l.conn = nil
		_ = conn.Hijack().Close(context.WithoutCancel(ctx))
		return false, fmt.Errorf("acquiring advisory lock %d: %w", key, err)
	}
	if locked {
		l.also = append(l.also, key)
	}
	return locked, nil
}

// lockWithin takes advisory lock key on conn, waiting up to wait. It reports
// false with no error when the wait runs out. After any error the session's
// lock state is unknown and conn must be closed, not reused.
func lockWithin(ctx context.Context, conn *pgxpool.Conn, key int64, wait time.Duration) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, strconv.FormatInt(max(wait.Milliseconds(), 1), 10)); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_lock($1)`, key)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == lockNotAvailable {
		return false, tx.Rollback(ctx)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// Conn exposes the connection holding the lock. Work that must be serialized
// against the lock may run on any connection; this is for callers that want to
// keep it on the locked session.
func (l *Lock) Conn() *pgxpool.Conn {
	if l == nil {
		return nil
	}
	return l.conn
}

// Release unlocks every key the session holds (the lock's own and any
// AcquireAlso took) and returns the connection to the pool. If an unlock fails
// or reports that the lock was not held, the connection is hijacked out of the
// pool and closed so the stranded locks die with it.
//
// Release is idempotent and safe on a nil Lock.
func (l *Lock) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()

	// The keys AcquireAlso took, newest first, then the lock's own key.
	keys := append(slices.Clone(l.also), l.key)
	slices.Reverse(keys[:len(l.also)])
	l.also = nil
	for _, key := range keys {
		var unlocked bool
		err := conn.QueryRow(releaseCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked)
		if err == nil && unlocked {
			continue
		}
		rawConn := conn.Hijack()
		_ = rawConn.Close(context.WithoutCancel(ctx))
		if err != nil {
			return fmt.Errorf("releasing advisory lock %d: %w", key, err)
		}
		return fmt.Errorf("releasing advisory lock %d: lock was not held", key)
	}
	conn.Release()
	return nil
}
