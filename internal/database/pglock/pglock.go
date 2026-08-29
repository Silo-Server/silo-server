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
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseTimeout bounds the unlock round trip so a caller whose own context
// has already been canceled still gets a chance to release cleanly instead of
// always throwing the connection away.
const releaseTimeout = 5 * time.Second

// Lock is a held session-level advisory lock and the connection holding it.
type Lock struct {
	conn *pgxpool.Conn
	key  int64
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

// Conn exposes the connection holding the lock. Work that must be serialized
// against the lock may run on any connection; this is for callers that want to
// keep it on the locked session.
func (l *Lock) Conn() *pgxpool.Conn {
	if l == nil {
		return nil
	}
	return l.conn
}

// Release unlocks and returns the connection to the pool. If the unlock fails
// or reports that the lock was not held, the connection is hijacked out of the
// pool and closed so the stranded lock dies with it.
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

	var unlocked bool
	err := conn.QueryRow(releaseCtx, `SELECT pg_advisory_unlock($1)`, l.key).Scan(&unlocked)
	if err == nil && unlocked {
		conn.Release()
		return nil
	}

	rawConn := conn.Hijack()
	_ = rawConn.Close(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("releasing advisory lock %d: %w", l.key, err)
	}
	return fmt.Errorf("releasing advisory lock %d: lock was not held", l.key)
}
