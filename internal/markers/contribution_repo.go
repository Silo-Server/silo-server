package markers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const contributionClaimIndex = "marker_contributions_provider_target_active_uidx"

// ContributionRow is one submission audit record from marker_contributions.
type ContributionRow struct {
	ID               string
	ClaimToken       string
	MediaFileID      int
	Provider         string
	SegmentKind      string
	Source           string
	SubmittedStartMs *int64
	SubmittedEndMs   *int64
	VideoDurationMs  *int64
	ContentHash      string
	TargetKey        string
	SubmissionID     *string
	Status           string
	HTTPStatus       *int
	Error            *string
	SubmittedAt      time.Time
	UpdatedAt        time.Time
}

// ContributionClaim identifies one ownership generation of a contribution
// row. The token fences a late worker from recording over a reclaimed claim.
type ContributionClaim struct {
	ID    string
	Token string
}

// ContributionStore persists marker contribution attempts for idempotency and
// audit.
type ContributionStore struct {
	pool *pgxpool.Pool
}

// NewContributionStore constructs a store backed by the supplied pool.
func NewContributionStore(pool *pgxpool.Pool) *ContributionStore {
	return &ContributionStore{pool: pool}
}

// ContentHash is a stable hash over the contributed value and resolved target.
// Identical submissions hash identically (never resubmitted); a marker
// correction or rematch to a different provider identity hashes differently.
func ContentHash(segmentKind string, startMs, endMs, durationMs *int64, targetParts ...string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s", segmentKind, ptrIntStr(startMs), ptrIntStr(endMs), ptrIntStr(durationMs))
	for _, part := range targetParts {
		fmt.Fprintf(h, "|%s", part)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func ptrIntStr(v *int64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatInt(*v, 10)
}

// Claim atomically reserves a provider target before the network call. Any
// active row for the same provider, segment, and target blocks the claim,
// whatever times it carried: providers accept one submission per account for
// each item. A stale in-flight row, or an invalid refusal older than
// contributionInvalidRecheck, can be reclaimed; recording a retryable error
// releases the claim. The advisory lock and partial unique index serialize
// claims across local media files and server workers.
func (s *ContributionStore) Claim(ctx context.Context, row ContributionRow, staleAfter time.Duration) (ContributionClaim, bool, error) {
	if s == nil || s.pool == nil {
		return ContributionClaim{}, false, fmt.Errorf("contribution store unavailable")
	}
	if staleAfter <= 0 {
		return ContributionClaim{}, false, fmt.Errorf("contribution claim lease must be positive")
	}
	if row.TargetKey == "" {
		return ContributionClaim{}, false, fmt.Errorf("contribution claim target missing")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("begin marker contribution claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended($1 || chr(31) || $2 || chr(31) || $3, 0)
		)`, row.Provider, row.SegmentKind, row.TargetKey); err != nil {
		return ContributionClaim{}, false, fmt.Errorf("lock marker contribution claim: %w", err)
	}

	leaseSeconds := int64(staleAfter / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	recheckSeconds := int64(contributionInvalidRecheck / time.Second)
	var activeID string
	var reclaimable bool
	err = tx.QueryRow(ctx, `
		SELECT id,
		       (status = $4 AND updated_at < now() - ($5 * interval '1 second'))
		       OR (status = $6 AND updated_at < now() - ($7 * interval '1 second'))
		FROM marker_contributions
		WHERE provider = $1 AND segment_kind = $2 AND target_key = $3
		  AND claim_active`,
		row.Provider, row.SegmentKind, row.TargetKey,
		contributionStatusClaim, leaseSeconds,
		OutcomeStatusInvalid, recheckSeconds,
	).Scan(&activeID, &reclaimable)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ContributionClaim{}, false, fmt.Errorf("find marker contribution claim: %w", err)
	}
	if err == nil {
		if !reclaimable {
			return ContributionClaim{}, false, nil
		}
		// Hand the target over by retiring the old row; its audit record stays.
		if _, err := tx.Exec(ctx, `
			UPDATE marker_contributions SET claim_active = false, updated_at = now()
			WHERE id = $1 AND claim_active`, activeID); err != nil {
			return ContributionClaim{}, false, fmt.Errorf("release stale marker contribution claim: %w", err)
		}
	}

	var claim ContributionClaim
	err = tx.QueryRow(ctx, `
		INSERT INTO marker_contributions (
			media_file_id, provider, segment_kind, source,
			submitted_start_ms, submitted_end_ms, video_duration_ms,
			content_hash, target_key, status, claim_active, claim_token, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,gen_random_uuid(),now())
		ON CONFLICT (media_file_id, provider, segment_kind, content_hash) DO UPDATE SET
			source = EXCLUDED.source,
			submitted_start_ms = EXCLUDED.submitted_start_ms,
			submitted_end_ms = EXCLUDED.submitted_end_ms,
			video_duration_ms = EXCLUDED.video_duration_ms,
			target_key = EXCLUDED.target_key,
			submission_id = NULL,
			status = EXCLUDED.status,
			http_status = NULL,
			error = NULL,
			claim_active = true,
			claim_token = gen_random_uuid(),
			updated_at = now()
		WHERE NOT marker_contributions.claim_active
		RETURNING id, claim_token`,
		row.MediaFileID, row.Provider, row.SegmentKind, row.Source,
		row.SubmittedStartMs, row.SubmittedEndMs, row.VideoDurationMs,
		row.ContentHash, row.TargetKey, contributionStatusClaim,
	).Scan(&claim.ID, &claim.Token)
	if errors.Is(err, pgx.ErrNoRows) || isContributionClaimConflict(err) {
		return ContributionClaim{}, false, nil
	}
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("claim marker contribution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContributionClaim{}, false, fmt.Errorf("commit marker contribution claim: %w", err)
	}
	return claim, true, nil
}

func isContributionClaimConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == contributionClaimIndex
}

// Record completes a claimed contribution. Retryable errors release the
// provider-target claim; every other result preserves it for deduplication.
func (s *ContributionStore) Record(ctx context.Context, row ContributionRow) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("contribution store unavailable")
	}
	if row.ID == "" || row.ClaimToken == "" {
		return fmt.Errorf("record marker contribution: claim identity missing")
	}
	claimActive := row.Status != OutcomeStatusError
	tag, err := s.pool.Exec(ctx, `
		UPDATE marker_contributions SET
			source = $3,
			submitted_start_ms = $4,
			submitted_end_ms = $5,
			video_duration_ms = $6,
			submission_id = $7,
			status = $8,
			http_status = $9,
			error = $10,
			claim_active = $11,
			updated_at = now()
		WHERE id = $1 AND claim_token = $2 AND claim_active`,
		row.ID, row.ClaimToken, row.Source,
		row.SubmittedStartMs, row.SubmittedEndMs, row.VideoDurationMs,
		row.SubmissionID, row.Status, row.HTTPStatus, row.Error,
		claimActive,
	)
	if err != nil {
		return fmt.Errorf("record marker contribution: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record marker contribution: claim not found")
	}
	return nil
}

// ContributionCandidate is one auto-contribution candidate and its keyset
// position.
type ContributionCandidate struct {
	FileID     int
	Confidence float64
}

// episodeTargetKeySQL derives contributionTargetKey in SQL for an episode row
// e and its series row series.
const episodeTargetKeySQL = `'episode|' ||
	CASE
		WHEN COALESCE(series.tmdb_id, '') <> '' THEN 'tmdb:' || series.tmdb_id
		WHEN COALESCE(series.tvdb_id, '') <> '' THEN 'tvdb:' || series.tvdb_id
		WHEN COALESCE(series.imdb_id, '') <> '' THEN 'imdb:' || series.imdb_id
		ELSE ''
	END || '|' || e.season_number || '|' || e.episode_number`

// CandidateLocalIntroFiles returns episode files carrying a local (scanner)
// intro marker at or above minConfidence, highest confidence first. A file is
// skipped while every listed provider holds a claim on its current target,
// from this file or another version of the episode, that Claim would not hand
// over; a claim on an older target, a stale in-flight claim, and an invalid
// refusal due for its recheck leave the file eligible.
// Paging is by keyset: pass the last candidate returned (nil for the first
// page).
func (s *ContributionStore) CandidateLocalIntroFiles(ctx context.Context, minConfidence float64, providers []string, after *ContributionCandidate, limit int) ([]ContributionCandidate, error) {
	if s == nil || s.pool == nil || len(providers) == 0 {
		return nil, nil
	}
	afterConfidence, afterID := 2.0, 0
	if after != nil {
		afterConfidence, afterID = after.Confidence, after.FileID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT mf.id, COALESCE(mf.intro_markers_confidence, 0) AS confidence
		FROM media_files mf
		JOIN episodes e ON e.content_id = mf.episode_id
		LEFT JOIN media_items series ON series.content_id = e.series_id
		WHERE mf.episode_id IS NOT NULL
		  AND mf.intro_markers_source = $1
		  AND mf.intro_start IS NOT NULL AND mf.intro_end IS NOT NULL
		  AND COALESCE(mf.intro_markers_confidence, 0) >= $2
		  AND e.season_number > 0 AND e.episode_number > 0
		  AND (COALESCE(mf.intro_markers_confidence, 0), -mf.id) < ($3::double precision, -$4::integer)
		  AND (
		      SELECT COUNT(DISTINCT mc.provider)
		      FROM marker_contributions mc
		      WHERE mc.segment_kind = 'intro'
		        AND mc.claim_active
		        AND mc.provider = ANY($5::text[])
		        AND mc.target_key = `+episodeTargetKeySQL+`
		        AND NOT (mc.status = $7 AND mc.updated_at < now() - ($8 * interval '1 second'))
		        AND NOT (mc.status = $9 AND mc.updated_at < now() - ($10 * interval '1 second'))
		  ) < cardinality($5::text[])
		ORDER BY confidence DESC, mf.id
		LIMIT $6`,
		models.MarkerSourceScanner, minConfidence, afterConfidence, afterID, providers, limit,
		contributionStatusClaim, int64(contributionClaimLease/time.Second),
		OutcomeStatusInvalid, int64(contributionInvalidRecheck/time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("query contribution candidates: %w", err)
	}
	defer rows.Close()

	var out []ContributionCandidate
	for rows.Next() {
		var c ContributionCandidate
		if err := rows.Scan(&c.FileID, &c.Confidence); err != nil {
			return nil, fmt.Errorf("scan contribution candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListByFile returns the contribution history for a file, newest first.
func (s *ContributionStore) ListByFile(ctx context.Context, fileID int) ([]ContributionRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, media_file_id, provider, segment_kind, source,
		       submitted_start_ms, submitted_end_ms, video_duration_ms,
		       content_hash, submission_id, status, http_status, error,
		       submitted_at, updated_at
		FROM marker_contributions WHERE media_file_id = $1
		ORDER BY updated_at DESC`, fileID)
	if err != nil {
		return nil, fmt.Errorf("list marker contributions: %w", err)
	}
	defer rows.Close()

	var out []ContributionRow
	for rows.Next() {
		var r ContributionRow
		if err := rows.Scan(
			&r.ID, &r.MediaFileID, &r.Provider, &r.SegmentKind, &r.Source,
			&r.SubmittedStartMs, &r.SubmittedEndMs, &r.VideoDurationMs,
			&r.ContentHash, &r.SubmissionID, &r.Status, &r.HTTPStatus, &r.Error,
			&r.SubmittedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan marker contribution: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ContributionPagePosition is a descending keyset position, not a snapshot.
// Rows updated during traversal may move ahead of the current page.
type ContributionPagePosition struct {
	UpdatedAt time.Time
	ID        string
}

func nullContributionTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *ContributionStore) ListByFilePage(ctx context.Context, fileID int, limit int, after ContributionPagePosition) ([]ContributionRow, bool, error) {
	if s == nil || s.pool == nil {
		return []ContributionRow{}, false, nil
	}
	if limit < 1 || limit > 200 {
		return nil, false, fmt.Errorf("invalid contribution page limit")
	}
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, media_file_id, provider, segment_kind, source,
		       submitted_start_ms, submitted_end_ms, video_duration_ms,
		       content_hash, submission_id, status, http_status, error,
		       submitted_at, updated_at
		FROM marker_contributions WHERE media_file_id = $1
		AND ($2::timestamptz IS NULL OR (updated_at, id) < ($2, $3::uuid))
 ORDER BY updated_at DESC, id DESC LIMIT $4`, fileID, nullContributionTime(after.UpdatedAt), after.ID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list marker contributions: %w", err)
	}
	defer rows.Close()

	var out []ContributionRow
	for rows.Next() {
		var r ContributionRow
		if err := rows.Scan(
			&r.ID, &r.MediaFileID, &r.Provider, &r.SegmentKind, &r.Source,
			&r.SubmittedStartMs, &r.SubmittedEndMs, &r.VideoDurationMs,
			&r.ContentHash, &r.SubmissionID, &r.Status, &r.HTTPStatus, &r.Error,
			&r.SubmittedAt, &r.UpdatedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan marker contribution: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
