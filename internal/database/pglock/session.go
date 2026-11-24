package pglock

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionLock is a session-level advisory lock held on a database session of
// its own, opened with a pool's configuration but outside the pool. Work that
// queries the pool while it holds the lock needs this: a lock pinning one of
// the pool's connections leaves a pool of database.max_connections=1 nothing
// for that work, and the work and every other query on the server wait on it
// forever. Closing the session releases the lock, whatever state a canceled
// caller left it in.
type SessionLock struct {
	conn *pgx.Conn
	key  int64
}

// TryAcquireSession opens a session outside pool and takes advisory lock key
// on it without blocking. Like TryAcquire, it reports acquired false (with a
// nil SessionLock and no error) when another session holds the lock or pool is
// nil. Opening the session is bounded by releaseTimeout.
//
// The caller owns the returned SessionLock and must call Release.
func TryAcquireSession(ctx context.Context, pool *pgxpool.Pool, key int64) (*SessionLock, bool, error) {
	if pool == nil {
		return nil, false, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, releaseTimeout)
	conn, err := pgx.ConnectConfig(connectCtx, pool.Config().ConnConfig.Copy())
	cancel()
	if err != nil {
		return nil, false, fmt.Errorf("opening session for advisory lock %d: %w", key, err)
	}
	lock := &SessionLock{conn: conn, key: key}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		// PostgreSQL may have granted the lock before the error; closing the
		// session releases it either way.
		_ = lock.close(ctx)
		return nil, false, fmt.Errorf("acquiring advisory lock %d: %w", key, err)
	}
	if !locked {
		_ = lock.close(ctx)
		return nil, false, nil
	}
	return lock, true, nil
}

// Release unlocks the key and then closes the session. Unlocking first means
// another server trying the lock right after finds it free: closing the socket
// returns before PostgreSQL has ended the backend. A second call is a no-op.
func (l *SessionLock) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	var unlocked bool
	unlockErr := l.conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, l.key).Scan(&unlocked)
	cancel()
	if unlockErr == nil && !unlocked {
		unlockErr = errors.New("lock was not held")
	}
	if unlockErr != nil {
		unlockErr = fmt.Errorf("releasing advisory lock %d: %w", l.key, unlockErr)
	}
	return errors.Join(unlockErr, l.close(ctx))
}

func (l *SessionLock) close(ctx context.Context) error {
	conn := l.conn
	l.conn = nil
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := conn.Close(closeCtx); err != nil {
		return fmt.Errorf("closing session for advisory lock %d: %w", l.key, err)
	}
	return nil
}
