package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// SubsystemLockKey spells "SILOBKUP". See the package comment for the lock
// order every holder keeps.
const SubsystemLockKey int64 = 0x53494C4F424B5550

// ErrBusy reports that another server or operation holds the subsystem lock.
var ErrBusy = errors.New("another backup or restore is running")

// lockCheckInterval is how often a held lock's session is checked. A session
// that died released the lock, so the work under it must stop before another
// server starts the same work.
const lockCheckInterval = 10 * time.Second

// Locker hands out the subsystem lock.
type Locker interface {
	// TryLock takes the lock without waiting, or returns ErrBusy.
	TryLock(ctx context.Context) (Held, error)
	// Holder reports the session that holds the lock, without taking it: a
	// read that took the lock, even briefly, could make a nightly run skip.
	Holder(ctx context.Context) (session LockSession, held bool, err error)
}

// LockSession names one database session. Each session holds the subsystem
// lock at most once, because Release closes it, so a session names one
// acquisition: the backend pid with the session's start time, which a reused
// pid cannot repeat.
type LockSession struct {
	PID   int32
	Start time.Time
}

// Same reports whether two sessions are one.
func (s LockSession) Same(o LockSession) bool {
	return s.PID != 0 && s.PID == o.PID && s.Start.Equal(o.Start)
}

// Held is a taken subsystem lock.
type Held interface {
	// Lost is closed once the lock's session has ended. Work holding the lock
	// must stop then: another server may already hold it.
	Lost() <-chan struct{}
	// Release gives the lock back. It is idempotent.
	Release(ctx context.Context)
	// Session is the session holding the lock. The status row records it so
	// readers can tell this holder's operation from a stale one.
	Session() LockSession
	// Exec runs a statement on the session holding the lock, serialized with
	// the session check and Release. Status writes go through it, so a
	// publication can never outlive the lock that justified it.
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// errLockLost is the cause of a context withLost canceled.
var errLockLost = errors.New("backup lock session ended")

// errLockReleased refuses a statement on a released lock.
var errLockReleased = errors.New("the backup lock was released")

// lockLost reports whether ctx ended because its lock's session did.
func lockLost(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errLockLost) }

// PostgresLocker is the production Locker: a session advisory lock on a
// connection of its own, opened with the pool's configuration but outside
// it. Work under the lock (the snapshot transaction, settings reads) takes
// pool connections; with the lock's session in the pool, a pool of one
// connection would deadlock.
type PostgresLocker struct {
	Pool *pgxpool.Pool
	// CheckInterval overrides lockCheckInterval for tests.
	CheckInterval time.Duration
}

func (l PostgresLocker) TryLock(ctx context.Context) (Held, error) {
	return l.acquire(ctx, func(conn *pgx.Conn) (bool, error) {
		var acquired bool
		err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, SubsystemLockKey).Scan(&acquired)
		return acquired, err
	})
}

// Lock takes the lock, waiting up to wait (indefinitely when zero or less)
// for its holder to release it; ErrBusy when the wait runs out.
func (l PostgresLocker) Lock(ctx context.Context, wait time.Duration) (Held, error) {
	return l.acquire(ctx, func(conn *pgx.Conn) (bool, error) {
		if wait > 0 {
			return pglock.LockWithin(ctx, conn, SubsystemLockKey, wait)
		}
		_, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, SubsystemLockKey)
		return err == nil, err
	})
}

// acquire opens the lock's own session and takes the lock on it with take.
// After an error the lock may have been granted just before it, so the
// session is closed, which releases it.
func (l PostgresLocker) acquire(ctx context.Context, take func(*pgx.Conn) (bool, error)) (Held, error) {
	if l.Pool == nil {
		return nil, errors.New("backup lock: no database")
	}
	conn, err := pgx.ConnectConfig(ctx, l.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("opening the backup lock session: %w", err)
	}
	closeConn := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}
	acquired, err := take(conn)
	if err != nil {
		closeConn()
		return nil, fmt.Errorf("taking the backup lock: %w", err)
	}
	if !acquired {
		closeConn()
		return nil, ErrBusy
	}
	var session LockSession
	if err := conn.QueryRow(ctx, `SELECT pid, backend_start FROM pg_stat_activity WHERE pid = pg_backend_pid()`).
		Scan(&session.PID, &session.Start); err != nil {
		closeConn()
		return nil, fmt.Errorf("reading the backup lock session: %w", err)
	}
	interval := l.CheckInterval
	if interval <= 0 {
		interval = lockCheckInterval
	}
	h := &pgHeld{conn: conn, session: session, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	// The watch outlives ctx on purpose: "Back up now" takes the lock in a
	// request and holds it for the backup that request starts.
	go h.watch(interval) //nolint:gosec // G118: see above
	return h, nil
}

// Holder reads pg_locks. A bigint advisory key is stored as classid (high 32
// bits) and objid (low 32 bits) with objsubid 1.
func (l PostgresLocker) Holder(ctx context.Context) (LockSession, bool, error) {
	if l.Pool == nil {
		return LockSession{}, false, nil
	}
	var s LockSession
	err := l.Pool.QueryRow(ctx, `
		SELECT l.pid, a.backend_start FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
		  AND l.classid::bigint = $1 AND l.objid::bigint = $2
		  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		LIMIT 1`,
		lockClassID, lockObjID).Scan(&s.PID, &s.Start)
	if errors.Is(err, pgx.ErrNoRows) {
		return LockSession{}, false, nil
	}
	return s, err == nil, err
}

// The halves of SubsystemLockKey as pg_locks stores them.
var (
	lockClassID = int64(uint64(SubsystemLockKey) >> 32)
	lockObjID   = int64(uint64(SubsystemLockKey) & 0xffffffff)
)

// pgHeld owns the lock's connection. mu serializes every use of it: the
// session check, status statements, and Release.
type pgHeld struct {
	mu       sync.Mutex
	conn     *pgx.Conn
	session  LockSession
	lost     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	lostOnce sync.Once
	stopOnce sync.Once
	released bool
}

func (h *pgHeld) watch(interval time.Duration) {
	defer close(h.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := h.Exec(ctx, `SELECT 1`)
			cancel()
			if err != nil && !errors.Is(err, errLockReleased) {
				slog.Warn("backup lock session ended; stopping the work that held it", "component", "backup", "error", err)
				h.markLost()
				return
			}
		}
	}
}

func (h *pgHeld) markLost() { h.lostOnce.Do(func() { close(h.lost) }) }

func (h *pgHeld) Lost() <-chan struct{} { return h.lost }

func (h *pgHeld) Session() LockSession { return h.session }

func (h *pgHeld) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return pgconn.CommandTag{}, errLockReleased
	}
	tag, err := h.conn.Exec(ctx, sql, args...)
	if err != nil && h.conn.IsClosed() {
		// The session is gone, and the lock with it.
		h.markLost()
	}
	return tag, err
}

func (h *pgHeld) Release(ctx context.Context) {
	h.stopOnce.Do(func() { close(h.stop) })
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return
	}
	h.released = true
	// Unlock first, so the release is visible as soon as Release returns; a
	// closing backend can still show in pg_locks for a moment. Then close the
	// session, retiring it: one session, one acquisition. Closing releases the
	// lock even when the unlock failed.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := h.conn.Exec(closeCtx, `SELECT pg_advisory_unlock($1)`, SubsystemLockKey); err != nil && !h.conn.IsClosed() {
		slog.WarnContext(ctx, "unlocking the backup lock failed; closing its session", "component", "backup", "error", err)
	}
	if err := h.conn.Close(closeCtx); err != nil {
		slog.WarnContext(ctx, "closing the backup lock session failed", "component", "backup", "error", err)
	}
}

// withLost returns a context canceled when held's session ends, so a dead
// lock stops pg_dump, uploads, and restores that were running under it.
func withLost(ctx context.Context, held Held) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		select {
		case <-held.Lost():
			cancel(errLockLost)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}
