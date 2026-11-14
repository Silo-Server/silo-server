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

// registryPageMaxRows is the most rows one registry read returns: the largest
// page a caller may ask for and one more row, which shows whether another
// page follows.
const registryPageMaxRows = 101

// RegistryPage is one page of the download registry. Next is the position of
// the last item, set while more rows follow.
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

// accountPageMaxReads bounds how far one account-level page reads past rows the
// profile may not open. Account-level rows are pruned about 7 days after their
// last update, but nothing caps how many an account queues in that time when
// the administrator sets no download limits.
const accountPageMaxReads = 20

// listAccountPage pages the account-level rows the requesting profile may
// open. A position names a row's ID and creation time, and a cursor is signed
// but readable, so Next is only ever the last returned row, and only when
// another row the profile may open follows it. Rows are read in full batches
// and the hidden ones dropped until the page and that one further row are
// found or the rows run out, so a page is full unless the list is ending. The
// next page starts after its last returned row and reads any hidden rows
// behind it again.
//
// After accountPageMaxReads batches the page returns what it found with no
// Next: a profile behind that many hidden rows sees the list end early.
func (s *Service) listAccountPage(ctx context.Context, userID int, after *RegistryPosition, limit int, filter catalog.AccessFilter) (RegistryPage, error) {
	if limit < 1 || limit >= registryPageMaxRows {
		return RegistryPage{}, fmt.Errorf("download page limit must be 1 to %d", registryPageMaxRows-1)
	}
	visible := make([]*Download, 0, limit+1)
	for reads := 0; len(visible) <= limit; reads++ {
		if reads == accountPageMaxReads {
			slog.WarnContext(ctx, "account download page stopped reading past hidden rows", "component", "downloads", "user_id", userID, "rows_read", reads*registryPageMaxRows)
			break
		}
		rows, err := s.repo.ListPage(ctx, userID, "", "", after, registryPageMaxRows)
		if err != nil {
			return RegistryPage{}, err
		}
		if len(rows) == 0 {
			break
		}
		last := rows[len(rows)-1]
		after = &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}
		exhausted := len(rows) < registryPageMaxRows
		if rows, err = s.accessibleRows(ctx, rows, filter); err != nil {
			return RegistryPage{}, err
		}
		visible = append(visible, rows...)
		if exhausted {
			break
		}
	}
	page := RegistryPage{}
	if len(visible) > limit {
		visible = visible[:limit]
		last := visible[limit-1]
		page.Next = &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if err := s.repo.attachPreparations(ctx, visible); err != nil {
		// Progress is decoration; the registry is still correct without it.
		slog.WarnContext(ctx, "download preparation progress unavailable", "component", "downloads", "error", err)
	}
	page.Items = visible
	return page, nil
}

// ListPage preserves the separate account-ephemeral and profile/device modes.
// ID breaks timestamp ties; creation time remains stable through status reports.
func (s *Service) ListPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int, filter catalog.AccessFilter) (RegistryPage, error) {
	if deviceID == "" {
		return s.listAccountPage(ctx, userID, after, limit, filter)
	}
	if profileID == "" {
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
	if after == nil {
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
	if limit < 1 || limit > registryPageMaxRows {
		return nil, fmt.Errorf("download page limit must be 1 to %d", registryPageMaxRows)
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
