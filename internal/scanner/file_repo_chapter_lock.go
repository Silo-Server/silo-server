package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Active sessions and waiting workers retain their pool in the registry.
// Repositories sharing a query pool also share its chapter session budget.
type chapterLockBudget struct {
	active  int
	users   int
	changed chan struct{}
}

var chapterLockSessions = struct {
	sync.Mutex
	budgets map[*pgxpool.Pool]*chapterLockBudget
}{budgets: make(map[*pgxpool.Pool]*chapterLockBudget)}

func reserveChapterLockSession(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	// Keep most query connections available even during a large backfill.
	limit := min(4, max(1, int(pool.Config().MaxConns)/4))
	chapterLockSessions.Lock()
	budget := chapterLockSessions.budgets[pool]
	if budget == nil {
		budget = &chapterLockBudget{changed: make(chan struct{})}
		chapterLockSessions.budgets[pool] = budget
	}
	budget.users++
	for {
		if err := ctx.Err(); err != nil {
			budget.users--
			if budget.users == 0 {
				delete(chapterLockSessions.budgets, pool)
			}
			chapterLockSessions.Unlock()
			return nil, err
		}
		if budget.active < limit {
			budget.active++
			chapterLockSessions.Unlock()
			return sync.OnceFunc(func() {
				chapterLockSessions.Lock()
				defer chapterLockSessions.Unlock()
				budget.active--
				budget.users--
				close(budget.changed)
				budget.changed = make(chan struct{})
				if budget.users == 0 {
					delete(chapterLockSessions.budgets, pool)
				}
			}), nil
		}
		changed := budget.changed
		chapterLockSessions.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		chapterLockSessions.Lock()
	}
}

type chapterThumbnailLockKey struct{}

type chapterThumbnailLock struct {
	conn   *pgx.Conn
	repo   *FileRepository
	fileID int
}

type chapterThumbnailStateWriter interface {
	fileQueryer
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *FileRepository) chapterStateWriter(ctx context.Context, fileID int) chapterThumbnailStateWriter {
	if lock, ok := ctx.Value(chapterThumbnailLockKey{}).(chapterThumbnailLock); ok && lock.repo == r && lock.fileID == fileID {
		return lock.conn
	}
	return r.pool
}

// TryLockChapterThumbnails serializes a file's chapter extraction and save
// across API replicas. The caller must read the file after taking the lock
// and release it after saving, so a later worker sees completed thumbnails.
func (r *FileRepository) TryLockChapterThumbnails(ctx context.Context, fileID int) (context.Context, func(), bool, error) {
	// Hash a namespaced decimal ID to retain all bigint ID bits without
	// sharing the key space of other per-file jobs.
	digest := sha256.Sum256([]byte("silo-chapter-thumbnails:" + strconv.Itoa(fileID)))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	// Keep the request with its worker while the session budget is saturated.
	// Fresh files may not have chapters yet, so a width scan cannot retry them.
	releaseBudget, err := reserveChapterLockSession(ctx, r.pool)
	if err != nil {
		return ctx, nil, false, fmt.Errorf("wait for chapter lock session: %w", err)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var conn *pgx.Conn
	var pooled *pgxpool.Conn
	if r.pool.Config().MaxConns == 1 {
		// A one-connection query pool cannot lend a long-lived lock session:
		// extraction also reads settings and folder state through that pool.
		// Its exceptional separate session is capped at one per query pool.
		connConfig := r.pool.Config().ConnConfig.Copy()
		conn, err = pgx.ConnectConfig(connectCtx, connConfig)
	} else {
		// Count lock sessions inside database.max_connections. Admission leaves
		// room for ordinary queries and uses the same budget across repositories.
		pooled, err = r.pool.Acquire(connectCtx)
		if err == nil {
			conn = pooled.Conn()
		}
	}
	if err != nil {
		releaseBudget()
		return ctx, nil, false, fmt.Errorf("connect chapter lock session: %w", err)
	}
	originalApplicationName := conn.PgConn().ParameterStatus("application_name")
	var acquired bool
	var acquireFailed bool
	closeSession := sync.OnceFunc(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Healthy pooled connections retain their physical session. Release the
		// advisory lock and restore its diagnostic name before returning it.
		// Any failed cleanup discards the session, preventing a leaked lock.
		var cleanupErr error
		if pooled != nil && !acquireFailed && !conn.IsClosed() {
			if acquired {
				var unlocked bool
				var restored string
				cleanupErr = conn.QueryRow(closeCtx, `SELECT pg_advisory_unlock($1), set_config('application_name', $2, false)`,
					key, originalApplicationName).Scan(&unlocked, &restored)
				if cleanupErr == nil && !unlocked {
					cleanupErr = fmt.Errorf("chapter advisory lock was no longer held")
				}
			} else {
				_, cleanupErr = conn.Exec(closeCtx, `SELECT set_config('application_name', $1, false)`, originalApplicationName)
			}
		}
		if pooled == nil || acquireFailed || cleanupErr != nil || conn.IsClosed() {
			if err := conn.Close(closeCtx); err != nil {
				slog.WarnContext(ctx, "chapter thumbnail lock session could not be closed", "component", "chapterthumbs", "file_id", fileID, "error", err)
			}
		}
		if cleanupErr != nil {
			slog.WarnContext(ctx, "chapter thumbnail lock cleanup failed", "component", "chapterthumbs", "file_id", fileID, "error", cleanupErr)
		}
		if pooled != nil {
			pooled.Release()
		}
		releaseBudget()
	})
	var applicationName string
	if err := conn.QueryRow(connectCtx, `SELECT set_config('application_name', $2, false), pg_try_advisory_lock($1)`,
		key, "silo-chapter-thumbnails:"+strconv.Itoa(fileID)).Scan(&applicationName, &acquired); err != nil {
		// A failed result read cannot prove whether PostgreSQL took the lock.
		// Discard its session rather than returning a possibly locked one.
		acquireFailed = true
		closeSession()
		return ctx, nil, false, fmt.Errorf("take chapter lock: %w", err)
	}
	if !acquired {
		closeSession()
		return ctx, nil, false, nil
	}
	lockCtx := context.WithValue(ctx, chapterThumbnailLockKey{}, chapterThumbnailLock{conn: conn, repo: r, fileID: fileID})
	return lockCtx, closeSession, true, nil
}
