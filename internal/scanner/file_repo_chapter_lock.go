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

// Only active sessions retain their pool in the registry. Repositories sharing
// a query pool also share its chapter session budget.
var chapterLockSessions = struct {
	sync.Mutex
	active map[*pgxpool.Pool]int
}{active: make(map[*pgxpool.Pool]int)}

func tryReserveChapterLockSession(pool *pgxpool.Pool) (func(), bool) {
	// Keep most query connections available even during a large backfill.
	limit := min(4, max(1, int(pool.Config().MaxConns)/4))
	chapterLockSessions.Lock()
	defer chapterLockSessions.Unlock()
	if chapterLockSessions.active[pool] >= limit {
		return nil, false
	}
	chapterLockSessions.active[pool]++
	return sync.OnceFunc(func() {
		chapterLockSessions.Lock()
		defer chapterLockSessions.Unlock()
		chapterLockSessions.active[pool]--
		if chapterLockSessions.active[pool] == 0 {
			delete(chapterLockSessions.active, pool)
		}
	}), true
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
	// Admission is nonblocking, just like the advisory lock. The coordinator
	// rediscovers skipped files on its next scan.
	releaseBudget, reserved := tryReserveChapterLockSession(r.pool)
	if !reserved {
		return ctx, nil, false, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var conn *pgx.Conn
	var pooled *pgxpool.Conn
	var err error
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
	closeSession := sync.OnceFunc(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := conn.Close(closeCtx); err != nil {
			slog.WarnContext(ctx, "chapter thumbnail lock could not be released", "component", "chapterthumbs", "file_id", fileID, "error", err)
		}
		if pooled != nil {
			pooled.Release()
		}
		releaseBudget()
	})
	var acquired bool
	var applicationName string
	if err := conn.QueryRow(connectCtx, `SELECT set_config('application_name', $2, false), pg_try_advisory_lock($1)`,
		key, "silo-chapter-thumbnails:"+strconv.Itoa(fileID)).Scan(&applicationName, &acquired); err != nil {
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
