package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrSubtitleGuardedDeletionUnavailable = errors.New("guarded subtitle deletion unavailable")

// SubtitleRevisionDeleter is optional so existing Repository callers remain compatible.
// Implementations must apply the revision predicate at the actual durable delete.
type SubtitleRevisionDeleter interface {
	DeleteDownloadedSubtitleWithRevision(context.Context, int, *int64) (*DownloadedSubtitle, error)
}

func (r *PgRepository) DeleteDownloadedSubtitleWithRevision(ctx context.Context, id int, revision *int64) (*DownloadedSubtitle, error) {
	var sub DownloadedSubtitle
	err := r.pool.QueryRow(ctx, `DELETE FROM downloaded_subtitles
 WHERE id = $1 AND ($2::bigint IS NULL OR revision = $2)
 RETURNING id, media_file_id, provider, language, format, release_name,
 s3_key, score, hearing_impaired, downloaded_by, created_at, COALESCE(content_sha256, ''), revision`, id, revision,
	).Scan(&sub.ID, &sub.MediaFileID, &sub.Provider, &sub.Language, &sub.Format,
		&sub.ReleaseName, &sub.S3Key, &sub.Score, &sub.HearingImpaired,
		&sub.DownloadedBy, &sub.CreatedAt, &sub.ContentSHA256, &sub.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		current, lookupErr := r.GetDownloadedSubtitle(ctx, id)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if current != nil && revision != nil {
			return nil, &SubtitleRevisionConflict{Current: current}
		}
		return nil, ErrSubtitleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("delete subtitle with revision: %w", err)
	}
	return &sub, nil
}

// DeleteSubtitleWithRevision removes only the captured row version. A nil revision
// is an explicit existence-only delete. SQL errors may mean an uncertain outcome;
// they never authorize object cleanup or an automatic retry. Success confirms row
// removal, while physical object cleanup remains best effort.
func (m *Manager) DeleteSubtitleWithRevision(ctx context.Context, id int, revision *int64) error {
	repo, ok := m.repo.(SubtitleRevisionDeleter)
	if !ok {
		return ErrSubtitleGuardedDeletionUnavailable
	}
	removed, err := repo.DeleteDownloadedSubtitleWithRevision(ctx, id, revision)
	if err != nil {
		return err
	}
	if removed == nil {
		return ErrSubtitleNotFound
	}
	m.cleanupSubtitleObject(ctx, removed.S3Key)
	return nil
}
