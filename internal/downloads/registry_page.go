package downloads

import (
	"context"
	"fmt"
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
	return s.repo.ListPage(ctx, userID, profileID, deviceID, after, limit)
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
