package chapterthumbs

import (
	"context"
	"errors"
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
// migration points existing chapter rows at w300.webp; OriginalsCleaner then
// deletes the original.webp objects nothing reads any more.
const (
	// originalsCleanupPageSize is the storage listing page size. One page
	// costs one list call, one database query and at most one delete call.
	originalsCleanupPageSize = 1000

	// originalsCleanupMinAge keeps the cleanup away from objects written by a
	// node still running an earlier build during a rolling upgrade: that build
	// uploads original.webp before it saves the chapter row that references
	// it, so a fresh object can look unreferenced for a moment.
	originalsCleanupMinAge = 24 * time.Hour
)

// originalsCleanupAdvisoryLock serializes the cleanup across nodes, whose task
// schedulers all fire it. The holder reads, advances and writes the shared
// checkpoint, so two nodes never interleave their passes.
const originalsCleanupAdvisoryLock int64 = 0x53494C4F43485452 // "SILOCHTR"

// OriginalsStore is the storage surface the cleanup needs.
type OriginalsStore interface {
	Stat(ctx context.Context, key string) (blobstore.ObjectInfo, error)
	Delete(ctx context.Context, keys []string) (int, error)
	List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error)
}

// OriginalsCleanupStats summarizes cleanup work.
type OriginalsCleanupStats struct {
	Scanned   int `json:"scanned"`
	Originals int `json:"originals"`
	Deleted   int `json:"deleted"`
	// Repointed counts originals a chapter row still referenced; the row was
	// moved to the w300 image beside it before the original was deleted.
	Repointed int `json:"repointed"`
	// Referenced counts originals kept because a row references them and no
	// w300 image exists to move the row to.
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
	s.Repointed += o.Repointed
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
	// repoint moves chapter rows holding any of keys to the w300 image beside
	// each. Tests substitute it to run without a database.
	repoint func(ctx context.Context, fileIDs []int, keys []string) error
}

// NewOriginalsCleaner returns nil when the cleanup cannot run.
func NewOriginalsCleaner(pool *pgxpool.Pool, store OriginalsStore) *OriginalsCleaner {
	if pool == nil || store == nil {
		return nil
	}
	c := &OriginalsCleaner{pool: pool, store: store, now: time.Now}
	c.referenced = c.referencedOriginals
	c.repoint = c.repointOriginals
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

// servedSibling is the w300 key stored beside a legacy original.
func servedSibling(originalKey string) string {
	return strings.TrimSuffix(originalKey, "original.webp") + "w300.webp"
}

// referencedOriginals returns the keys a chapter row still points at. After
// the migration, a row does only when a node on an earlier build saved it
// during the upgrade.
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

// repointOriginals is the migration's rewrite, limited to the given keys.
func (c *OriginalsCleaner) repointOriginals(ctx context.Context, fileIDs []int, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := c.pool.Exec(ctx, `
		UPDATE media_files
		SET chapters = (
		    SELECT jsonb_agg(
		        CASE
		            WHEN e->>'thumbnail_path' = ANY($2)
		            THEN jsonb_set(e, '{thumbnail_path}',
		                           to_jsonb(regexp_replace(e->>'thumbnail_path', '/original\.webp$', '/w300.webp')))
		            ELSE e
		        END
		        ORDER BY ord
		    )
		    FROM jsonb_array_elements(chapters) WITH ORDINALITY AS t(e, ord)
		)
		WHERE id = ANY($1)
		  AND jsonb_typeof(chapters) = 'array'
		  AND EXISTS (
		      SELECT 1 FROM jsonb_array_elements(chapters) e
		      WHERE e->>'thumbnail_path' = ANY($2)
		  )`, fileIDs, keys)
	if err != nil {
		return fmt.Errorf("chapter thumbnail cleanup: repoint rows: %w", err)
	}
	return nil
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
// Originals younger than the age floor are left alone. An original a chapter
// row still references has its row moved to the w300 image beside it first,
// and is kept only when that image is missing.
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
	fileOf := make(map[string]int)
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
		fileOf[info.Key] = fileID
	}

	referenced, err := c.referenced(ctx, fileIDs, keys)
	if err != nil {
		return stats, token, err
	}
	var repointKeys []string
	var repointIDs []int
	for _, key := range keys {
		if _, ok := referenced[key]; !ok {
			continue
		}
		_, err := c.store.Stat(ctx, servedSibling(key))
		switch {
		case err == nil:
			repointKeys = append(repointKeys, key)
			repointIDs = append(repointIDs, fileOf[key])
		case errors.Is(err, blobstore.ErrNotFound):
			// Without the w300 image the original is the only picture
			// this chapter has; keep it rather than break the row.
		default:
			return stats, token, fmt.Errorf("chapter thumbnail cleanup: stat %s: %w", servedSibling(key), err)
		}
	}
	if err := c.repoint(ctx, repointIDs, repointKeys); err != nil {
		return stats, token, err
	}
	repointed := make(map[string]struct{}, len(repointKeys))
	for _, key := range repointKeys {
		repointed[key] = struct{}{}
	}

	doomed := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := referenced[key]; ok {
			if _, moved := repointed[key]; !moved {
				stats.Referenced++
				continue
			}
			stats.Repointed++
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
