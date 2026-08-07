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

const contributionClaimIndex = "marker_contributions_provider_hash_active_uidx"

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

// Claim atomically reserves a provider-target payload before the network call.
// A terminal or fresh in-flight row keeps the claim active; recording a
// retryable error releases it, and a stale in-flight row can be reclaimed.
// The advisory lock and partial unique index serialize identical payloads
// across different local media files and server workers.
func (s *ContributionStore) Claim(ctx context.Context, row ContributionRow, staleAfter time.Duration) (ContributionClaim, bool, error) {
	if s == nil || s.pool == nil {
		return ContributionClaim{}, false, fmt.Errorf("contribution store unavailable")
	}
	if staleAfter <= 0 {
		return ContributionClaim{}, false, fmt.Errorf("contribution claim lease must be positive")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("begin marker contribution claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended($1 || chr(31) || $2 || chr(31) || $3, 0)
		)`, row.Provider, row.SegmentKind, row.ContentHash); err != nil {
		return ContributionClaim{}, false, fmt.Errorf("lock marker contribution claim: %w", err)
	}

	leaseSeconds := int64(staleAfter / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	var activeID, activeStatus string
	var stale bool
	err = tx.QueryRow(ctx, `
		SELECT id, status, updated_at < now() - ($4 * interval '1 second')
		FROM marker_contributions
		WHERE provider = $1 AND segment_kind = $2 AND content_hash = $3
		  AND claim_active`,
		row.Provider, row.SegmentKind, row.ContentHash, leaseSeconds,
	).Scan(&activeID, &activeStatus, &stale)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ContributionClaim{}, false, fmt.Errorf("find marker contribution claim: %w", err)
	}
	if err == nil {
		if activeStatus != contributionStatusClaim || !stale {
			return ContributionClaim{}, false, nil
		}
		claim, reclaimed, err := reclaimContribution(ctx, tx, activeID, row, leaseSeconds)
		if err != nil {
			return ContributionClaim{}, false, err
		}
		if reclaimed {
			if err := tx.Commit(ctx); err != nil {
				return ContributionClaim{}, false, fmt.Errorf("commit marker contribution claim: %w", err)
			}
			return claim, true, nil
		}
	}

	var claim ContributionClaim
	err = tx.QueryRow(ctx, `
		INSERT INTO marker_contributions (
			media_file_id, provider, segment_kind, source,
			submitted_start_ms, submitted_end_ms, video_duration_ms,
			content_hash, status, claim_active, claim_token, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,true,gen_random_uuid(),now())
		ON CONFLICT (media_file_id, provider, segment_kind, content_hash) DO UPDATE SET
			source = EXCLUDED.source,
			submitted_start_ms = EXCLUDED.submitted_start_ms,
			submitted_end_ms = EXCLUDED.submitted_end_ms,
			video_duration_ms = EXCLUDED.video_duration_ms,
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
		row.ContentHash, contributionStatusClaim,
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

func reclaimContribution(ctx context.Context, tx pgx.Tx, id string, row ContributionRow, leaseSeconds int64) (ContributionClaim, bool, error) {
	var claim ContributionClaim
	err := tx.QueryRow(ctx, `
		UPDATE marker_contributions SET
			source = $2,
			submitted_start_ms = $3,
			submitted_end_ms = $4,
			video_duration_ms = $5,
			submission_id = NULL,
			status = $6,
			http_status = NULL,
			error = NULL,
			claim_token = gen_random_uuid(),
			updated_at = now()
		WHERE id = $1 AND claim_active AND status = $6
		  AND updated_at < now() - ($7 * interval '1 second')
		RETURNING id, claim_token`,
		id, row.Source, row.SubmittedStartMs, row.SubmittedEndMs,
		row.VideoDurationMs, contributionStatusClaim, leaseSeconds,
	).Scan(&claim.ID, &claim.Token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContributionClaim{}, false, nil
	}
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("reclaim marker contribution: %w", err)
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

// CandidateLocalIntroFiles returns ids of episode files carrying a local
// (scanner) intro marker at or above minConfidence — the auto-contribution
// candidates. Iterated by keyset (id > afterID) for stable paging.
func (s *ContributionStore) CandidateLocalIntroFiles(ctx context.Context, minConfidence float64, afterID, limit int) ([]int, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM media_files
		WHERE episode_id IS NOT NULL
		  AND intro_markers_source = $1
		  AND intro_start IS NOT NULL AND intro_end IS NOT NULL
		  AND COALESCE(intro_markers_confidence, 0) >= $2
		  AND id > $3
		ORDER BY id
		LIMIT $4`, models.MarkerSourceScanner, minConfidence, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query contribution candidates: %w", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan candidate id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
