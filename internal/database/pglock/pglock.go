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

	"github.com/jackc/pgx/v5"
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

// minAcquireWait is the least time Acquire gives itself to get a pooled
// connection, so a call with little or no wait left still gets an idle one.
const minAcquireWait = 250 * time.Millisecond

// Acquire takes advisory lock key, waiting up to wait in all for a pooled
// connection (at least minAcquireWait) and for another session holding the
// lock to release it; with no wait left it only tries the lock. It reports
// acquired false (with a nil Lock and no error) when the wait runs out, or
// when pool is nil. PostgreSQL enforces the lock wait, so a wait that runs out
// leaves no session queued on the lock.
//
// The caller owns the returned Lock and must call Release exactly once.
func Acquire(ctx context.Context, pool *pgxpool.Pool, key int64, wait time.Duration) (*Lock, bool, error) {
	if pool == nil {
		return nil, false, nil
	}
	deadline := time.Now().Add(wait)
	acquireCtx, cancel := context.WithTimeout(ctx, max(wait, minAcquireWait))
	defer cancel()
	conn, err := pool.Acquire(acquireCtx)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("acquiring connection for advisory lock %d: %w", key, err)
	}
	locked, err := LockWithin(ctx, conn, key, time.Until(deadline))
	if err != nil {
		// PostgreSQL may have granted the lock just before the error.
		_ = conn.Hijack().Close(context.WithoutCancel(ctx))
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
		locked, err = LockWithin(ctx, l.conn, key, wait)
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

// Held reports whether l's session is still open. An error in AcquireAlso
// or ReleaseAlso closes it, which drops every lock l held.
func (l *Lock) Held() bool {
	return l != nil && l.conn != nil
}

// Check runs a query on l's session and returns an error when the session
// has closed, which released every lock l held. A failed query closes the
// session, as in AcquireAlso, and Held then reports false.
func (l *Lock) Check(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return errors.New("checking advisory lock: no session holds a lock")
	}
	if _, err := l.conn.Exec(ctx, `SELECT 1`); err != nil {
		conn := l.conn
		l.conn = nil
		_ = conn.Hijack().Close(context.WithoutCancel(ctx))
		return fmt.Errorf("checking advisory lock session: %w", err)
	}
	return nil
}

// ReleaseAlso unlocks key, which AcquireAlso took on l's session, and keeps
// l's other locks, so work that takes one key after another under a
// longer-held lock does not hold them all to the end. When the unlock fails
// or reports the key was not held, the session is closed, as in Release:
// that drops l's other locks too, and Release becomes a no-op. A key l does
// not hold, or a session already closed, is a no-op.
func (l *Lock) ReleaseAlso(ctx context.Context, key int64) error {
	if l == nil || l.conn == nil {
		return nil
	}
	i := slices.Index(l.also, key)
	if i < 0 {
		return nil
	}
	l.also = slices.Delete(l.also, i, i+1)

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	var unlocked bool
	err := l.conn.QueryRow(releaseCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked)
	if err == nil && unlocked {
		return nil
	}
	conn := l.conn
	l.conn = nil
	l.also = nil
	_ = conn.Hijack().Close(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("releasing advisory lock %d: %w", key, err)
	}
	return fmt.Errorf("releasing advisory lock %d: lock was not held", key)
}

// Session is a database session that can begin a transaction: a pooled
// connection, or a connection of its own.
type Session interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// LockWithin takes advisory lock key on conn's session, waiting up to wait
// (at least a millisecond). It reports false with no error when the wait runs
// out. After any error the session's lock state is unknown and conn must be
// closed, not reused.
func LockWithin(ctx context.Context, conn Session, key int64, wait time.Duration) (bool, error) {
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
