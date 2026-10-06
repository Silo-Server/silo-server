package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// ArtworkDeliveryStore reads publication manifests and reconciles delivery on
// workers. Scope identifies storage plus the current client-facing URL policy.
type ArtworkDeliveryStore struct {
	pool     *pgxpool.Pool
	scope    string
	external bool
	// scopeSweptAt is when a sweep last found no verdict from another
	// delivery scope left to requeue, in Unix nanoseconds.
	scopeSweptAt atomic.Int64
}

func NewArtworkDeliveryStore(pool *pgxpool.Pool, scope string, external bool) *ArtworkDeliveryStore {
	return &ArtworkDeliveryStore{pool: pool, scope: scope, external: external}
}

func (s *ArtworkDeliveryStore) ArtworkAvailability(ctx context.Context, paths []string) (map[string]ArtworkAvailability, error) {
	// Publication clears delivery_checked_at but keeps the stale verdict's keys.
	rows, err := s.pool.Query(ctx, `SELECT original_path,
        CASE WHEN deleted_at IS NULL THEN published_keys ELSE '{}'::text[] END,
        CASE WHEN deleted_at IS NULL AND delivery_scope = $2 THEN delivery_keys ELSE '{}'::text[] END,
        delivery_scope = $2 AND delivery_checked_at IS NOT NULL
        FROM artwork_revision_gc_candidates
        WHERE original_path = ANY($1)
          AND (published_keys IS NOT NULL OR deleted_at IS NOT NULL
               OR (delivery_scope = $2 AND delivery_checked_at IS NOT NULL))`, paths, s.scope)
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
	Stat(context.Context, string) (blobstore.ObjectInfo, error)
}

type artworkExternalAvailabilityChecker interface {
	ObjectAvailable(context.Context, string) (bool, error)
}

// ArtworkDeliveryStats is one verifier run's durable outcome, saved as the
// task's result data.
type ArtworkDeliveryStats struct {
	// Checked counts claimed revisions; Pending is the subset awaiting their
	// first verdict since publication.
	Checked int `json:"checked"`
	Pending int `json:"pending"`
	// Incomplete counts recorded verdicts with at least one undeliverable
	// variant; Missing counts those variants.
	Incomplete int `json:"incomplete"`
	Missing    int `json:"missing"`
	// Errors counts revisions whose probe failed. They keep their previous
	// verdict and retry on the incomplete-verdict backoff.
	Errors    int    `json:"errors"`
	LastError string `json:"last_error,omitempty"`
	// Superseded counts results discarded because the revision was
	// republished or collected while it was being checked.
	Superseded int `json:"superseded"`
	Repairs    int `json:"repairs"`
	// Rescoped counts verdicts from another delivery configuration that were
	// moved back to pending verification.
	Rescoped int64 `json:"rescoped"`
	// Overdue counts revisions still due when the run ended.
	Overdue int64 `json:"overdue"`
}

func (s ArtworkDeliveryStats) JSON() []byte {
	data, _ := json.Marshal(s)
	return data
}

func (s *ArtworkDeliveryStats) add(other ArtworkDeliveryStats) {
	s.Checked += other.Checked
	s.Pending += other.Pending
	s.Incomplete += other.Incomplete
	s.Missing += other.Missing
	s.Errors += other.Errors
	s.Superseded += other.Superseded
	s.Repairs += other.Repairs
	if s.LastError == "" {
		s.LastError = other.LastError
	}
}

const (
	artworkDeliveryBatchSize    = 100
	artworkDeliveryWorkers      = 12
	artworkDeliveryProbeTimeout = 5 * time.Second
	// A run claims batches for this long. Its probes stop when the lease of
	// the last batch it could claim expires.
	artworkDeliveryClaimWindow = time.Minute
	artworkDeliveryLease       = 2 * time.Minute
	// A complete verdict is rechecked weekly. An incomplete verdict or a probe
	// error retries after 15 minutes, doubling per consecutive failure up to
	// a day. Publication bypasses both through the pending lane.
	artworkDeliveryRecheck   = 7 * 24 * time.Hour
	artworkDeliveryRetryBase = 15 * time.Minute
	artworkDeliveryRetryMax  = 24 * time.Hour
	// Verdicts from another delivery scope are requeued in steps this large.
	// A finished sweep repeats hourly: during a rolling restart, a replica
	// still on the old configuration can record old-scope verdicts after it.
	artworkDeliveryRescopeLimit    = 10000
	artworkDeliveryRescopeInterval = time.Hour
)

// artworkDeliveryRecheckAfter schedules the next check after a verdict with
// the given number of consecutive incomplete verdicts or probe errors.
func artworkDeliveryRecheckAfter(failures int) time.Duration {
	if failures <= 0 {
		return artworkDeliveryRecheck
	}
	wait := artworkDeliveryRetryBase
	for range failures - 1 {
		wait *= 2
		if wait >= artworkDeliveryRetryMax {
			return artworkDeliveryRetryMax
		}
	}
	return wait
}

type artworkDeliveryRevision struct {
	id       int64
	path     string
	keys     []string
	failures int
}

type artworkDeliveryOutcome struct {
	probeErr   error
	missing    int
	superseded bool
	repair     string
}

// Reconcile verifies due revisions in batches until none remain, the claim
// window closes, or every probe in a batch fails. Each batch first claims
// revisions awaiting their first verdict since publication, then fills up
// with routine rechecks, oldest due first. The durable lease is recoverable
// after worker/node failure.
func (s *ArtworkDeliveryStore) Reconcile(ctx context.Context, checker ArtworkDeliveryChecker) (ArtworkDeliveryStats, error) {
	ctx, cancel := context.WithTimeout(ctx, artworkDeliveryLease)
	defer cancel()
	claimUntil := time.Now().Add(artworkDeliveryClaimWindow)
	var stats ArtworkDeliveryStats
	var err error
	if stats.Rescoped, err = s.requeueOtherScopes(ctx); err != nil {
		return stats, err
	}
	for {
		batch, err := s.reconcileBatch(ctx, checker)
		stats.add(batch)
		if err != nil {
			return stats, err
		}
		// A batch where every probe failed suggests a storage or delivery
		// outage; the next run retries instead of spending this one's budget.
		if batch.Checked < artworkDeliveryBatchSize || batch.Errors == batch.Checked || !time.Now().Before(claimUntil) {
			break
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM artwork_revision_gc_candidates
        WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0
          AND delivery_next_check <= NOW()`).Scan(&stats.Overdue); err != nil {
		return stats, fmt.Errorf("count overdue artwork delivery checks: %w", err)
	}
	return stats, nil
}

// requeueOtherScopes moves verdicts recorded under another delivery
// configuration back to the pending lane. The reader already treats them as
// unverified; without this they would wait for their old recheck time.
func (s *ArtworkDeliveryStore) requeueOtherScopes(ctx context.Context) (int64, error) {
	if swept := s.scopeSweptAt.Load(); swept != 0 && time.Since(time.Unix(0, swept)) < artworkDeliveryRescopeInterval {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_checked_at = NULL, delivery_next_check = LEAST(delivery_next_check, NOW())
        WHERE id IN (SELECT id FROM artwork_revision_gc_candidates
            WHERE delivery_checked_at IS NOT NULL AND delivery_scope <> $1 AND deleted_at IS NULL
              AND cardinality(coalesce(published_keys, object_keys)) > 0
            LIMIT $2)`, s.scope, artworkDeliveryRescopeLimit)
	if err != nil {
		return 0, fmt.Errorf("requeue artwork delivery verdicts from another scope: %w", err)
	}
	if tag.RowsAffected() < artworkDeliveryRescopeLimit {
		s.scopeSweptAt.Store(time.Now().UnixNano())
	}
	return tag.RowsAffected(), nil
}

func (s *ArtworkDeliveryStore) reconcileBatch(ctx context.Context, checker ArtworkDeliveryChecker) (ArtworkDeliveryStats, error) {
	lease := uuid.NewString()
	batch, err := s.claim(ctx, lease, artworkDeliveryPendingLane, artworkDeliveryBatchSize)
	if err != nil {
		return ArtworkDeliveryStats{}, err
	}
	stats := ArtworkDeliveryStats{Pending: len(batch)}
	if len(batch) < artworkDeliveryBatchSize {
		routine, err := s.claim(ctx, lease, artworkDeliveryRoutineLane, artworkDeliveryBatchSize-len(batch))
		if err != nil {
			return stats, err
		}
		batch = append(batch, routine...)
	}
	stats.Checked = len(batch)
	outcomes := make([]artworkDeliveryOutcome, len(batch))
	var group errgroup.Group
	group.SetLimit(artworkDeliveryWorkers)
	for i, revision := range batch {
		group.Go(func() error {
			var err error
			outcomes[i], err = s.verifyRevision(ctx, checker, lease, revision)
			return err
		})
	}
	err = group.Wait()
	var repairs []string
	for _, outcome := range outcomes {
		switch {
		case outcome.probeErr != nil:
			stats.Errors++
			if stats.LastError == "" {
				stats.LastError = artworkDeliveryErrorText(outcome.probeErr)
			}
		case outcome.superseded:
			stats.Superseded++
		case outcome.missing > 0:
			stats.Incomplete++
			stats.Missing += outcome.missing
		}
		if outcome.repair != "" {
			repairs = append(repairs, outcome.repair)
		}
	}
	if err == nil && len(repairs) > 0 {
		stats.Repairs, err = NewImageCacheJobRepository(s.pool).EnqueueArtworkRepair(ctx, repairs, artworkDeliveryBatchSize)
	}
	return stats, err
}

// Claim lanes are extra filters on due revisions. Pending revisions have no
// verdict since their latest publication; the routine lane is every due one.
const (
	artworkDeliveryPendingLane = "AND delivery_checked_at IS NULL"
	artworkDeliveryRoutineLane = ""
)

// claim leases up to limit due revisions from one lane, oldest due first.
func (s *ArtworkDeliveryStore) claim(ctx context.Context, lease, lane string, limit int) ([]artworkDeliveryRevision, error) {
	rows, err := s.pool.Query(ctx, `WITH due AS (
        SELECT id FROM artwork_revision_gc_candidates
        WHERE deleted_at IS NULL AND cardinality(coalesce(published_keys, object_keys)) > 0
          AND delivery_next_check <= NOW() `+lane+`
        ORDER BY delivery_next_check, id LIMIT $2 FOR UPDATE SKIP LOCKED
    ) UPDATE artwork_revision_gc_candidates m
      SET delivery_next_check = NOW() + make_interval(secs => $3), delivery_lease = $1
      FROM due WHERE m.id = due.id
      RETURNING m.id, m.original_path, coalesce(m.published_keys, m.object_keys), m.delivery_failures`,
		lease, limit, artworkDeliveryLease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim artwork delivery checks: %w", err)
	}
	defer rows.Close()
	var batch []artworkDeliveryRevision
	for rows.Next() {
		var revision artworkDeliveryRevision
		if err := rows.Scan(&revision.id, &revision.path, &revision.keys, &revision.failures); err != nil {
			return nil, err
		}
		batch = append(batch, revision)
	}
	return batch, rows.Err()
}

func (s *ArtworkDeliveryStore) verifyRevision(ctx context.Context, checker ArtworkDeliveryChecker, lease string, revision artworkDeliveryRevision) (artworkDeliveryOutcome, error) {
	available := make([]string, 0, len(revision.keys))
	var storageMissing bool
	for _, key := range revision.keys {
		probeCtx, cancel := context.WithTimeout(ctx, artworkDeliveryProbeTimeout)
		_, err := checker.Stat(probeCtx, key)
		exists := err == nil
		if errors.Is(err, blobstore.ErrNotFound) {
			err = nil
		}
		if err == nil && exists && s.external {
			if external, ok := checker.(artworkExternalAvailabilityChecker); ok {
				exists, err = external.ObjectAvailable(probeCtx, key)
			}
		} else if err == nil && !exists {
			storageMissing = true
		}
		cancel()
		if err != nil {
			return s.deferRevision(ctx, lease, revision, err)
		}
		if exists {
			available = append(available, key)
		}
	}
	missing := len(revision.keys) - len(available)
	failures := 0
	if missing > 0 {
		failures = revision.failures + 1
	}
	// Publishing/re-uploading invalidates the lease. A stale verifier cannot
	// overwrite a newer publication or resurrect a garbage-collected revision.
	tag, err := s.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_keys = $3, delivery_scope = $4, delivery_checked_at = NOW(),
            published_keys = CASE WHEN published_keys IS NULL AND $6 THEN $5 ELSE published_keys END,
            delivery_failures = $7,
            delivery_next_check = NOW() + make_interval(secs => $8), delivery_lease = ''
        WHERE id = $1 AND delivery_lease = $2 AND coalesce(published_keys, object_keys) = $5 AND deleted_at IS NULL`,
		revision.id, lease, available, s.scope, revision.keys, !storageMissing,
		failures, artworkDeliveryRecheckAfter(failures).Seconds())
	if err != nil {
		return artworkDeliveryOutcome{}, fmt.Errorf("record artwork delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return artworkDeliveryOutcome{superseded: true}, nil
	}
	outcome := artworkDeliveryOutcome{missing: missing}
	if storageMissing {
		outcome.repair = revision.path
	}
	return outcome, nil
}

// deferRevision keeps the last completed verdict after a transport/auth error
// and backs the revision off, so a probe that keeps failing cannot return on
// every lease. No catalog pointers are cleared.
func (s *ArtworkDeliveryStore) deferRevision(ctx context.Context, lease string, revision artworkDeliveryRevision, probeErr error) (artworkDeliveryOutcome, error) {
	outcome := artworkDeliveryOutcome{probeErr: probeErr}
	if ctx.Err() != nil {
		// The run ran out of time; the lease expiry retries the revision.
		return outcome, nil //nolint:nilerr // A timed-out run is not a failure; the probe error is counted in the outcome.
	}
	failures := revision.failures + 1
	if _, err := s.pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates
        SET delivery_failures = $3, delivery_next_check = NOW() + make_interval(secs => $4), delivery_lease = ''
        WHERE id = $1 AND delivery_lease = $2`,
		revision.id, lease, failures, artworkDeliveryRecheckAfter(failures).Seconds()); err != nil {
		return outcome, fmt.Errorf("defer artwork delivery check: %w", err)
	}
	return outcome, nil
}

var artworkDeliveryURLQuery = regexp.MustCompile(`(https?://[^\s"?]+)\?[^\s"]*`)

// artworkDeliveryErrorText prepares a probe error for task history, which is
// kept long after logs rotate. URL query strings can carry presigned or token
// credentials, so they are dropped, and the text is capped.
func artworkDeliveryErrorText(err error) string {
	const limit = 500
	text := artworkDeliveryURLQuery.ReplaceAllString(err.Error(), "$1?[redacted]")
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "")
	}
	return text
}
