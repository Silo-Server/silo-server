package markers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContributionRow is one submission audit record from marker_contributions.
type ContributionRow struct {
	ID               string
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

// AlreadySubmitted reports whether a non-error contribution with this value-hash
// already exists for the file+provider+segment. Errors are excluded so failed
// attempts can be retried.
func (s *ContributionStore) AlreadySubmitted(ctx context.Context, fileID int, provider, segmentKind, contentHash string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM marker_contributions
			WHERE media_file_id = $1 AND provider = $2 AND segment_kind = $3
			  AND content_hash = $4 AND status <> 'error'
		)`, fileID, provider, segmentKind, contentHash).Scan(&exists); err != nil {
		return false, fmt.Errorf("check marker contribution: %w", err)
	}
	return exists, nil
}

// Record upserts a contribution row keyed by (file, provider, segment, hash).
func (s *ContributionStore) Record(ctx context.Context, row ContributionRow) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("contribution store unavailable")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO marker_contributions (
			media_file_id, provider, segment_kind, source,
			submitted_start_ms, submitted_end_ms, video_duration_ms,
			content_hash, submission_id, status, http_status, error, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
		ON CONFLICT (media_file_id, provider, segment_kind, content_hash) DO UPDATE SET
			source = EXCLUDED.source,
			submitted_start_ms = EXCLUDED.submitted_start_ms,
			submitted_end_ms = EXCLUDED.submitted_end_ms,
			video_duration_ms = EXCLUDED.video_duration_ms,
			submission_id = EXCLUDED.submission_id,
			status = EXCLUDED.status,
			http_status = EXCLUDED.http_status,
			error = EXCLUDED.error,
			updated_at = now()`,
		row.MediaFileID, row.Provider, row.SegmentKind, row.Source,
		row.SubmittedStartMs, row.SubmittedEndMs, row.VideoDurationMs,
		row.ContentHash, row.SubmissionID, row.Status, row.HTTPStatus, row.Error,
	); err != nil {
		return fmt.Errorf("record marker contribution: %w", err)
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
