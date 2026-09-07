package metadata

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// ArtworkDeliveryStore reads publication manifests and reconciles delivery on
// workers. Scope identifies storage plus the current client-facing URL policy.
type ArtworkDeliveryStore struct {
	pool     *pgxpool.Pool
	scope    string
	external bool
}

func NewArtworkDeliveryStore(pool *pgxpool.Pool, scope string, external bool) *ArtworkDeliveryStore {
	return &ArtworkDeliveryStore{pool: pool, scope: scope, external: external}
}

func (s *ArtworkDeliveryStore) ArtworkAvailability(ctx context.Context, paths []string) (map[string]ArtworkAvailability, error) {
	rows, err := s.pool.Query(ctx, `SELECT original_path,
        CASE WHEN deleted_at IS NULL THEN published_keys ELSE '{}'::text[] END,
        CASE WHEN deleted_at IS NULL THEN delivery_keys ELSE '{}'::text[] END,
        delivery_scope = $2 AND delivery_checked_at IS NOT NULL
        FROM artwork_revision_gc_candidates
        WHERE original_path = ANY($1)
          AND (published_keys IS NOT NULL OR deleted_at IS NOT NULL)`, paths, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]ArtworkAvailability)
	for rows.Next() {
		var path string
		state := ArtworkAvailability{External: s.external}
		if err := rows.Scan(&path, &state.Published, &state.Deliverable, &state.Verified); err != nil {
			return nil, err
		}
		// For direct S3 delivery a completed check can also detect deletions.
		if state.Verified {
			state.External = true
		}
		states[path] = state
	}
	return states, rows.Err()
}

type ArtworkDeliveryChecker interface {
	ObjectExists(context.Context, string, string) (bool, error)
	ObjectAvailable(context.Context, string, string) (bool, error)
	Bucket() string
}

type ArtworkDeliveryStats struct {
	Checked int `json:"checked"`
	Missing int `json:"missing"`
	Errors  int `json:"errors"`
}

type artworkDeliveryRevision struct {
	id   int64
	path string
	keys []string
}

// Reconcile checks at most 100 revisions, with 12 workers and a one-minute
// runtime bound. The durable lease is recoverable after worker/node failure.
func (s *ArtworkDeliveryStore) Reconcile(ctx context.Context, checker ArtworkDeliveryChecker) (ArtworkDeliveryStats, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	lease := uuid.NewString()
	rows, err := s.pool.Query(ctx, `WITH due AS (
        SELECT id FROM artwork_revision_gc_candidates
        WHERE deleted_at IS NULL AND cardinality(published_keys) > 0
          AND delivery_next_check <= NOW()
        ORDER BY delivery_next_check, id LIMIT 100 FOR UPDATE SKIP LOCKED
    ) UPDATE artwork_revision_gc_candidates m
      SET delivery_next_check = NOW() + INTERVAL '2 minutes', delivery_lease = $1
      FROM due WHERE m.id = due.id
      RETURNING m.id, m.original_path, m.published_keys`, lease)
	if err != nil {
		return ArtworkDeliveryStats{}, err
	}
	var batch []artworkDeliveryRevision
	for rows.Next() {
		var revision artworkDeliveryRevision
		if err := rows.Scan(&revision.id, &revision.path, &revision.keys); err != nil {
			rows.Close()
			return ArtworkDeliveryStats{}, err
		}
		batch = append(batch, revision)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ArtworkDeliveryStats{}, err
	}
	results := make([]ArtworkDeliveryStats, len(batch))
	repairs := make([]string, len(batch))
	var group errgroup.Group
	group.SetLimit(12)
	for i, revision := range batch {
		group.Go(func() error {
			var err error
			results[i], repairs[i], err = s.verifyRevision(ctx, checker, lease, revision)
			return err
		})
	}
	err = group.Wait()
	var stats ArtworkDeliveryStats
	for _, result := range results {
		stats.Checked += result.Checked
		stats.Missing += result.Missing
		stats.Errors += result.Errors
	}
	if err == nil {
		paths := make([]string, 0, len(repairs))
		for _, path := range repairs {
			if path != "" {
				paths = append(paths, path)
			}
		}
		if len(paths) > 0 {
			err = s.repairMissingRevisions(ctx, paths)
		}
	}
	return stats, err
}

func (s *ArtworkDeliveryStore) verifyRevision(ctx context.Context, checker ArtworkDeliveryChecker, lease string, revision artworkDeliveryRevision) (ArtworkDeliveryStats, string, error) {
	stats := ArtworkDeliveryStats{Checked: 1}
	available := make([]string, 0, len(revision.keys))
	var storageMissing bool
	for _, key := range revision.keys {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		exists, err := checker.ObjectExists(probeCtx, checker.Bucket(), key)
		if err == nil && exists && s.external {
			exists, err = checker.ObjectAvailable(probeCtx, checker.Bucket(), key)
		} else if err == nil && !exists {
			storageMissing = true
		}
		cancel()
		if err != nil {
			// Preserve the last completed verdict on transport/auth errors.
			// The lease expires soon; no catalog pointers are cleared.
			stats.Errors++
			return stats, "", nil //nolint:nilerr // Probe failures are counted and retried after the lease; they do not abort other revisions.
		}
		if exists {
			available = append(available, key)
		} else {
			stats.Missing++
		}
	}
	// Publishing/re-uploading invalidates the lease. A stale verifier cannot
	// overwrite a newer publication or resurrect a garbage-collected revision.
	tag, err := s.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_keys = $3, delivery_scope = $4, delivery_checked_at = NOW(),
            delivery_next_check = NOW() + INTERVAL '15 minutes', delivery_lease = ''
        WHERE id = $1 AND delivery_lease = $2 AND published_keys = $5 AND deleted_at IS NULL`,
		revision.id, lease, available, s.scope, revision.keys)
	if err != nil {
		return stats, "", fmt.Errorf("record artwork delivery: %w", err)
	}
	if tag.RowsAffected() > 0 && storageMissing {
		return stats, revision.path, nil
	}
	return stats, "", nil
}

func (s *ArtworkDeliveryStore) repairMissingRevisions(ctx context.Context, paths []string) error {
	_, err := NewImageCacheJobRepository(s.pool).EnqueueArtworkRepair(ctx, paths, 100)
	return err
}
