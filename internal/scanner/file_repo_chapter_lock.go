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
)

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
	// A session lock must outlive the reads and writes made during extraction.
	// Keep it outside the data pool so even a one-connection pool can run them.
	// There is at most one such session per active chapter worker.
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connConfig := r.pool.Config().ConnConfig.Copy()
	connConfig.RuntimeParams["application_name"] = "silo-chapter-thumbnails:" + strconv.Itoa(fileID)
	conn, err := pgx.ConnectConfig(connectCtx, connConfig)
	if err != nil {
		return ctx, nil, false, fmt.Errorf("connect chapter lock session: %w", err)
	}
	closeSession := sync.OnceFunc(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := conn.Close(closeCtx); err != nil {
			slog.WarnContext(ctx, "chapter thumbnail lock could not be released", "component", "chapterthumbs", "file_id", fileID, "error", err)
		}
	})
	var acquired bool
	if err := conn.QueryRow(connectCtx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
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
