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
	return s.repo.ReportStatus(ctx, userID, profileID, deviceID, id, event)
}

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
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}
