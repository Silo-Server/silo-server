package downloads

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// PrepareAgain asks the server to prepare a finished managed download's file
// again after its server copy expired, so the device can fetch it once more.
// The row returns to preparing and becomes ready like a new download; its
// revision does not change, because the recipe is the same. A row whose file
// is still on the server, an original-quality row (served from the source),
// or a row already being prepared comes back unchanged.
func (s *Service) PrepareAgain(ctx context.Context, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (*Download, error) {
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	cfg, _, err := s.downloadConfigForUser(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	dl, err := s.repo.GetManagedByID(ctx, downloadID, userID, profileID, deviceID)
	if err != nil {
		return nil, err
	}
	switch dl.Status {
	case StatusRevoked, StatusFailed, StatusCancelled:
		return nil, fmt.Errorf("download is %s: %w", dl.Status, ErrDownloadNotActive)
	}
	if dl.Format == FormatOriginal || dl.ArtifactID == "" {
		return dl, nil
	}
	if err := s.itemAccess.EnsureAccessible(ctx, dl.ContentID, filter); err != nil {
		return nil, err
	}
	if s.artifacts == nil || (dl.Format == FormatTranscode && !cfg.TranscodeEnabled) {
		return nil, ErrFormatUnavailable
	}
	// An entry that becomes preparing again counts toward the concurrent cap
	// like a new download would. Whether it does is decided under the same
	// per-account lock, from the entry and its file as they are now: a file
	// still on the server is served as it is, and a concurrent call may
	// already have made the entry active. It creates no download, so the
	// period quota is untouched.
	var updated *Download
	var requeued bool
	err = s.repo.WithUserQuotaLock(ctx, userID, func(ctx context.Context) error {
		current, err := s.repo.GetManagedByID(ctx, downloadID, userID, profileID, deviceID)
		if err != nil {
			return err
		}
		activates, err := s.artifacts.prepareAgainActivates(ctx, current)
		if err != nil {
			return err
		}
		if activates {
			if err := s.limiter.CheckCounts(ctx, userID, 1, 0); err != nil {
				return err
			}
		}
		updated, requeued, err = s.artifacts.repo.PrepareDownloadAgain(ctx, current)
		return err
	})
	if err != nil {
		return nil, err
	}
	if requeued {
		s.artifacts.notifyPreparationChanged(ctx, dl.ArtifactID)
		s.artifacts.triggerDrain()
	}
	if updated.Status != dl.Status {
		s.artifacts.publish(ctx, updated)
	}
	return updated, nil
}

// prepareAgainActivates reports whether PrepareDownloadAgain would return d
// to preparing, which makes it count toward the concurrent cap: a finished or
// ready entry whose file is no longer ready. A missing file row is left to
// PrepareDownloadAgain, which refuses it.
func (m *ArtifactManager) prepareAgainActivates(ctx context.Context, d *Download) (bool, error) {
	if d.Status != StatusCompleted && d.Status != StatusReady {
		return false, nil
	}
	a, err := m.repo.GetByID(ctx, d.ArtifactID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !artifactReady(a), nil
}

// PrepareDownloadAgain requeues the expired (or failed) artifact a finished
// download links to and returns that download to preparing, in one
// transaction, so the row is never preparing against an artifact no worker
// will claim. requeued is false when the artifact was already ready or queued.
func (r *ArtifactRepository) PrepareDownloadAgain(ctx context.Context, d *Download) (*Download, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning prepare again: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM download_artifacts WHERE id = $1 FOR UPDATE`, d.ArtifactID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The recipe is gone with its row; only a new download can choose one.
			return nil, false, fmt.Errorf("prepared file recipe is gone: %w", ErrDownloadNotActive)
		}
		return nil, false, fmt.Errorf("locking artifact to prepare again: %w", err)
	}
	if artifactReady(&Artifact{Status: status}) {
		return d, false, nil
	}
	requeued := false
	if status == ArtifactExpired || status == ArtifactFailed {
		if _, err := tx.Exec(ctx,
			`UPDATE download_artifacts
			 SET status = CASE
			                  WHEN track_recipe_version <> '' THEN 'tracks_v1_queued'
			                  WHEN audio_recipe_version <> '' THEN 'audio_v2_queued'
			                  WHEN tone_map_mode <> '' THEN 'tone_map_queued'
			                  ELSE 'queued'
			              END,
			     attempts = 0, error_message = '', next_retry_at = NULL, completed_at = NULL,
			     last_used_at = now()
			 WHERE id = $1`, d.ArtifactID); err != nil {
			return nil, false, fmt.Errorf("requeuing artifact to prepare again: %w", err)
		}
		requeued = true
	}
	updated, err := scanDownload(tx.QueryRow(ctx,
		`UPDATE downloads SET status = 'preparing', bytes_sent = 0, completed_at = NULL, error_message = '', updated_at = now()
		 WHERE id = $1 AND artifact_id = $2 AND status IN ('completed', 'ready', 'downloading')
		 RETURNING `+downloadColumns, d.ID, d.ArtifactID))
	if errors.Is(err, ErrNotFound) {
		// Already preparing, or changed concurrently: report the stored row.
		updated, err = scanDownload(tx.QueryRow(ctx, `SELECT `+downloadColumns+` FROM downloads WHERE id = $1`, d.ID))
	}
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing prepare again: %w", err)
	}
	return updated, requeued, nil
}
