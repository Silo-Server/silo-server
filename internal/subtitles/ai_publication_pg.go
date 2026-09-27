package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PublishAISubtitle serializes metadata publication with cancellation and stale
// recovery on the job row. Final output and job completion commit together.
// Intermediate output commits while active and remains valid if a later phase
// is canceled. No object-store work runs while this transaction holds locks.
func (r *PgRepository) PublishAISubtitle(ctx context.Context, candidate *DownloadedSubtitle, publication AIJobPublication, legacy *DownloadedSubtitle) (*DownloadedSubtitle, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // No-op after commit.
	var jobID int64
	err = tx.QueryRow(ctx, `SELECT id FROM subtitle_ai_jobs WHERE id=$1
 AND status IN ('pending','running') AND media_file_id=$2
 AND requested_by IS NOT DISTINCT FROM $3::bigint FOR UPDATE`, publication.JobID, candidate.MediaFileID, candidate.DownloadedBy).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAIJobInactive
	}
	if err != nil {
		return nil, fmt.Errorf("lock subtitle AI publication: %w", err)
	}
	lookup := func() (*DownloadedSubtitle, error) {
		legacyID := 0
		legacyKey := ""
		if legacy != nil {
			legacyID = legacy.ID
			legacyKey = legacy.S3Key
		}
		var sub DownloadedSubtitle
		err := tx.QueryRow(ctx, `SELECT id,media_file_id,provider,language,format,release_name,s3_key,
   score,hearing_impaired,downloaded_by,created_at,COALESCE(content_sha256,''),revision
   FROM downloaded_subtitles WHERE media_file_id=$1 AND provider=$2 AND language=$3 AND format=$4
   AND (content_sha256=$5 OR (content_sha256 IS NULL AND id=$6 AND s3_key=$7))
   ORDER BY id LIMIT 1 FOR SHARE`, candidate.MediaFileID, candidate.Provider, candidate.Language, candidate.Format, candidate.ContentSHA256, legacyID, legacyKey).Scan(
			&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format, &sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired, &sub.DownloadedBy, &sub.CreatedAt, &sub.ContentSHA256, &sub.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return &sub, err
	}
	sub, err := lookup()
	if err != nil {
		return nil, err
	}
	if sub == nil {
		err = tx.QueryRow(ctx, `INSERT INTO downloaded_subtitles
   (media_file_id,provider,language,format,release_name,s3_key,score,hearing_impaired,downloaded_by,content_sha256)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING RETURNING id,created_at,revision`,
			candidate.MediaFileID, candidate.Provider, candidate.Language, candidate.Format, candidate.ReleaseName, candidate.S3Key, candidate.Score, candidate.HearingImpaired, candidate.DownloadedBy, candidate.ContentSHA256).Scan(&candidate.ID, &candidate.CreatedAt, &candidate.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			// A competing job can win content identity while we wait at INSERT.
			// Read in a new statement snapshot and lock its row against deletion.
			sub, err = lookup()
			if err == nil && sub == nil {
				err = ErrSubtitleNotFound
			}
		} else if err == nil {
			sub = candidate
		}
		if err != nil {
			return nil, err
		}
	}
	if publication.Complete {
		_, err = tx.Exec(ctx, `UPDATE subtitle_ai_jobs SET status='completed',progress=1,result_subtitle_id=$2,
   error_message='',updated_at=now(),heartbeat_at=now() WHERE id=$1`, jobID, sub.ID)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit subtitle AI publication: %w", ErrAIPublicationUncertain, err)
	}
	return sub, nil
}
