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
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/pathscope"
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

// ArtworkRevisionDeleter is the artwork-store surface used by revision GC. It
// is the store's own batch-delete contract: logical keys only, no bucket, so GC
// behaves identically on every backend. Already-absent keys count as deleted on
// both, which is what makes the strict count check below meaningful.
type ArtworkRevisionDeleter interface {
	DeleteObjects(ctx context.Context, keys []string) (int, error)
	ListPage(ctx context.Context, prefix, cursor string, limit int) ([]artworkstore.ObjectInfo, string, bool, error)
	DeletePrefixMaintenance(ctx context.Context, prefix string) (int, error)
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
	LegacyPrefixes  int `json:"legacy_prefixes"`
}

// ArtworkRevisionGarbageCollector deletes unpublished or displaced immutable
// revisions only after their grace period and while no catalog surface
// references them. Work is leased with SKIP LOCKED so multiple workers are safe.
type ArtworkRevisionGarbageCollector struct {
	pool  *pgxpool.Pool
	store ArtworkRevisionDeleter
}

func NewArtworkRevisionGarbageCollector(pool *pgxpool.Pool, store ArtworkRevisionDeleter) *ArtworkRevisionGarbageCollector {
	if pool == nil || store == nil {
		return nil
	}
	return &ArtworkRevisionGarbageCollector{pool: pool, store: store}
}

// Run processes one bounded batch. Failed deletions are retried with
// exponential backoff; an expired lease is recoverable by another worker.
func (g *ArtworkRevisionGarbageCollector) Run(ctx context.Context) (ArtworkRevisionGCStats, error) {
	stats := ArtworkRevisionGCStats{}
	if g == nil || g.pool == nil || g.store == nil {
		return stats, fmt.Errorf("artwork revision GC is not configured")
	}
	if health, ok := g.store.(interface {
		Health() (artworkstore.HealthState, time.Time)
	}); ok {
		state, _ := health.Health()
		if state == artworkstore.HealthUnavailable || state == artworkstore.HealthWrongMount {
			return stats, nil
		}
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
	legacy, legacyErr := g.sweepLegacyPrefixes(ctx, 20)
	stats.LegacyPrefixes = legacy
	if err == nil {
		err = legacyErr
	}
	return stats, err
}

func (g *ArtworkRevisionGarbageCollector) sweepLegacyPrefixes(ctx context.Context, limit int) (int, error) {
	rows, err := g.pool.Query(ctx, `SELECT prefix FROM artwork_legacy_prefix_gc_candidates WHERE not_before <= NOW() ORDER BY not_before LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return 0, err
		}
		prefixes = append(prefixes, prefix)
	}
	rows.Close()
	completed := 0
	for _, prefix := range prefixes {
		var referenced bool
		if err := g.pool.QueryRow(ctx, artworkLegacyPrefixReferencedSQL(), artworkLegacyPrefixPattern(prefix)).Scan(&referenced); err != nil {
			return completed, err
		}
		if referenced {
			_, err = g.pool.Exec(ctx, `UPDATE artwork_legacy_prefix_gc_candidates SET not_before = NOW() + interval '24 hours', last_error = '', updated_at = NOW() WHERE prefix = $1`, prefix)
			if err != nil {
				return completed, err
			}
			continue
		}
		_, err = g.store.DeletePrefixMaintenance(ctx, prefix)
		if err != nil {
			_, _ = g.pool.Exec(ctx, `UPDATE artwork_legacy_prefix_gc_candidates SET attempt_count = attempt_count + 1, last_error = $2, not_before = NOW() + interval '1 hour', updated_at = NOW() WHERE prefix = $1`, prefix, err.Error())
			continue
		}
		_, err = g.pool.Exec(ctx, `DELETE FROM artwork_legacy_prefix_gc_candidates WHERE prefix = $1`, prefix)
		if err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func artworkLegacyPrefixReferencedSQL() string {
	return `WITH refs AS (` + artworkInventoryReferenceSQL() + `) SELECT EXISTS (SELECT 1 FROM refs WHERE path LIKE $1 ESCAPE '\')`
}

func artworkLegacyPrefixPattern(prefix string) string {
	return pathscope.EscapeLike(prefix) + "%"
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
			  AND NOT (seed_imported_at IS NOT NULL AND seed_expires_at IS NULL)
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
			last_reference_check_at = NOW(),
			deletion_started_at = NULL,
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
					last_reference_check_at = NOW(),
					deletion_started_at = NULL,
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
		if _, err := tx.Exec(ctx, `
			UPDATE artwork_revision_gc_candidates
			SET last_reference_check_at = NOW(), deletion_started_at = COALESCE(deletion_started_at, NOW()), updated_at = NOW()
			WHERE id = $1 AND locked_by = $2`, candidate.id, workerID); err != nil {
			return artworkRevisionGCSuperseded, fmt.Errorf("artwork revision GC: mark deleting: %w", err)
		}
	}

	// Rows queued by the displacement triggers carry no manifest; expand the
	// expected object set from the image type so the variant ladder stays
	// defined once, in artworkkey.
	if len(objectKeys) == 0 {
		objectKeys = artworkkey.ObjectKeys(originalPath, imageType)
	}
	if len(objectKeys) > 0 {
		deleted, err := g.store.DeleteObjects(ctx, objectKeys)
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
		UPDATE artwork_revision_gc_candidates
		SET tombstoned_at = NOW(),
			next_attempt_at = NULL,
			missing_at = NULL,
			repair_state = '',
			repair_queued_at = NULL,
			protected_loss_at = NULL,
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NOT NULL AND tombstoned_at IS NULL`, candidate.id); err != nil {
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

// artworkNoRemoteSource is the remote-source predicate for a surface whose
// artwork cannot be re-downloaded: nothing matches it, so such a row is always
// cleared rather than repointed.
const artworkNoRemoteSource = "FALSE"

// artworkReferenceSurface is one column that can hold a live reference to a
// stored artwork object.
//
// It is a superset of the reconciler's sweep surfaces: a column can be worth
// protecting from deletion without being worth sweeping. Profile avatars are
// the case in point — their column holds a prefixed reference rather than a
// bare key, so the reconciler cannot verify it row by row, but the collector
// must still see it or it would delete avatars that are plainly in use.
//
// Every surface listed here is checked before an object is deleted, so an
// omission is a data-loss bug, not a leak. The union can only see catalog
// Postgres tables: on installs with userdb.backend=sqlite, profile and
// personal-collection rows live in per-user database files instead, which is
// why the upload handlers only Track user-owned revisions when the user store
// is Postgres-backed (see api.Dependencies.UserArtworkTracked).
type artworkReferenceSurface struct {
	name  string
	table string
	// pathExpr is a SQL expression over the row yielding the object key.
	pathExpr string
	// filter restricts the surface to rows that actually hold a key. Empty
	// means every row qualifies.
	filter string
	// resetSet repoints a row at its re-downloadable source. Empty when the
	// surface has none.
	resetSet string
	// remoteSource selects rows resetSet applies to, or "FALSE".
	remoteSource string
	// clearSet blanks a row that cannot be reset, so its owning pipeline
	// refills it.
	clearSet string
}

// matchPredicate selects the rows of this surface referencing $1.
func (s artworkReferenceSurface) matchPredicate() string {
	predicate := s.pathExpr + " = $1"
	if s.filter != "" {
		predicate += " AND " + s.filter
	}
	return predicate
}

// artworkReferenceSurfaces lists every column the collector treats as a live
// reference.
func artworkReferenceSurfaces() []artworkReferenceSurface {
	sweeps := artworkSweepSurfaces()
	surfaces := make([]artworkReferenceSurface, 0, len(sweeps)+1)
	for _, sweep := range sweeps {
		surface := artworkReferenceSurface{
			name:         sweep.name,
			table:        sweep.table,
			pathExpr:     sweep.pathCol,
			remoteSource: sweep.remoteSourcePredicate(),
			clearSet:     sweep.clearSet,
		}
		if sweep.sourceCol != "" {
			surface.resetSet = sweep.resetSet()
		}
		surfaces = append(surfaces, surface)
	}
	return append(surfaces, profileAvatarReferenceSurface())
}

// profileAvatarUploadPrefix marks a profile avatar that is an uploaded object
// rather than a bundled preset or a generated remote URL. It mirrors the
// constant the profile handlers write; the two must not drift, which is what
// the accompanying test pins.
const profileAvatarUploadPrefix = "upload:"

// profileAvatarReferenceSurface protects uploaded profile avatars from
// collection. user_profiles.avatar stores "upload:<object key>", so the key is
// recovered by dropping the prefix; presets ("preset:<id>") and DiceBear URLs
// are excluded by the filter rather than by hoping they never collide with a
// key.
func profileAvatarReferenceSurface() artworkReferenceSurface {
	return artworkReferenceSurface{
		name:         "profile avatars",
		table:        "user_profiles",
		pathExpr:     fmt.Sprintf("substr(avatar, %d)", len(profileAvatarUploadPrefix)+1),
		filter:       fmt.Sprintf("avatar LIKE '%s%%'", profileAvatarUploadPrefix),
		remoteSource: artworkNoRemoteSource,
		clearSet:     "avatar = '', updated_at = NOW()",
	}
}

func artworkReferenceUnionSQL() string {
	return artworkReferenceUnionMatchingSQL(func(pathExpr string) string {
		return pathExpr + " = ANY($1)"
	})
}

func artworkReferenceUnionMatchingSQL(match func(pathExpr string) string) string {
	surfaces := artworkReferenceSurfaces()
	parts := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		part := "SELECT " + surface.pathExpr + " AS path FROM " + surface.table +
			" WHERE " + match(surface.pathExpr)
		if surface.filter != "" {
			part += " AND " + surface.filter
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " UNION ALL ")
}

// artworkLossReferenceUnionSQL retains the indexed candidate predicate used
// by GC while sourcing candidates from the rebuild's loss_paths CTE. This
// avoids repeatedly projecting every catalog artwork row while rebuilding a
// large store.
func artworkLossReferenceUnionSQL() string {
	return artworkReferenceUnionMatchingSQL(func(pathExpr string) string {
		return pathExpr + " IN (SELECT original_path FROM loss_paths)"
	})
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
		  AND tombstoned_at IS NULL
		  AND NOT (seed_imported_at IS NOT NULL AND seed_expires_at IS NULL)
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
				last_reference_check_at = NOW(),
				updated_at = NOW()
			WHERE id = ANY($1) AND next_attempt_at IS NULL`, requeue); err != nil {
			return checked, 0, fmt.Errorf("artwork revision GC: requeue dormant revisions: %w", err)
		}
		requeued = len(requeue)
	}
	if len(touch) > 0 {
		if _, err := g.pool.Exec(ctx, `
			UPDATE artwork_revision_gc_candidates
			SET last_reference_check_at = NOW(), updated_at = NOW()
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
	for _, surface := range artworkReferenceSurfaces() {
		if surface.resetSet != "" {
			resetSQL := fmt.Sprintf(`UPDATE %s SET %s WHERE %s AND %s`,
				surface.table, surface.resetSet, surface.matchPredicate(), surface.remoteSource)
			tag, err := g.pool.Exec(ctx, resetSQL, originalPath)
			if err != nil {
				return healed, fmt.Errorf("artwork revision GC: heal %s: %w", surface.name, err)
			}
			healed = healed || tag.RowsAffected() > 0
		}
		clearSQL := fmt.Sprintf(`UPDATE %s SET %s WHERE %s AND NOT (%s)`,
			surface.table, surface.clearSet, surface.matchPredicate(), surface.remoteSource)
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
