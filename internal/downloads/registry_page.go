package downloads

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type RegistryPosition struct {
	CreatedAt time.Time
	ID        string
}

// ListPage preserves the separate account-ephemeral and profile/device modes.
// ID breaks timestamp ties; creation time remains stable through status reports.
func (s *Service) ListPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int) ([]*Download, error) {
	if deviceID != "" && profileID == "" {
		return nil, ErrProfileRequired
	}
	rows, err := s.repo.ListPage(ctx, userID, profileID, deviceID, after, limit)
	if err != nil {
		return nil, err
	}
	if after == nil && deviceID != "" {
		// A registry read is the device syncing: it is how a revoked copy
		// reaches the device, and what makes "last seen" mean something.
		if err := s.repo.TouchDeviceSeen(ctx, userID, profileID, deviceID); err != nil {
			slog.WarnContext(ctx, "recording device sync failed", "component", "downloads", "error", err)
		}
	}
	if err := s.repo.attachPreparations(ctx, rows); err != nil {
		// Progress is decoration; the registry is still correct without it.
		slog.WarnContext(ctx, "download preparation progress unavailable", "component", "downloads", "error", err)
	}
	return rows, nil
}
func (r *Repository) ListPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int) ([]*Download, error) {
	return r.listRegistryPage(ctx, userID, profileID, deviceID, "", after, limit)
}
func (r *Repository) listRegistryPage(ctx context.Context, userID int, profileID, deviceID, batchID string, after *RegistryPosition, limit int) ([]*Download, error) {
	if limit < 1 || limit > 101 {
		return nil, fmt.Errorf("download page limit must be 1 to 101")
	}
	var at *time.Time
	id := ""
	if after != nil {
		at = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT `+downloadColumns+` FROM downloads WHERE user_id=$1
 AND (($3='' AND device_id IS NULL) OR ($3<>'' AND profile_id=$2 AND device_id=$3))
 AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5))
 AND ($7='' OR batch_id=$7)
 ORDER BY created_at DESC,id DESC LIMIT $6`, userID, profileID, deviceID, at, id, limit, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDownloads(rows)
}
