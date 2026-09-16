package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// The artwork revision GC only ever sees revisions that were enqueued into
// artwork_revision_gc_candidates. A superseded revision that was never enqueued
// -- because the row was lost, the enqueue failed, or the displacement predates
// the queue -- is invisible to it forever, and nothing else in the server reads
// the bucket back. That is a one-way leak: the objects stay until someone
// deletes them by hand.
//
// This sweep closes it from the other side. It walks the bucket and deletes
// objects the catalog does not reference, which makes it a backstop for the GC
// rather than a replacement: the GC still collects promptly on displacement,
// and this catches whatever the GC never learned about.
//
// Measured on a reference deployment before this existed: 2,244,601 orphaned
// objects, 178 GB, 27% of the artwork bucket.

const (
	// artworkSweepPageSize is the storage listing page size. One page costs one
	// list call, one database query and at most one delete call, so larger
	// pages mean proportionally fewer round trips against storage that
	// charges per call rather than per object.
	artworkSweepPageSize = 1000

	// artworkSweepMinAge keeps the sweep away from objects the cache pipeline
	// may still be in the middle of writing. An object written after the
	// catalog row it belongs to is read looks unreferenced through no fault
	// of its own, and deleting it would destroy artwork that was just cached.
	artworkSweepMinAge = 24 * time.Hour

	// artworkSweepAnomalyRatio fails the sweep closed. Every way the
	// reference check can go wrong -- a query error swallowed upstream, a
	// schema change that empties a surface, a key-prefix mismatch that makes
	// every reconstructed path miss -- looks identical from here: a page in
	// which almost nothing is referenced. Deleting on that signal would erase
	// the library, so a page this lopsided stops the sweep instead.
	artworkSweepAnomalyRatio = 0.8

	// artworkSweepAnomalyFloor exempts small pages from the ratio check. The
	// final page of a prefix is legitimately short and may be entirely
	// unreferenced without anything being wrong.
	artworkSweepAnomalyFloor = 50
)

// artworkStorageSweepAdvisoryLock serializes the sweep across nodes. Silo
// deploys as a cluster, every node runs its own triggers, and the checkpoint is
// a single shared settings row: two nodes sweeping at once would overwrite each
// other's cursor, so a prefix could stay unswept while both repeatedly re-walk
// the same region. Deletions themselves are idempotent, so this protects
// progress rather than correctness.
const artworkStorageSweepAdvisoryLock int64 = 0x53494C4F53574550 // "SILOSWEP"

// ArtworkStorageLister is the storage surface the sweep needs on top of
// deletion: a bounded, resumable listing.
type ArtworkStorageLister interface {
	Delete(ctx context.Context, keys []string) (int, error)
	List(ctx context.Context, prefix, cursor string, limit int) ([]artworkstore.ObjectInfo, string, error)
}

// ArtworkStorageSweepStats summarizes one bounded sweep.
type ArtworkStorageSweepStats struct {
	Scanned          int    `json:"scanned"`
	Referenced       int    `json:"referenced"`
	TooNew           int    `json:"too_new"`
	Unparsable       int    `json:"unparsable"`
	Deleted          int    `json:"deleted"`
	Pages            int    `json:"pages"`
	Skipped          bool   `json:"skipped"`
	NextToken        string `json:"next_token"`
	PrefixDone       bool   `json:"prefix_done"`
	StoppedOnAnomaly bool   `json:"stopped_on_anomaly"`
}

// ArtworkStorageSweeper deletes stored artwork objects that no catalog surface
// references.
type ArtworkStorageSweeper struct {
	pool  *pgxpool.Pool
	store ArtworkStorageLister
	now   func() time.Time
	// lookup resolves which candidate paths the catalog still references.
	// Defaults to the database query; tests substitute it so the deletion
	// guards can be exercised without a live catalog.
	lookup func(ctx context.Context, paths []string) (map[string]struct{}, error)
}

// NewArtworkStorageSweeper returns nil when the sweep cannot run, matching the
// garbage collector's construction contract.
func NewArtworkStorageSweeper(pool *pgxpool.Pool, store ArtworkStorageLister) *ArtworkStorageSweeper {
	if pool == nil || store == nil {
		return nil
	}
	sweeper := &ArtworkStorageSweeper{pool: pool, store: store, now: time.Now}
	sweeper.lookup = sweeper.referencedOriginals
	return sweeper
}

// artworkObjectKey is a stored object decomposed into the parts that decide
// whether the catalog still references it.
type artworkObjectKey struct {
	key      string
	original string
	modified time.Time
}

// parseArtworkObjectKey rebuilds the path the catalog would hold for an object.
//
// Every cached artwork column stores the "original" variant of its ladder --
// verified across 2,139,166 cached rows on the reference deployment, where
// "original" was the only variant present -- so an object at
// <dir>/<variant>.<hash>.<ext> is referenced exactly when the catalog holds
// <dir>/original.<hash>.<ext>. Reconstructing that lets the check be an
// indexed equality lookup instead of a scan.
//
// Returns false for anything that does not decompose, which the caller counts
// and skips. Refusing to guess is the point: an unrecognized key shape is the
// one case where deleting could destroy something this code does not model.
func parseArtworkObjectKey(info artworkstore.ObjectInfo) (artworkObjectKey, bool) {
	dir, file := path.Split(info.Key)
	if dir == "" || file == "" {
		return artworkObjectKey{}, false
	}
	parts := strings.Split(file, ".")
	if len(parts) != 3 {
		return artworkObjectKey{}, false
	}
	variant, hash, ext := parts[0], parts[1], parts[2]
	if variant == "" || hash == "" || ext == "" {
		return artworkObjectKey{}, false
	}
	return artworkObjectKey{
		key:      info.Key,
		original: dir + "original." + hash + "." + ext,
		modified: info.ModTime,
	}, true
}

// referencedOriginals returns the subset of candidate original-variant paths
// that some catalog surface still holds, using the same union the garbage
// collector checks against so the sweep can never take a narrower view of
// "referenced" than the GC itself.
func (s *ArtworkStorageSweeper) referencedOriginals(ctx context.Context, paths []string) (map[string]struct{}, error) {
	referenced := make(map[string]struct{}, len(paths))
	if len(paths) == 0 {
		return referenced, nil
	}
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT path FROM ("+artworkReferenceUnionSQL("$1")+") refs", paths)
	if err != nil {
		return nil, fmt.Errorf("artwork storage sweep: reference check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("artwork storage sweep: scan reference: %w", err)
		}
		referenced[p] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork storage sweep: references: %w", err)
	}
	return referenced, nil
}

// SweepPrefix walks one prefix from the supplied continuation token, deleting
// unreferenced objects, and stops after maxPages. It returns the token to
// resume from; an empty token with PrefixDone set means the prefix is finished.
func (s *ArtworkStorageSweeper) SweepPrefix(ctx context.Context, prefix, token string, maxPages int) (ArtworkStorageSweepStats, error) {
	stats := ArtworkStorageSweepStats{NextToken: token}
	if maxPages < 1 {
		maxPages = 1
	}

	// Only one node sweeps at a time. Another node holding the lock is the
	// normal case in a cluster, not an error: skip this run and leave its
	// checkpoint untouched rather than racing it.
	if s.pool != nil {
		lock, acquired, err := pglock.TryAcquire(ctx, s.pool, artworkStorageSweepAdvisoryLock)
		if err != nil {
			return stats, fmt.Errorf("artwork storage sweep: acquiring sweep lock: %w", err)
		}
		if !acquired {
			stats.Skipped = true
			return stats, nil
		}
		defer func() {
			if releaseErr := lock.Release(ctx); releaseErr != nil {
				slog.WarnContext(ctx, "artwork storage sweep: releasing sweep lock failed",
					"component", "metadata", "error", releaseErr)
			}
		}()
	}

	cutoff := s.now().Add(-artworkSweepMinAge)

	for page := 0; page < maxPages; page++ {
		infos, next, err := s.store.List(ctx, prefix, stats.NextToken, artworkSweepPageSize)
		if err != nil {
			return stats, fmt.Errorf("artwork storage sweep: list %s: %w", prefix, err)
		}
		stats.Pages++

		parsed := make([]artworkObjectKey, 0, len(infos))
		candidates := make([]string, 0, len(infos))
		for _, info := range infos {
			stats.Scanned++
			object, ok := parseArtworkObjectKey(info)
			if !ok {
				stats.Unparsable++
				continue
			}
			// An object younger than the age floor is skipped without ever
			// reaching the reference check, so a mid-write object cannot be
			// deleted even if the catalog has not caught up to it yet.
			//
			// A missing timestamp fails closed. Storage that does not report a
			// modification time gives no way to tell a just-written object from
			// an ancient one, and guessing "old" there would silently disable
			// the age floor for every object it applies to. Skipping costs a
			// little unreclaimed space; guessing costs freshly cached artwork.
			if object.modified.IsZero() || object.modified.After(cutoff) {
				stats.TooNew++
				continue
			}
			parsed = append(parsed, object)
			candidates = append(candidates, object.original)
		}

		referenced, err := s.lookup(ctx, candidates)
		if err != nil {
			return stats, err
		}

		doomed := make([]string, 0, len(parsed))
		for _, object := range parsed {
			if _, ok := referenced[object.original]; ok {
				stats.Referenced++
				continue
			}
			doomed = append(doomed, object.key)
		}

		// Fail closed on an implausible page rather than deleting on what is
		// more likely a broken reference check than a genuinely empty catalog.
		if len(parsed) >= artworkSweepAnomalyFloor &&
			float64(len(doomed)) > artworkSweepAnomalyRatio*float64(len(parsed)) {
			stats.StoppedOnAnomaly = true
			return stats, fmt.Errorf(
				"artwork storage sweep: %d of %d objects on one page of %s looked unreferenced; refusing to delete and stopping (check the catalog and the storage key prefix)",
				len(doomed), len(parsed), prefix,
			)
		}

		if len(doomed) > 0 {
			deleted, delErr := s.store.Delete(ctx, doomed)
			stats.Deleted += deleted
			if delErr != nil {
				return stats, fmt.Errorf("artwork storage sweep: delete: %w", delErr)
			}
			if deleted != len(doomed) {
				// A short count means per-object failures. The objects stay
				// unreferenced, so the next pass over this prefix retries
				// them; there is nothing to repair and nothing is lost.
				slog.WarnContext(ctx, "artwork storage sweep: partial delete",
					"component", "metadata", "prefix", prefix, "requested", len(doomed), "deleted", deleted)
			}
		}

		stats.NextToken = next
		if next == "" {
			stats.PrefixDone = true
			return stats, nil
		}
	}
	return stats, nil
}
