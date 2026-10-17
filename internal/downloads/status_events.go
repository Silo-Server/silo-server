package downloads

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// StatusEvent identifies one local-device report for one registry revision.
// Clients retain UpdatedAt and Revision on retries; UpdatedAt is not send time.
type StatusEvent struct {
	Status    string
	UpdatedAt time.Time
	Revision  int
}

var ErrInvalidStatusEvent = errors.New("invalid download status event")

func (s *Service) ReportStatus(ctx context.Context, userID int, profileID, deviceID, id string, event StatusEvent) (*Download, error) {
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	if event.Status != StatusDownloading && event.Status != StatusCompleted {
		return nil, ErrInvalidStatus
	}
	if event.Revision < 1 || event.UpdatedAt.IsZero() || event.UpdatedAt.After(time.Now()) {
		return nil, ErrInvalidStatusEvent
	}
	d, err := s.repo.ReportStatus(ctx, userID, profileID, deviceID, id, event)
	if err == nil && event.Status == StatusCompleted && s.artifacts != nil {
		// A finished copy moves its prepared file from in use to cached and
		// changes the device's row, so open storage views re-read. A late
		// completion can also end the preparation it was waiting on.
		s.artifacts.notifyStorageChanged(ctx)
		if d.ArtifactID != "" {
			s.artifacts.notifyPreparationChanged(ctx, d.ArtifactID)
		}
	}
	return d, err
}

// expireUnneededQueuedJobSQL moves a job ($1) still waiting in the queue to
// expired when no in-flight download needs its bytes. The row keeps the recipe
// that finished downloads refer to. A job a worker may still hold, under its
// own lease or the unowned one a pause leaves, finishes into the cache instead:
// an expired row keeps its id and output path, so stopping a live encode could
// race a later prepare-again.
const expireUnneededQueuedJobSQL = `UPDATE download_artifacts a SET ` + expireArtifactAssignment + `, paused_at = NULL
	WHERE a.id = $1 AND a.status IN ('queued', 'tone_map_queued', 'audio_v2_queued', 'tracks_v1_queued')
	  AND a.lease_owner IS NULL AND (a.lease_expires_at IS NULL OR a.lease_expires_at <= now())
	  AND NOT EXISTS (SELECT 1 FROM downloads d WHERE d.artifact_id = a.id AND ` + inFlightLinkPredicate + `)`

// ReportStatus locks the existing lifecycle row. Late/equal events acknowledge
// the current state; a report for replaced bytes cannot mutate the new revision.
func (r *Repository) ReportStatus(ctx context.Context, userID int, profileID, deviceID, id string, event StatusEvent) (*Download, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanDownload(tx.QueryRow(ctx, `SELECT `+downloadColumns+` FROM downloads WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND device_id=$4 FOR UPDATE`, id, userID, profileID, deviceID))
	if err != nil {
		return nil, err
	}
	if current.Revision != event.Revision {
		return nil, ErrStatusConflict
	}
	switch current.Status {
	case StatusReady, StatusDownloading, StatusCompleted:
	case StatusPreparing:
		// Recovery returns an in-flight download to preparing, at the same
		// revision, when its prepared file goes missing. A device that fetched
		// the whole file first still holds it, and its completion report can
		// arrive after the reset, for example once the server update that lost
		// the file is over. Only that report applies; a transfer-start report
		// describes bytes the device can no longer fetch.
		if event.Status != StatusCompleted {
			return nil, ErrNotFound
		}
	default:
		return nil, ErrNotFound
	}
	if current.StatusEventAt != nil && !event.UpdatedAt.After(*current.StatusEventAt) {
		return current, nil
	}
	var completedAt *time.Time
	if event.Status == StatusCompleted {
		completedAt = &event.UpdatedAt
	}
	updated, err := scanDownload(tx.QueryRow(ctx, `UPDATE downloads SET status=$2,completed_at=$3,status_event_at=$4,updated_at=now() WHERE id=$1 RETURNING `+downloadColumns, id, event.Status, completedAt, event.UpdatedAt))
	if err != nil {
		return nil, fmt.Errorf("recording download status event: %w", err)
	}
	if event.Status == StatusCompleted && updated.ArtifactID != "" {
		// The cache period of a prepared file starts when the last device
		// waiting on it finishes, not when it was last served.
		if _, err := tx.Exec(ctx, `UPDATE download_artifacts SET last_used_at = now() WHERE id = $1`, updated.ArtifactID); err != nil {
			return nil, fmt.Errorf("touching completed download's artifact: %w", err)
		}
		// A late completion may leave a requeued job that nobody waits for.
		if current.Status == StatusPreparing {
			if _, err := tx.Exec(ctx, expireUnneededQueuedJobSQL, updated.ArtifactID); err != nil {
				return nil, fmt.Errorf("expiring unneeded preparation: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}
