package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

const (
	artworkRevisionGCBatchSize = 100
	artworkRevisionGCLease     = 15 * time.Minute
	// artworkRevisionDormantRecheck bounds how stale a parked (referenced)
	// revision may get before the sweep re-verifies it. Displacement triggers
	// are the fast path; the sweep guarantees a reference that disappears
	// through an untriggered surface still becomes collectible eventually.
	artworkRevisionDormantRecheck = 24 * time.Hour
)

// ArtworkRevisionDeleter is the object-storage surface used by revision GC.
type ArtworkRevisionDeleter interface {
	DeleteObjects(ctx context.Context, bucket string, keys []string) (int, error)
	Bucket() string
}

// ArtworkRevisionGCStats summarizes one bounded cleanup pass.
type ArtworkRevisionGCStats struct {
	Claimed         int `json:"claimed"`
	Deleted         int `json:"deleted"`
	Referenced      int `json:"referenced"`
	Retried         int `json:"retried"`
	DormantChecked  int `json:"dormant_checked"`
	DormantRequeued int `json:"dormant_requeued"`
	Healed          int `json:"healed"`
}

// ArtworkRevisionGarbageCollector deletes unpublished or displaced immutable
// revisions only after their grace period and while no catalog surface
// references them. Work is leased with SKIP LOCKED so multiple workers are safe.
type ArtworkRevisionGarbageCollector struct {
	pool *pgxpool.Pool
	s3   ArtworkRevisionDeleter
}

func NewArtworkRevisionGarbageCollector(pool *pgxpool.Pool, s3 ArtworkRevisionDeleter) *ArtworkRevisionGarbageCollector {
	if pool == nil || s3 == nil {
		return nil
	}
	return &ArtworkRevisionGarbageCollector{pool: pool, s3: s3}
}

// Run processes one bounded batch. Failed deletions are retried with
// exponential backoff; an expired lease is recoverable by another worker.
func (g *ArtworkRevisionGarbageCollector) Run(ctx context.Context) (ArtworkRevisionGCStats, error) {
	stats := ArtworkRevisionGCStats{}
	if g == nil || g.pool == nil || g.s3 == nil {
		return stats, fmt.Errorf("artwork revision GC is not configured")
	}

	workerID := uuid.NewString()
	candidates, err := g.claim(ctx, workerID, artworkRevisionGCBatchSize)
	if err != nil {
		return stats, err
	}

	// Park referenced candidates with one batched reference check instead of a
	// per-candidate transaction; most publish/re-cache churn lands here. The
	// per-candidate path below re-verifies under the row lock before deleting,
	// so a stale answer from this pre-check can only cost extra work, never a
	// wrong deletion.
	due := candidates
	if len(candidates) > 0 {
		if referenced, refErr := g.referencedPaths(ctx, candidatePaths(candidates)); refErr == nil {
			var parked []int64
			due = due[:0]
			for _, candidate := range candidates {
				// Rows whose objects were already deleted must finish their
				// pending heal; a reference to them is broken, not live.
				if _, ok := referenced[candidate.originalPath]; ok && candidate.deletedAt == nil {
					parked = append(parked, candidate.id)
					continue
				}
				due = append(due, candidate)
			}
			if len(parked) > 0 {
				if parkErr := g.parkClaimed(ctx, parked, workerID); parkErr != nil {
					return stats, parkErr
				}
				stats.Referenced += len(parked)
			}
		} else {
			slog.WarnContext(ctx, "artwork revision GC: batched reference pre-check failed; falling back to per-candidate checks",
				"component", "metadata", "error", refErr)
		}
	}

	batchStats, err := processArtworkRevisionGCBatch(
		due,
		func(candidate artworkRevisionGCCandidate) (artworkRevisionGCOutcome, error) {
			return g.processCandidate(ctx, candidate, workerID)
		},
		func(candidate artworkRevisionGCCandidate, cause error) error {
			return g.retry(ctx, candidate, workerID, cause)
		},
	)
	stats.Claimed = len(candidates)
	stats.Deleted = batchStats.Deleted
	stats.Referenced += batchStats.Referenced
	stats.Retried = batchStats.Retried
	stats.Healed = batchStats.Healed

	checked, requeued, sweepErr := g.sweepDormant(ctx, artworkRevisionGCBatchSize)
	stats.DormantChecked = checked
	stats.DormantRequeued = requeued
	if err == nil {
		err = sweepErr
	}
	return stats, err
}

func processArtworkRevisionGCBatch(
	candidates []artworkRevisionGCCandidate,
	process func(artworkRevisionGCCandidate) (artworkRevisionGCOutcome, error),
	retry func(artworkRevisionGCCandidate, error) error,
) (ArtworkRevisionGCStats, error) {
	stats := ArtworkRevisionGCStats{Claimed: len(candidates)}
	var firstErr error
	for _, candidate := range candidates {
		outcome, err := process(candidate)
		if err != nil {
			stats.Retried++
			if retryErr := retry(candidate, err); retryErr != nil && firstErr == nil {
				firstErr = retryErr
			}
			continue
		}
		switch outcome {
		case artworkRevisionGCReferenced:
			stats.Referenced++
		case artworkRevisionGCDeleted:
			stats.Deleted++
		case artworkRevisionGCDeletedAndHealed:
			stats.Deleted++
			stats.Healed++
		}
	}
	return stats, firstErr
}

type artworkRevisionGCOutcome int

const (
	artworkRevisionGCSuperseded artworkRevisionGCOutcome = iota
	artworkRevisionGCReferenced
	artworkRevisionGCDeleted
	artworkRevisionGCDeletedAndHealed
)

type artworkRevisionGCCandidate struct {
	id           int64
	originalPath string
	imageType    string
	objectKeys   []string
	attemptCount int
	deletedAt    *time.Time
}

func candidatePaths(candidates []artworkRevisionGCCandidate) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.originalPath)
	}
	return paths
}

func (g *ArtworkRevisionGarbageCollector) claim(ctx context.Context, workerID string, limit int) ([]artworkRevisionGCCandidate, error) {
	rows, err := g.pool.Query(ctx, `
		WITH due AS (
			SELECT id
			FROM artwork_revision_gc_candidates
			WHERE not_before <= NOW()
			  AND next_attempt_at <= NOW()
			  AND (locked_at IS NULL OR locked_at < NOW() - ($3 * interval '1 second'))
			ORDER BY next_attempt_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE artwork_revision_gc_candidates AS candidate
		SET locked_at = NOW(), locked_by = $2, updated_at = NOW()
		FROM due
		WHERE candidate.id = due.id
		RETURNING candidate.id, candidate.original_path, candidate.image_type, candidate.object_keys, candidate.attempt_count, candidate.deleted_at`,
		limit, workerID, int64(artworkRevisionGCLease/time.Second))
	if err != nil {
		return nil, fmt.Errorf("artwork revision GC: claim: %w", err)
	}
	defer rows.Close()

	var candidates []artworkRevisionGCCandidate
	for rows.Next() {
		var candidate artworkRevisionGCCandidate
		if err := rows.Scan(&candidate.id, &candidate.originalPath, &candidate.imageType, &candidate.objectKeys, &candidate.attemptCount, &candidate.deletedAt); err != nil {
			return nil, fmt.Errorf("artwork revision GC: scan claim: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork revision GC: claims: %w", err)
	}
	return candidates, nil
}

// parkClaimed releases claimed candidates back to the dormant state in one
// statement. Used when the batched pre-check already proved them referenced.
func (g *ArtworkRevisionGarbageCollector) parkClaimed(ctx context.Context, ids []int64, workerID string) error {
	_, err := g.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET next_attempt_at = NULL,
			attempt_count = 0,
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = NOW()
		WHERE id = ANY($1) AND locked_by = $2`, ids, workerID)
	if err != nil {
		return fmt.Errorf("artwork revision GC: park referenced revisions: %w", err)
	}
	return nil
}

// processCandidate holds the registry row lock across the last reference check
// and object deletion. A concurrent cache attempt registers the revision before
// uploading and therefore waits here; once deletion commits, that attempt can
// safely recreate the complete object set before publication.
func (g *ArtworkRevisionGarbageCollector) processCandidate(
	ctx context.Context,
	candidate artworkRevisionGCCandidate,
	workerID string,
) (artworkRevisionGCOutcome, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: begin deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var originalPath, imageType string
	var objectKeys []string
	var deletedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT original_path, image_type, object_keys, deleted_at
		FROM artwork_revision_gc_candidates
		WHERE id = $1 AND locked_by = $2
		FOR UPDATE`, candidate.id, workerID).Scan(&originalPath, &imageType, &objectKeys, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return artworkRevisionGCSuperseded, nil
	}
	if err != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: lock candidate: %w", err)
	}

	// Once objects are deleted, a lingering reference is broken rather than
	// live: skip parking and finish the pending heal instead.
	if deletedAt == nil {
		referenced, err := g.isReferenced(ctx, tx, originalPath)
		if err != nil {
			return artworkRevisionGCSuperseded, err
		}
		if referenced {
			if _, err := tx.Exec(ctx, `
				UPDATE artwork_revision_gc_candidates
				SET next_attempt_at = NULL,
					attempt_count = 0,
					locked_at = NULL,
					locked_by = '',
					last_error = '',
					updated_at = NOW()
				WHERE id = $1 AND locked_by = $2`, candidate.id, workerID); err != nil {
				return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: park referenced revision: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: commit referenced revision: %w", err)
			}
			return artworkRevisionGCReferenced, nil
		}
	}

	// Rows queued by the displacement triggers carry no manifest; expand the
	// expected object set from the image type so the variant ladder stays
	// defined once, in artworkkey.
	if len(objectKeys) == 0 {
		objectKeys = artworkkey.ObjectKeys(originalPath, imageType)
	}
	if len(objectKeys) > 0 {
		deleted, err := g.s3.DeleteObjects(ctx, g.s3.Bucket(), objectKeys)
		if err == nil && deleted != len(objectKeys) {
			err = fmt.Errorf("deleted %d of %d artwork objects", deleted, len(objectKeys))
		}
		if err != nil {
			return artworkRevisionGCSuperseded, err
		}
	}
	// Keep the row until the post-delete heal succeeds: marking deleted_at
	// (instead of deleting the row) preserves a durable retry if healing or
	// the final row removal fails after the objects are already gone.
	if _, err := tx.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET deleted_at = COALESCE(deleted_at, NOW()), updated_at = NOW()
		WHERE id = $1 AND locked_by = $2`, candidate.id, workerID); err != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: mark deleted: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: commit deletion: %w", err)
	}

	// Writers that assign an existing path without registering a revision
	// (bulk upserts copying previously-read values) do not serialize with the
	// row lock above. Detect that narrow race after the fact and reset the
	// affected rows the same way the artwork reconciler handles missing
	// objects, so pipelines re-cache instead of serving 404 artwork.
	healed, healErr := g.healDeletedReferences(ctx, originalPath)
	if healErr != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: post-delete heal: %w", healErr)
	}
	// Healing can itself displace the path again (clearing a referencing row
	// fires the trigger, which re-arms this row and drops our lease), so gate
	// the removal on deleted_at instead of the lease: a concurrent tracker
	// re-registration clears deleted_at and must survive; anything else with
	// deleted_at set is a finished deletion.
	if _, err := g.pool.Exec(ctx, `
		DELETE FROM artwork_revision_gc_candidates
		WHERE id = $1 AND deleted_at IS NOT NULL`, candidate.id); err != nil {
		return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: finish: %w", err)
	}
	if healed {
		slog.WarnContext(ctx, "artwork revision GC: deleted revision was re-referenced concurrently; reset rows for re-cache",
			"component", "metadata", "original_path", originalPath)
		return artworkRevisionGCDeletedAndHealed, nil
	}
	return artworkRevisionGCDeleted, nil
}

type artworkReferenceQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func artworkReferenceUnionSQL() string {
	surfaces := artworkSweepSurfaces()
	parts := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		parts = append(parts, fmt.Sprintf("SELECT %s AS path FROM %s WHERE %s = ANY($1)", surface.pathCol, surface.table, surface.pathCol))
	}
	return strings.Join(parts, " UNION ALL ")
}

func (g *ArtworkRevisionGarbageCollector) isReferenced(ctx context.Context, q artworkReferenceQuerier, originalPath string) (bool, error) {
	var referenced bool
	query := "SELECT EXISTS(" + artworkReferenceUnionSQL() + ")"
	if err := q.QueryRow(ctx, query, []string{originalPath}).Scan(&referenced); err != nil {
		return false, fmt.Errorf("artwork revision GC: check references: %w", err)
	}
	return referenced, nil
}

// referencedPaths returns the subset of paths referenced by any catalog
// surface, using one query per run instead of one per candidate.
func (g *ArtworkRevisionGarbageCollector) referencedPaths(ctx context.Context, paths []string) (map[string]struct{}, error) {
	referenced := make(map[string]struct{})
	if len(paths) == 0 {
		return referenced, nil
	}
	rows, err := g.pool.Query(ctx, "SELECT DISTINCT path FROM ("+artworkReferenceUnionSQL()+") refs", paths)
	if err != nil {
		return nil, fmt.Errorf("artwork revision GC: batch reference check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("artwork revision GC: scan reference: %w", err)
		}
		referenced[path] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artwork revision GC: references: %w", err)
	}
	return referenced, nil
}

// sweepDormant re-verifies a bounded batch of parked revisions whose last
// check is older than the recheck interval, re-arming any that lost every
// reference through a surface without a displacement trigger.
func (g *ArtworkRevisionGarbageCollector) sweepDormant(ctx context.Context, limit int) (checked, requeued int, err error) {
	rows, err := g.pool.Query(ctx, `
		SELECT id, original_path
		FROM artwork_revision_gc_candidates
		WHERE next_attempt_at IS NULL
		  AND updated_at < NOW() - ($2 * interval '1 second')
		ORDER BY updated_at, id
		LIMIT $1`, limit, int64(artworkRevisionDormantRecheck/time.Second))
	if err != nil {
		return 0, 0, fmt.Errorf("artwork revision GC: list dormant revisions: %w", err)
	}
	defer rows.Close()

	ids := make(map[string][]int64)
	var paths []string
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return 0, 0, fmt.Errorf("artwork revision GC: scan dormant revision: %w", err)
		}
		if _, ok := ids[path]; !ok {
			paths = append(paths, path)
		}
		ids[path] = append(ids[path], id)
		checked++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("artwork revision GC: dormant revisions: %w", err)
	}
	if checked == 0 {
		return 0, 0, nil
	}

	referenced, err := g.referencedPaths(ctx, paths)
	if err != nil {
		return checked, 0, err
	}
	var touch, requeue []int64
	for path, pathIDs := range ids {
		if _, ok := referenced[path]; ok {
			touch = append(touch, pathIDs...)
			continue
		}
		requeue = append(requeue, pathIDs...)
	}
	if len(requeue) > 0 {
		if _, err := g.pool.Exec(ctx, `
			UPDATE artwork_revision_gc_candidates
			SET next_attempt_at = GREATEST(not_before, NOW()),
				updated_at = NOW()
			WHERE id = ANY($1) AND next_attempt_at IS NULL`, requeue); err != nil {
			return checked, 0, fmt.Errorf("artwork revision GC: requeue dormant revisions: %w", err)
		}
		requeued = len(requeue)
	}
	if len(touch) > 0 {
		if _, err := g.pool.Exec(ctx, `
			UPDATE artwork_revision_gc_candidates
			SET updated_at = NOW()
			WHERE id = ANY($1) AND next_attempt_at IS NULL`, touch); err != nil {
			return checked, requeued, fmt.Errorf("artwork revision GC: touch dormant revisions: %w", err)
		}
	}
	return checked, requeued, nil
}

// healDeletedReferences resets any row still pointing at a just-deleted
// revision, mirroring the reconciler: rows with a re-downloadable source are
// repointed at it (the image cache pipeline re-caches them); the rest are
// cleared for their owning pipeline to refill.
func (g *ArtworkRevisionGarbageCollector) healDeletedReferences(ctx context.Context, originalPath string) (bool, error) {
	healed := false
	for _, surface := range artworkSweepSurfaces() {
		if surface.sourceCol != "" {
			resetSQL := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $1 AND %s`,
				surface.table, surface.resetSet(), surface.pathCol, surface.remoteSourcePredicate())
			tag, err := g.pool.Exec(ctx, resetSQL, originalPath)
			if err != nil {
				return healed, fmt.Errorf("artwork revision GC: heal %s: %w", surface.name, err)
			}
			healed = healed || tag.RowsAffected() > 0
		}
		clearSQL := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $1 AND NOT (%s)`,
			surface.table, surface.clearSet, surface.pathCol, surface.remoteSourcePredicate())
		tag, err := g.pool.Exec(ctx, clearSQL, originalPath)
		if err != nil {
			return healed, fmt.Errorf("artwork revision GC: heal %s: %w", surface.name, err)
		}
		healed = healed || tag.RowsAffected() > 0
	}
	return healed, nil
}

func (g *ArtworkRevisionGarbageCollector) retry(ctx context.Context, candidate artworkRevisionGCCandidate, workerID string, cause error) error {
	delay := time.Minute << min(candidate.attemptCount, 10)
	_, err := g.pool.Exec(ctx, `
		UPDATE artwork_revision_gc_candidates
		SET attempt_count = attempt_count + 1,
			next_attempt_at = NOW() + ($3 * interval '1 second'),
			locked_at = NULL,
			locked_by = '',
			last_error = $4,
			updated_at = NOW()
		WHERE id = $1 AND locked_by = $2`, candidate.id, workerID, int64(delay/time.Second), cause.Error())
	if err != nil {
		return fmt.Errorf("artwork revision GC: schedule retry: %w", err)
	}
	return nil
}

func (s ArtworkRevisionGCStats) JSON() []byte {
	data, _ := json.Marshal(s)
	return data
}
