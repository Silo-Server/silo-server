package downloads

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Device listing orders.
const (
	DeviceSortLastSeen = "last_seen" // longest unseen first
	DeviceSortSize     = "size"      // most bytes on the device first
)

// AdminDeviceFilter narrows the server-wide device listing.
type AdminDeviceFilter struct {
	Query string // account, profile, or device name
	// StaleOnly keeps devices not seen within the stale-device age.
	StaleOnly bool
	Platform  string
	Sort      string
}

// AdminDevicePosition is a keyset position in the device listing.
type AdminDevicePosition struct {
	LastSeen  time.Time // zero sorts first (never seen)
	Bytes     int64
	UserID    int
	ProfileID string
	DeviceID  string
}

// AdminDeviceRow is one device holding managed downloads, with counts.
type AdminDeviceRow struct {
	UserID      int
	Username    string
	ProfileID   string
	ProfileName string
	DeviceID    string
	DeviceName  string
	Platform    string
	LastSeenAt  *time.Time
	// Copies counts rows that are not revoked; Finished, Waiting, Failed
	// split them. Revoked counts rows still waiting for the device to confirm.
	Copies, Finished, Waiting, Failed, Revoked int
	// BytesOnDevice is the size of the finished copies, including revoked
	// ones the device has not confirmed deleting.
	BytesOnDevice int64
	Monitors      int
	Stale         bool
}

// AdminListDevicesPage lists every device that holds managed downloads.
func (s *Service) AdminListDevicesPage(ctx context.Context, f AdminDeviceFilter, after *AdminDevicePosition, limit int) ([]AdminDeviceRow, error) {
	return s.repo.listDevicesPage(ctx, f, after, limit)
}

func (r *Repository) listDevicesPage(ctx context.Context, f AdminDeviceFilter, after *AdminDevicePosition, limit int) ([]AdminDeviceRow, error) {
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("device page limit must be 1 to %d", adminPageMaxLimit)
	}
	order := "COALESCE(g.last_seen_at, 'epoch'::timestamptz) ASC, g.user_id, g.profile_id, g.device_id"
	// Each order reads one cursor value; the other is always NULL there, and
	// naming it keeps every parameter typed.
	cursor := "$7::bigint IS NULL AND ($6::timestamptz IS NULL OR (COALESCE(g.last_seen_at, 'epoch'::timestamptz), g.user_id, g.profile_id, g.device_id) > ($6, $8, $9, $10))"
	if f.Sort == DeviceSortSize {
		order = "g.bytes_on_device DESC, g.user_id, g.profile_id, g.device_id"
		cursor = "$6::timestamptz IS NULL AND ($7::bigint IS NULL OR g.bytes_on_device < $7 OR (g.bytes_on_device = $7 AND (g.user_id, g.profile_id, g.device_id) > ($8, $9, $10)))"
	}
	var afterSeen *time.Time
	var afterBytes *int64
	afterUser, afterProfile, afterDevice := 0, "", ""
	if after != nil {
		afterUser, afterProfile, afterDevice = after.UserID, after.ProfileID, after.DeviceID
		if f.Sort == DeviceSortSize {
			afterBytes = &after.Bytes
		} else {
			seen := after.LastSeen
			if seen.IsZero() {
				seen = time.Unix(0, 0)
			}
			afterSeen = &seen
		}
	}
	rows, err := r.pool.Query(ctx,
		`WITH g AS (
			SELECT d.user_id, d.profile_id, d.device_id,
			       COALESCE(max(us.username), '') AS username, COALESCE(max(p.name), '') AS profile_name,
			       COALESCE(max(u.device_name), '') AS device_name, COALESCE(max(u.device_platform), '') AS platform,
			       max(u.last_seen_at) AS last_seen_at,
			       count(*) FILTER (WHERE d.status <> 'revoked') AS copies,
			       count(*) FILTER (WHERE d.status = 'completed') AS finished,
			       count(*) FILTER (WHERE d.status IN ('queued', 'preparing', 'ready', 'downloading')) AS waiting,
			       count(*) FILTER (WHERE d.status = 'failed') AS failed,
			       count(*) FILTER (WHERE d.status = 'revoked') AS revoked,
			       -- A revoked finished copy stays on the device until it confirms the
			       -- delete, which removes the row.
			       COALESCE(SUM(GREATEST(d.file_size, 0)) FILTER (WHERE d.status = 'completed'
			                    OR (d.status = 'revoked' AND d.completed_at IS NOT NULL)), 0)::bigint AS bytes_on_device
			FROM downloads d
			LEFT JOIN user_devices u ON u.user_id = d.user_id AND u.profile_id = d.profile_id AND u.device_id = d.device_id
			LEFT JOIN users us ON us.id = d.user_id
			LEFT JOIN user_profiles p ON p.user_id = d.user_id AND p.id = d.profile_id
			WHERE d.device_id IS NOT NULL
			GROUP BY d.user_id, d.profile_id, d.device_id
		 )
		 SELECT g.user_id, g.username, g.profile_id, g.profile_name, g.device_id, g.device_name, g.platform, g.last_seen_at,
		        g.copies, g.finished, g.waiting, g.failed, g.revoked, g.bytes_on_device,
		        (SELECT count(*) FROM download_subscriptions s
		         WHERE s.user_id = g.user_id AND s.profile_id = g.profile_id AND s.device_id = g.device_id AND s.active)::int
		 FROM g
		 WHERE ($1 = '' OR g.username ILIKE '%' || $1 || '%' OR g.profile_name ILIKE '%' || $1 || '%' OR g.device_name ILIKE '%' || $1 || '%')
		   AND (NOT $2 OR g.last_seen_at IS NULL OR g.last_seen_at < now() - make_interval(secs => $3))
		   AND ($4 = '' OR lower(g.platform) = lower($4))
		   AND `+cursor+`
		 ORDER BY `+order+`
		 LIMIT $5`,
		escapeLike(strings.TrimSpace(f.Query)), f.StaleOnly, staleDeviceAge.Seconds(), f.Platform, limit,
		afterSeen, afterBytes, afterUser, afterProfile, afterDevice)
	if err != nil {
		return nil, fmt.Errorf("listing download devices: %w", err)
	}
	defer rows.Close()
	staleBefore := time.Now().Add(-staleDeviceAge)
	var out []AdminDeviceRow
	for rows.Next() {
		var row AdminDeviceRow
		if err := rows.Scan(&row.UserID, &row.Username, &row.ProfileID, &row.ProfileName, &row.DeviceID, &row.DeviceName,
			&row.Platform, &row.LastSeenAt, &row.Copies, &row.Finished, &row.Waiting, &row.Failed, &row.Revoked,
			&row.BytesOnDevice, &row.Monitors); err != nil {
			return nil, fmt.Errorf("scanning download device: %w", err)
		}
		row.Stale = row.LastSeenAt == nil || row.LastSeenAt.Before(staleBefore)
		out = append(out, row)
	}
	return out, rows.Err()
}

// AdminEntryFilter narrows the server-wide managed-download listing.
type AdminEntryFilter struct {
	UserID    int
	ProfileID string
	DeviceID  string
	Status    string
}

// AdminEntryRow is a managed download with the device and storage context an
// administrator needs to act on it.
type AdminEntryRow struct {
	AdminDownloadRow
	Username      string
	DeviceName    string
	RevokedAt     *time.Time
	RevokedReason string
	// Location is where its prepared file lives, when it has one.
	Location     string
	LocationName string
}

// AdminListEntriesPage lists managed downloads across accounts newest first.
func (s *Service) AdminListEntriesPage(ctx context.Context, f AdminEntryFilter, after *RegistryPosition, limit int) ([]AdminEntryRow, error) {
	return s.repo.listEntriesPage(ctx, f, after, limit)
}

func (r *Repository) listEntriesPage(ctx context.Context, f AdminEntryFilter, after *RegistryPosition, limit int) ([]AdminEntryRow, error) {
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("download page limit must be 1 to %d", adminPageMaxLimit)
	}
	at, id := adminPagePosition(after)
	rows, err := r.pool.Query(ctx, `SELECT `+qualifiedColumns(downloadColumns, "d")+`,
		COALESCE(mi.title, ''), COALESCE(mi.type, ''), ep.season_number, ep.episode_number, COALESCE(ep.title, ''),
		COALESCE(us.username, ''), COALESCE(u.device_name, ''), d.revoked_at, d.revoked_reason,
		`+storageFileLocationSQL+`, COALESCE(n.name, '')
	FROM downloads d
	LEFT JOIN media_items mi ON mi.content_id = d.content_id
	LEFT JOIN episodes ep ON ep.content_id = d.episode_id
	LEFT JOIN users us ON us.id = d.user_id
	LEFT JOIN user_devices u ON u.user_id = d.user_id AND u.profile_id = d.profile_id AND u.device_id = d.device_id
	LEFT JOIN download_artifacts a ON a.id = d.artifact_id
	LEFT JOIN stream_nodes n ON n.id = `+storageFileLocationSQL+`
	WHERE d.device_id IS NOT NULL
		AND ($1 = 0 OR d.user_id = $1)
		AND ($2 = '' OR d.profile_id = $2)
		AND ($3 = '' OR d.device_id = $3)
		AND ($4 = '' OR d.status = $4)
		AND ($5::timestamptz IS NULL OR (d.created_at, d.id) < ($5, $6))
	ORDER BY d.created_at DESC, d.id DESC
	LIMIT $7`, f.UserID, f.ProfileID, f.DeviceID, f.Status, at, id, limit)
	if err != nil {
		return nil, fmt.Errorf("paging downloads: %w", err)
	}
	defer rows.Close()
	out := make([]AdminEntryRow, 0)
	for rows.Next() {
		var row AdminEntryRow
		var originNode *int
		joined := joinedRow{rows, []any{
			&row.Title, &row.MediaType, &row.SeasonNumber, &row.EpisodeNumber, &row.EpisodeTitle,
			&row.Username, &row.DeviceName, &row.RevokedAt, &row.RevokedReason, &originNode, &row.LocationName,
		}}
		if err := scanInto(joined, &row.Download); err != nil {
			return nil, fmt.Errorf("scanning download: %w", err)
		}
		if originNode != nil {
			row.Location = locationKeyForNode(*originNode)
			if *originNode == 0 {
				row.LocationName = serverLocationName
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
