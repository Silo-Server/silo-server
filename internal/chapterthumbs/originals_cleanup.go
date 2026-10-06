package chapterthumbs

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// chapterImagesPrefix is the storage namespace chapter thumbnails live under.
const chapterImagesPrefix = "chapter-images/"

// Earlier builds stored every chapter thumbnail twice: a full-size
// original.webp next to the w300.webp clients were actually served. A
// migration points existing chapter rows at w300.webp, and its trigger keeps
// every later write there too, including writes from a node still on an
// earlier build. OriginalsCleaner then deletes the original.webp objects
// nothing reads any more.
const (
	// originalsCleanupPageSize is the storage listing page size. One page
	// costs one list call, one database query and at most one delete call.
	originalsCleanupPageSize = 1000

	// originalsCleanupMinAge keeps the cleanup away from objects a node on an
	// earlier build may be writing during a rolling upgrade.
	originalsCleanupMinAge = 24 * time.Hour
)

// originalsCleanupAdvisoryLock serializes the cleanup across nodes, whose task
// schedulers all fire it. The holder reads, advances and writes the shared
// checkpoint, so two nodes never interleave their passes.
const originalsCleanupAdvisoryLock int64 = 0x53494C4F43485452 // "SILOCHTR"

// OriginalsStore is the storage surface the cleanup needs.
type OriginalsStore interface {
	Delete(ctx context.Context, keys []string) (int, error)
	List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error)
}

// OriginalsCleanupStats summarizes cleanup work.
type OriginalsCleanupStats struct {
	Scanned   int `json:"scanned"`
	Originals int `json:"originals"`
	Deleted   int `json:"deleted"`
	// Referenced counts originals kept because a chapter row references them.
	// The migration's trigger makes that impossible; the check fails safe.
	Referenced int `json:"referenced"`
	TooNew     int `json:"too_new"`
	// DeleteFailed counts originals a delete call did not remove.
	DeleteFailed int `json:"delete_failed"`
	Pages        int `json:"pages"`
}

// Add accumulates another page's counts.
func (s *OriginalsCleanupStats) Add(o OriginalsCleanupStats) {
	s.Scanned += o.Scanned
	s.Originals += o.Originals
	s.Deleted += o.Deleted
	s.Referenced += o.Referenced
	s.TooNew += o.TooNew
	s.DeleteFailed += o.DeleteFailed
	s.Pages += o.Pages
}

// OriginalsCleaner deletes the full-size chapter thumbnail originals that
// earlier builds stored and no client requests.
type OriginalsCleaner struct {
	pool  *pgxpool.Pool
	store OriginalsStore
	now   func() time.Time
	// referenced returns the subset of keys some chapter row still holds as
	// its thumbnail_path. Tests substitute it to run without a database.
	referenced func(ctx context.Context, fileIDs []int, keys []string) (map[string]struct{}, error)
}

// NewOriginalsCleaner returns nil when the cleanup cannot run.
func NewOriginalsCleaner(pool *pgxpool.Pool, store OriginalsStore) *OriginalsCleaner {
	if pool == nil || store == nil {
		return nil
	}
	c := &OriginalsCleaner{pool: pool, store: store, now: time.Now}
	c.referenced = c.referencedOriginals
	return c
}

// parseOriginalKey returns the media file ID of a key shaped exactly like a
// legacy chapter thumbnail original,
// chapter-images/{file_id}/{chapter_index}/original.webp. Every other key,
// including the w300.webp thumbnails clients load, is rejected and never
// deleted.
func parseOriginalKey(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, chapterImagesPrefix)
	if !ok {
		return 0, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[2] != "original.webp" {
		return 0, false
	}
	fileID, err := strconv.Atoi(parts[0])
	if err != nil || fileID <= 0 || strconv.Itoa(fileID) != parts[0] {
		return 0, false
	}
	chapterIndex, err := strconv.Atoi(parts[1])
	if err != nil || chapterIndex < 0 || strconv.Itoa(chapterIndex) != parts[1] {
		return 0, false
	}
	return fileID, true
}

// referencedOriginals returns the keys a chapter row still points at.
func (c *OriginalsCleaner) referencedOriginals(ctx context.Context, fileIDs []int, keys []string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := c.pool.Query(ctx, `
		SELECT e->>'thumbnail_path'
		FROM media_files, jsonb_array_elements(chapters) e
		WHERE id = ANY($1)
		  AND jsonb_typeof(chapters) = 'array'
		  AND e->>'thumbnail_path' = ANY($2)`, fileIDs, keys)
	if err != nil {
		return nil, fmt.Errorf("chapter thumbnail cleanup: reference check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("chapter thumbnail cleanup: scan reference: %w", err)
		}
		out[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chapter thumbnail cleanup: references: %w", err)
	}
	return out, nil
}

// Exclusive runs fn while holding the cluster-wide cleanup lock. It reports
// false, without running fn, when another node holds the lock.
func (c *OriginalsCleaner) Exclusive(ctx context.Context, fn func() error) (bool, error) {
	if c.pool == nil {
		return true, fn()
	}
	lock, acquired, err := pglock.TryAcquire(ctx, c.pool, originalsCleanupAdvisoryLock)
	if err != nil {
		return false, fmt.Errorf("chapter thumbnail cleanup: acquiring lock: %w", err)
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		if err := lock.Release(ctx); err != nil {
			slog.WarnContext(ctx, "chapter thumbnail cleanup: releasing lock failed",
				"component", "chapterthumbs", "error", err)
		}
	}()
	return true, fn()
}

// Page cleans one listing page of chapter-images/ starting after token. It
// returns the token to continue from, empty at the end of the listing.
//
// Originals younger than the age floor, or that a chapter row references, are
// left alone.
func (c *OriginalsCleaner) Page(ctx context.Context, token string) (OriginalsCleanupStats, string, error) {
	var stats OriginalsCleanupStats
	infos, next, err := c.store.List(ctx, chapterImagesPrefix, token, originalsCleanupPageSize)
	if err != nil {
		return stats, token, fmt.Errorf("chapter thumbnail cleanup: list: %w", err)
	}
	stats.Pages = 1

	cutoff := c.now().Add(-originalsCleanupMinAge)
	var keys []string
	var fileIDs []int
	for _, info := range infos {
		stats.Scanned++
		fileID, ok := parseOriginalKey(info.Key)
		if !ok {
			continue
		}
		stats.Originals++
		// A missing timestamp fails closed, as in the artwork sweep: it
		// gives no way to tell a just-written object from an old one.
		if info.ModTime.IsZero() || info.ModTime.After(cutoff) {
			stats.TooNew++
			continue
		}
		keys = append(keys, info.Key)
		fileIDs = append(fileIDs, fileID)
	}

	referenced, err := c.referenced(ctx, fileIDs, keys)
	if err != nil {
		return stats, token, err
	}
	doomed := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := referenced[key]; ok {
			stats.Referenced++
			continue
		}
		doomed = append(doomed, key)
	}
	if len(doomed) > 0 {
		deleted, err := c.store.Delete(ctx, doomed)
		stats.Deleted += deleted
		if err != nil {
			return stats, token, fmt.Errorf("chapter thumbnail cleanup: delete: %w", err)
		}
		if deleted != len(doomed) {
			// The objects stay unreferenced, so the next pass retries them.
			stats.DeleteFailed += len(doomed) - deleted
			slog.WarnContext(ctx, "chapter thumbnail cleanup: partial delete",
				"component", "chapterthumbs", "requested", len(doomed), "deleted", deleted)
		}
	}
	return stats, next, nil
}
