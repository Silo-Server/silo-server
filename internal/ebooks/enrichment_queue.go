package ebooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const (
	defaultEnrichmentLease   = 10 * time.Minute
	maxEnrichmentRetry       = 24 * time.Hour
	transientRetryBase       = 5 * time.Minute
	skippedRetryHorizon      = 15 * time.Minute
	claimCandidateWindow     = maxEnrichWorkers
	defaultRateLimitCooldown = 15 * time.Minute
	rateLimitCooldownEnv     = "SILO_EBOOK_RATE_LIMIT_COOLDOWN"
)

func rateLimitCooldownFloor() time.Duration {
	if v := os.Getenv(rateLimitCooldownEnv); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultRateLimitCooldown
}

type EnrichmentOutcome string

const (
	EnrichmentOutcomeSuccess EnrichmentOutcome = "success"
	EnrichmentOutcomeNoMatch EnrichmentOutcome = "no_match"
	EnrichmentOutcomeSkipped EnrichmentOutcome = "skipped"
)

type EnrichmentErrorClass string

const (
	EnrichmentErrorTransient   EnrichmentErrorClass = "transient"
	EnrichmentErrorRateLimited EnrichmentErrorClass = "rate_limited"
	EnrichmentErrorPermanent   EnrichmentErrorClass = "permanent"
)

var ErrEnrichmentLeaseLost = errors.New("ebook enrichment lease lost")

type EnrichmentScope string

const (
	EnrichmentScopeIncremental EnrichmentScope = "incremental"
	EnrichmentScopeLegacy      EnrichmentScope = "legacy"
)

func (s EnrichmentScope) validate() error {
	switch s {
	case EnrichmentScopeIncremental, EnrichmentScopeLegacy:
		return nil
	default:
		return fmt.Errorf("unsupported ebook enrichment scope %q", s)
	}
}

type EnrichmentJob struct {
	ContentID       string
	Token           string
	Attempts        int
	LastAttemptAt   time.Time
	ProtectedFields []string
}

type EnrichmentQueue struct {
	pool *pgxpool.Pool
}

func NewEnrichmentQueue(pool *pgxpool.Pool) *EnrichmentQueue {
	return &EnrichmentQueue{pool: pool}
}

const ebookProtectedFieldsSQL = `
	ARRAY_REMOVE(ARRAY[
		CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.title, '')) <> '' THEN 'title' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.year, 0) > 0 THEN 'year' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.overview, '')) <> '' THEN 'overview' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.tagline, '')) <> '' THEN 'tagline' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.content_rating, '')) <> '' THEN 'content_rating' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND COALESCE(mi.runtime, 0) > 0 THEN 'runtime' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND trim(COALESCE(mi.release_date::text, '')) <> '' THEN 'release_date' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.genres, '{}'::text[])) > 0 THEN 'genres' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND cardinality(COALESCE(mi.studios, '{}'::text[])) > 0 THEN 'studios' END,
		CASE WHEN lower(trim(mi.status)) = 'pending' AND EXISTS (
			SELECT 1 FROM item_people ip WHERE ip.content_id = mi.content_id AND ip.kind = 7
		) THEN 'authors' END,
		CASE WHEN trim(COALESCE(mi.poster_path, '')) <> ''
			AND lower(trim(mi.poster_path)) NOT LIKE 'http://%'
			AND lower(trim(mi.poster_path)) NOT LIKE 'https://%'
			AND lower(trim(mi.poster_path)) NOT LIKE 'ebook-metadata/ebooks/%'
			THEN 'poster_path' END,
		CASE WHEN trim(COALESCE(mi.backdrop_path, '')) <> ''
			AND lower(trim(mi.backdrop_path)) NOT LIKE 'http://%'
			AND lower(trim(mi.backdrop_path)) NOT LIKE 'https://%'
			AND lower(trim(mi.backdrop_path)) NOT LIKE 'ebook-metadata/ebooks/%'
			THEN 'backdrop_path' END,
		CASE WHEN trim(COALESCE(mi.logo_path, '')) <> ''
			AND lower(trim(mi.logo_path)) NOT LIKE 'http://%'
			AND lower(trim(mi.logo_path)) NOT LIKE 'https://%'
			AND lower(trim(mi.logo_path)) NOT LIKE 'ebook-metadata/ebooks/%'
			THEN 'logo_path' END
	]::text[], NULL)
`

const mergeEbookProtectedFieldsSQL = `
	ARRAY(
		SELECT DISTINCT field
		FROM unnest(
			ebook_enrichment_state.protected_fields ||
			ARRAY(
				SELECT candidate
				FROM unnest(EXCLUDED.protected_fields) AS candidate
				WHERE candidate IN ('poster_path', 'backdrop_path', 'logo_path')
			)
		) AS field
		ORDER BY field
	)
`

// Legacy-lane rows (priority < 0) may only leave the lane through a terminal
// outcome (Complete/Discard); enqueue, fail, and release must preserve the
// lane so the paced backfill task stays the sole drain for the backlog.
var enqueueEnrichmentJobQuery = `
	INSERT INTO ebook_enrichment_state (
		content_id, status, priority, next_attempt_at, protected_fields, updated_at
	)
	SELECT mi.content_id, 'pending', $2, now(), ` + ebookProtectedFieldsSQL + `, now()
	FROM media_items mi
	WHERE mi.content_id = $1
	  AND mi.type = 'ebook'
	  AND ` + catalog.MangaChapterExclusionWhere("mi") + `
	ON CONFLICT (content_id) DO UPDATE SET
		status = CASE
			WHEN ebook_enrichment_state.status = 'running' THEN ebook_enrichment_state.status
			ELSE 'pending'
		END,
		priority = CASE
			WHEN ebook_enrichment_state.status = 'running' THEN ebook_enrichment_state.priority
			WHEN ebook_enrichment_state.priority < 0 THEN ebook_enrichment_state.priority
			ELSE GREATEST(ebook_enrichment_state.priority, EXCLUDED.priority)
		END,
		next_attempt_at = CASE
			WHEN ebook_enrichment_state.status = 'running' THEN ebook_enrichment_state.next_attempt_at
			ELSE now()
		END,
		requeue_requested = ebook_enrichment_state.requeue_requested OR ebook_enrichment_state.status = 'running',
		protected_fields = ` + mergeEbookProtectedFieldsSQL + `,
		updated_at = now()
`

func (q *EnrichmentQueue) Enqueue(ctx context.Context, contentID string, priority int) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if strings.TrimSpace(contentID) == "" {
		return errors.New("ebook enrichment content id is required")
	}
	_, err := q.pool.Exec(ctx, enqueueEnrichmentJobQuery, contentID, priority)
	return err
}

const ensureEnrichmentReconcileCursorQuery = `
	INSERT INTO ebook_enrichment_reconcile_cursors (
		folder_id, after_first_seen_at, after_content_id, updated_at
	)
	VALUES ($1, NULL, NULL, now())
	ON CONFLICT (folder_id) DO NOTHING
`

const lockEnrichmentReconcileCursorQuery = `
	SELECT after_first_seen_at, after_content_id
	FROM ebook_enrichment_reconcile_cursors
	WHERE folder_id = $1
	FOR UPDATE
`

var reconcileRecentMissingEnrichmentJobsQuery = `
	WITH recent_memberships AS MATERIALIZED (
		SELECT membership.content_id
		FROM media_item_libraries membership
		WHERE membership.media_folder_id = $1
		ORDER BY membership.first_seen_at DESC, membership.content_id DESC
		LIMIT $3
	),
	candidates AS MATERIALIZED (
		SELECT candidate.content_id, ` + ebookProtectedFieldsSQL + ` AS protected_fields
		FROM recent_memberships candidate
		JOIN media_items mi ON mi.content_id = candidate.content_id
		LEFT JOIN ebook_enrichment_state state ON state.content_id = candidate.content_id
		WHERE mi.type = 'ebook'
		  AND ` + catalog.MangaChapterExclusionWhere("mi") + `
		  AND state.content_id IS NULL
	),
	inserted AS (
		INSERT INTO ebook_enrichment_state (
			content_id, status, priority, next_attempt_at, protected_fields, updated_at
		)
		SELECT candidates.content_id, 'pending', $2, now(), candidates.protected_fields, now()
		FROM candidates
		ON CONFLICT (content_id) DO NOTHING
		RETURNING content_id
	)
	SELECT
		(SELECT COUNT(*)::integer FROM inserted) AS reconciled,
		(SELECT COUNT(*)::integer FROM recent_memberships) AS inspected
`

var reconcileMissingEnrichmentJobsQuery = `
	WITH membership_candidates AS MATERIALIZED (
		SELECT membership.content_id, membership.first_seen_at
		FROM media_item_libraries membership
		WHERE membership.media_folder_id = $1
		  AND (
			$4::timestamptz IS NULL
			OR (membership.first_seen_at, membership.content_id) < ($4, $5)
		  )
		ORDER BY membership.first_seen_at DESC, membership.content_id DESC
		LIMIT $3
	),
	candidates AS MATERIALIZED (
		SELECT candidate.content_id, ` + ebookProtectedFieldsSQL + ` AS protected_fields
		FROM membership_candidates candidate
		JOIN media_items mi ON mi.content_id = candidate.content_id
		LEFT JOIN ebook_enrichment_state state ON state.content_id = candidate.content_id
		WHERE mi.type = 'ebook'
		  AND ` + catalog.MangaChapterExclusionWhere("mi") + `
		  AND state.content_id IS NULL
	),
	inserted AS (
		INSERT INTO ebook_enrichment_state (
			content_id, status, priority, next_attempt_at, protected_fields, updated_at
		)
		SELECT candidates.content_id, 'pending', $2, now(), candidates.protected_fields, now()
		FROM candidates
		ON CONFLICT (content_id) DO NOTHING
		RETURNING content_id
	),
	window_stats AS MATERIALIZED (
		SELECT
			COUNT(*)::integer AS inspected,
			(
				SELECT first_seen_at
				FROM membership_candidates
				ORDER BY first_seen_at, content_id
				LIMIT 1
			) AS last_first_seen_at,
			(
				SELECT content_id
				FROM membership_candidates
				ORDER BY first_seen_at, content_id
				LIMIT 1
		) AS last_content_id
		FROM membership_candidates
	)
	SELECT
		(SELECT COUNT(*)::integer FROM inserted) AS reconciled,
		window_stats.inspected,
		window_stats.last_first_seen_at,
		window_stats.last_content_id
	FROM window_stats
`

const updateEnrichmentReconcileCursorQuery = `
	UPDATE ebook_enrichment_reconcile_cursors
	SET after_first_seen_at = $2,
		after_content_id = $3,
		updated_at = now()
	WHERE folder_id = $1
`

// ReconcileMissing advances a database-persisted keyset cursor while holding
// its folder row lock. Queue repairs and cursor movement commit atomically.
// A server restart resumes from the persisted cursor; completed repairs remain
// durable, and reaching the end resets the cursor so later passes wrap safely.
func (q *EnrichmentQueue) ReconcileMissing(
	ctx context.Context,
	folderID, priority, limit int,
) (reconciled, inspected int, wrapped bool, err error) {
	if q == nil || q.pool == nil {
		return 0, 0, false, errors.New("ebook enrichment queue is not configured")
	}
	if folderID <= 0 {
		return 0, 0, false, errors.New("ebook enrichment folder id is required")
	}
	if limit <= 0 {
		return 0, 0, false, nil
	}

	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err = tx.Exec(ctx, ensureEnrichmentReconcileCursorQuery, folderID); err != nil {
		return 0, 0, false, err
	}
	var afterFirstSeenAt *time.Time
	var afterContentID *string
	if err = tx.QueryRow(
		ctx,
		lockEnrichmentReconcileCursorQuery,
		folderID,
	).Scan(&afterFirstSeenAt, &afterContentID); err != nil {
		return 0, 0, false, err
	}

	var recentReconciled, recentInspected int
	if err = tx.QueryRow(
		ctx,
		reconcileRecentMissingEnrichmentJobsQuery,
		folderID,
		priority,
		limit,
	).Scan(&recentReconciled, &recentInspected); err != nil {
		return 0, 0, false, err
	}

	var lastFirstSeenAt *time.Time
	var lastContentID *string
	err = tx.QueryRow(
		ctx,
		reconcileMissingEnrichmentJobsQuery,
		folderID,
		priority,
		limit,
		afterFirstSeenAt,
		afterContentID,
	).Scan(&reconciled, &inspected, &lastFirstSeenAt, &lastContentID)
	if err != nil {
		return 0, 0, false, err
	}
	wrapped = inspected < limit
	if wrapped {
		lastFirstSeenAt = nil
		lastContentID = nil
	}
	if _, err = tx.Exec(
		ctx,
		updateEnrichmentReconcileCursorQuery,
		folderID,
		lastFirstSeenAt,
		lastContentID,
	); err != nil {
		return 0, 0, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, false, err
	}
	reconciled += recentReconciled
	inspected += recentInspected
	return reconciled, inspected, wrapped, nil
}

// Each indexed window is fixed-size; only their deduplicated union pays for
// aging/ranking, while the oldest-due window guarantees eventual promotion.
const claimEnrichmentJobsQueryTemplate = `
	WITH high_priority_candidates AS MATERIALIZED (
		SELECT content_id, priority, next_attempt_at, updated_at
		FROM ebook_enrichment_state
		WHERE next_attempt_at <= now()
		  AND (status = 'pending' OR (status = 'running' AND lease_until < now()))
		  AND {{lane_predicate}}
		ORDER BY priority DESC, next_attempt_at, updated_at
		LIMIT $3
	),
	oldest_due_candidates AS MATERIALIZED (
		SELECT content_id, priority, next_attempt_at, updated_at
		FROM ebook_enrichment_state
		WHERE next_attempt_at <= now()
		  AND (status = 'pending' OR (status = 'running' AND lease_until < now()))
		  AND {{lane_predicate}}
		ORDER BY next_attempt_at, updated_at, priority DESC
		LIMIT $3
	),
	candidate_pool AS MATERIALIZED (
		SELECT * FROM high_priority_candidates
		UNION
		SELECT * FROM oldest_due_candidates
	),
	ranked_candidates AS MATERIALIZED (
		SELECT
			content_id,
			priority,
			next_attempt_at,
			updated_at,
			(priority + FLOOR(EXTRACT(EPOCH FROM (now() - next_attempt_at)) / 3600)::integer) AS effective_priority
		FROM candidate_pool
	),
	candidates AS (
		SELECT state.content_id
		FROM ranked_candidates ranked
		JOIN ebook_enrichment_state state ON state.content_id = ranked.content_id
		WHERE state.next_attempt_at <= now()
		  AND (state.status = 'pending' OR (state.status = 'running' AND state.lease_until < now()))
		  AND {{state_lane_predicate}}
		ORDER BY
			ranked.effective_priority DESC,
			ranked.priority DESC,
			ranked.next_attempt_at,
			ranked.updated_at
		FOR UPDATE OF state SKIP LOCKED
		LIMIT $1
	)
	UPDATE ebook_enrichment_state state
	SET status = 'running',
		lease_until = now() + $2::interval,
		claim_token = gen_random_uuid()::text,
		last_attempt_at = now(),
		attempts = attempts + 1,
		updated_at = now()
	FROM candidates
	WHERE state.content_id = candidates.content_id
	RETURNING state.content_id, state.claim_token, state.attempts, state.last_attempt_at, state.protected_fields
`

var (
	claimIncrementalEnrichmentJobsQuery = buildClaimEnrichmentJobsQuery("priority >= 0", "state.priority >= 0")
	claimLegacyEnrichmentJobsQuery      = buildClaimEnrichmentJobsQuery("priority < 0", "state.priority < 0")
)

func buildClaimEnrichmentJobsQuery(lanePredicate, stateLanePredicate string) string {
	query := strings.ReplaceAll(claimEnrichmentJobsQueryTemplate, "{{lane_predicate}}", lanePredicate)
	return strings.ReplaceAll(query, "{{state_lane_predicate}}", stateLanePredicate)
}

func claimEnrichmentJobsQueryForScope(scope EnrichmentScope) string {
	if scope == EnrichmentScopeLegacy {
		return claimLegacyEnrichmentJobsQuery
	}
	return claimIncrementalEnrichmentJobsQuery
}

func (q *EnrichmentQueue) ClaimBatch(
	ctx context.Context,
	scope EnrichmentScope,
	limit int,
	leaseDuration time.Duration,
) ([]EnrichmentJob, error) {
	if q == nil || q.pool == nil {
		return nil, errors.New("ebook enrichment queue is not configured")
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	if leaseDuration <= 0 {
		leaseDuration = defaultEnrichmentLease
	}

	query := claimEnrichmentJobsQueryForScope(scope)
	rows, err := q.pool.Query(
		ctx,
		query,
		limit,
		postgresInterval(leaseDuration),
		claimCandidateWindow,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := make([]EnrichmentJob, 0, limit)
	for rows.Next() {
		var job EnrichmentJob
		if err := rows.Scan(&job.ContentID, &job.Token, &job.Attempts, &job.LastAttemptAt, &job.ProtectedFields); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

const countReadyEnrichmentJobsQueryTemplate = `
	SELECT COUNT(*)
	FROM ebook_enrichment_state
	WHERE next_attempt_at <= now()
	  AND (status = 'pending' OR (status = 'running' AND lease_until < now()))
	  AND {{lane_predicate}}
`

var (
	countReadyIncrementalEnrichmentJobsQuery = strings.ReplaceAll(
		countReadyEnrichmentJobsQueryTemplate,
		"{{lane_predicate}}",
		"priority >= 0",
	)
	countReadyLegacyEnrichmentJobsQuery = strings.ReplaceAll(
		countReadyEnrichmentJobsQueryTemplate,
		"{{lane_predicate}}",
		"priority < 0",
	)
)

const hasReadyEnrichmentJobsQueryTemplate = `
	SELECT COALESCE((
		SELECT true
		FROM ebook_enrichment_state
		WHERE next_attempt_at <= now()
		  AND (status = 'pending' OR (status = 'running' AND lease_until < now()))
		  AND {{lane_predicate}}
		ORDER BY next_attempt_at, updated_at, priority DESC
		LIMIT 1
	), false)
`

var (
	hasReadyIncrementalEnrichmentJobsQuery = strings.ReplaceAll(
		hasReadyEnrichmentJobsQueryTemplate,
		"{{lane_predicate}}",
		"priority >= 0",
	)
	hasReadyLegacyEnrichmentJobsQuery = strings.ReplaceAll(
		hasReadyEnrichmentJobsQueryTemplate,
		"{{lane_predicate}}",
		"priority < 0",
	)
)

func hasReadyEnrichmentJobsQueryForScope(scope EnrichmentScope) string {
	if scope == EnrichmentScopeLegacy {
		return hasReadyLegacyEnrichmentJobsQuery
	}
	return hasReadyIncrementalEnrichmentJobsQuery
}

func (q *EnrichmentQueue) HasReady(ctx context.Context, scope EnrichmentScope) (bool, error) {
	if q == nil || q.pool == nil {
		return false, errors.New("ebook enrichment queue is not configured")
	}
	if err := scope.validate(); err != nil {
		return false, err
	}
	var ready bool
	if err := q.pool.QueryRow(ctx, hasReadyEnrichmentJobsQueryForScope(scope)).Scan(&ready); err != nil {
		return false, err
	}
	return ready, nil
}

func countReadyEnrichmentJobsQueryForScope(scope EnrichmentScope) string {
	if scope == EnrichmentScopeLegacy {
		return countReadyLegacyEnrichmentJobsQuery
	}
	return countReadyIncrementalEnrichmentJobsQuery
}

func (q *EnrichmentQueue) ReadyCount(ctx context.Context, scope EnrichmentScope) (int, error) {
	if q == nil || q.pool == nil {
		return 0, errors.New("ebook enrichment queue is not configured")
	}
	if err := scope.validate(); err != nil {
		return 0, err
	}
	var count int
	if err := q.pool.QueryRow(ctx, countReadyEnrichmentJobsQueryForScope(scope)).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

var checkEnrichmentClaimQuery = `
	SELECT EXISTS (
		SELECT 1
		FROM ebook_enrichment_state
		WHERE content_id = $1
		  AND status = 'running'
		  AND claim_token = $2
		  AND lease_until > now()
	)
`

func (q *EnrichmentQueue) CheckClaim(ctx context.Context, job EnrichmentJob) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if job.ContentID == "" || job.Token == "" {
		return ErrEnrichmentLeaseLost
	}
	var owned bool
	if err := q.pool.QueryRow(ctx, checkEnrichmentClaimQuery, job.ContentID, job.Token).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrEnrichmentLeaseLost
	}
	return nil
}

var completeEnrichmentJobQuery = `
	UPDATE ebook_enrichment_state
	SET status = 'pending',
		lease_until = NULL,
		claim_token = NULL,
		completed_at = now(),
		next_attempt_at = CASE
			WHEN requeue_requested THEN now()
			ELSE now() + $3::interval
		END,
		outcome = $2,
		attempts = 0,
		priority = CASE WHEN requeue_requested THEN 100 ELSE 0 END,
		requeue_requested = false,
		last_error_class = NULL,
		last_error = NULL,
		updated_at = now()
	WHERE content_id = $1
	  AND status = 'running'
	  AND claim_token = $4
`

func (q *EnrichmentQueue) Complete(
	ctx context.Context,
	job EnrichmentJob,
	outcome EnrichmentOutcome,
	refreshAfter time.Duration,
) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if job.ContentID == "" || job.Token == "" {
		return ErrEnrichmentLeaseLost
	}
	if refreshAfter <= 0 {
		refreshAfter = enrichmentRefreshHorizon(outcome)
	}
	if refreshAfter <= 0 {
		return fmt.Errorf("unsupported ebook enrichment outcome %q", outcome)
	}

	tag, err := q.pool.Exec(
		ctx,
		completeEnrichmentJobQuery,
		job.ContentID,
		string(outcome),
		postgresInterval(refreshAfter),
		job.Token,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEnrichmentLeaseLost
	}
	return nil
}

var failEnrichmentJobQuery = `
	UPDATE ebook_enrichment_state
	SET status = 'pending',
		lease_until = NULL,
		claim_token = NULL,
		next_attempt_at = CASE
			WHEN requeue_requested THEN now()
			ELSE now() + $4::interval
		END,
		priority = CASE WHEN requeue_requested AND priority >= 0 THEN 100 ELSE priority END,
		requeue_requested = false,
		outcome = 'failed',
		last_error_class = $2,
		last_error = $3,
		updated_at = now()
	WHERE content_id = $1
	  AND status = 'running'
	  AND claim_token = $5
`

func (q *EnrichmentQueue) Fail(
	ctx context.Context,
	job EnrichmentJob,
	errorClass EnrichmentErrorClass,
	message string,
	retryAfter time.Duration,
) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if job.ContentID == "" || job.Token == "" {
		return ErrEnrichmentLeaseLost
	}
	delay := enrichmentRetryDelay(errorClass, job.Attempts, retryAfter)
	tag, err := q.pool.Exec(
		ctx,
		failEnrichmentJobQuery,
		job.ContentID,
		string(errorClass),
		message,
		postgresInterval(delay),
		job.Token,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEnrichmentLeaseLost
	}
	return nil
}

var releaseEnrichmentJobQuery = `
	UPDATE ebook_enrichment_state
	SET status = 'pending',
		lease_until = NULL,
		claim_token = NULL,
		attempts = GREATEST(attempts - 1, 0),
		next_attempt_at = CASE WHEN requeue_requested THEN now() ELSE next_attempt_at END,
		priority = CASE WHEN requeue_requested AND priority >= 0 THEN 100 ELSE priority END,
		requeue_requested = false,
		updated_at = now()
	WHERE content_id = $1
	  AND status = 'running'
	  AND claim_token = $2
`

func (q *EnrichmentQueue) Release(ctx context.Context, job EnrichmentJob) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if job.ContentID == "" || job.Token == "" {
		return ErrEnrichmentLeaseLost
	}
	tag, err := q.pool.Exec(ctx, releaseEnrichmentJobQuery, job.ContentID, job.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEnrichmentLeaseLost
	}
	return nil
}

var discardEnrichmentJobQuery = `
	UPDATE ebook_enrichment_state
	SET status = 'discarded',
		lease_until = NULL,
		claim_token = NULL,
		completed_at = now(),
		outcome = 'discarded',
		attempts = 0,
		priority = 0,
		requeue_requested = false,
		last_error_class = NULL,
		last_error = NULL,
		updated_at = now()
	WHERE content_id = $1
	  AND status = 'running'
	  AND claim_token = $2
`

func (q *EnrichmentQueue) Discard(ctx context.Context, job EnrichmentJob) error {
	if q == nil || q.pool == nil {
		return errors.New("ebook enrichment queue is not configured")
	}
	if job.ContentID == "" || job.Token == "" {
		return ErrEnrichmentLeaseLost
	}
	tag, err := q.pool.Exec(ctx, discardEnrichmentJobQuery, job.ContentID, job.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEnrichmentLeaseLost
	}
	return nil
}

func enrichmentRefreshHorizon(outcome EnrichmentOutcome) time.Duration {
	switch outcome {
	case EnrichmentOutcomeSuccess:
		return 90 * 24 * time.Hour
	case EnrichmentOutcomeNoMatch:
		return 30 * 24 * time.Hour
	case EnrichmentOutcomeSkipped:
		return skippedRetryHorizon
	default:
		return 0
	}
}

func enrichmentRetryDelay(errorClass EnrichmentErrorClass, attempts int, retryAfter time.Duration) time.Duration {
	switch errorClass {
	case EnrichmentErrorRateLimited:
		if retryAfter <= 0 {
			retryAfter = transientRetryDelay(attempts)
		}
		// A provider's short RetryInfo is request-pacing advice for its own
		// token bucket, not a queue horizon; requeueing that fast re-claims
		// the same saturated tail every run.
		retryAfter = max(retryAfter, rateLimitCooldownFloor())
		return min(retryAfter, maxEnrichmentRetry)
	case EnrichmentErrorPermanent:
		return 30 * 24 * time.Hour
	default:
		return transientRetryDelay(attempts)
	}
}

func transientRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := transientRetryBase
	for i := 1; i < attempts; i++ {
		if delay >= maxEnrichmentRetry/2 {
			return maxEnrichmentRetry
		}
		delay *= 2
	}
	return min(delay, maxEnrichmentRetry)
}

func postgresInterval(duration time.Duration) string {
	return fmt.Sprintf("%d microseconds", duration.Microseconds())
}
