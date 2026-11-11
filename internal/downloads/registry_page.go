package downloads

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type RegistryPosition struct {
	CreatedAt time.Time
	ID        string
}

// RegistryPage is one page of the download registry. Next is set while more
// rows follow, whether or not this page holds any.
type RegistryPage struct {
	Items []*Download
	Next  *RegistryPosition
}

// accessibleRows drops the rows whose item the filter hides. Account-level
// rows are shared by every profile on the account, so listing them applies the
// requesting profile's content scope (library and maturity), as serving one
// does. A failed check is an error, never the unfiltered rows.
func (s *Service) accessibleRows(ctx context.Context, rows []*Download, filter catalog.AccessFilter) ([]*Download, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ContentID)
	}
	allowed, err := s.itemAccess.EnsureAccessibleIDs(ctx, ids, filter)
	if err != nil {
		return nil, fmt.Errorf("checking access to downloaded items: %w", err)
	}
	return slices.DeleteFunc(rows, func(row *Download) bool { return !allowed[row.ContentID] }), nil
}

// ListPage preserves the separate account-ephemeral and profile/device modes.
// ID breaks timestamp ties; creation time remains stable through status reports.
// Next comes from the rows read, before account-level rows the profile may not
// open are dropped, so a page left short or empty still leads to the next one.
func (s *Service) ListPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int, filter catalog.AccessFilter) (RegistryPage, error) {
	if deviceID != "" && profileID == "" {
		return RegistryPage{}, ErrProfileRequired
	}
	rows, err := s.repo.ListPage(ctx, userID, profileID, deviceID, after, limit+1)
	if err != nil {
		return RegistryPage{}, err
	}
	page := RegistryPage{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.Next = &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if deviceID == "" {
		if rows, err = s.accessibleRows(ctx, rows, filter); err != nil {
			return RegistryPage{}, err
		}
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
	page.Items = rows
	return page, nil
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
