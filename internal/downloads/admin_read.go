package downloads

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Administrator reads of one account's managed device library. They only
// read: an administrator sees what each app registered, in every status, and
// never changes it here. Ephemeral (device-less) rows are not part of a
// device library and are excluded.

// AdminDownloadFilter narrows an account's managed rows to one profile or
// device. Empty fields do not filter.
type AdminDownloadFilter struct {
	ProfileID, DeviceID string
}

// AdminDownloadRow is one managed download with the catalog titles an
// administrator needs to recognize it. Title and MediaType describe
// ContentID (the movie, or the series of an episode) and are empty when the
// item left the catalog; the episode fields are nil unless EpisodeID
// resolves to a catalog episode.
type AdminDownloadRow struct {
	Download
	Title, MediaType            string
	SeasonNumber, EpisodeNumber *int
	EpisodeTitle                string
}

// AdminDownloadSummary totals an account's managed rows. Revoked rows count
// only in Revoked; InProgress is preparing, ready or downloading.
type AdminDownloadSummary struct {
	Total           int
	Completed       int
	InProgress      int
	Failed          int
	Revoked         int
	TotalBytes      int64
	Devices         int
	MonitoredSeries int
}

// AdminSubscriptionRow is one series monitor with the series title and the
// state of the monitored episodes on its device: OnDevice counts completed
// rows, InProgress preparing, ready or downloading rows, and RemovedEpisodes
// the episodes the user deleted from the monitored series.
type AdminSubscriptionRow struct {
	Subscription
	SeriesTitle                           string
	OnDevice, InProgress, RemovedEpisodes int
}

// adminPageMaxLimit is the largest page an administrator read fetches: the
// v2 maximum of 200 plus the one row that tells whether more follow.
const adminPageMaxLimit = 201

// qualifiedColumns prefixes each column of a column list with a table alias.
func qualifiedColumns(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

// joinedRow hands a row's leading columns to a base scanner (scanInto,
// scanSubscriptionInto) and scans the trailing joined columns into extra.
type joinedRow struct {
	pgx.Row
	extra []any
}

func (r joinedRow) Scan(dest ...any) error {
	return r.Row.Scan(append(dest, r.extra...)...)
}

func adminPagePosition(after *RegistryPosition) (*time.Time, string) {
	if after == nil {
		return nil, ""
	}
	return &after.CreatedAt, after.ID
}

// AdminListUserDownloadsPage lists an account's managed downloads newest
// first across every profile and device unless the filter narrows it.
func (s *Service) AdminListUserDownloadsPage(ctx context.Context, userID int, f AdminDownloadFilter, after *RegistryPosition, limit int) ([]AdminDownloadRow, error) {
	return s.repo.ListUserManagedPage(ctx, userID, f, after, limit)
}

// ListUserManagedPage reads one keyset page of an account's managed rows in
// every status, ordered by creation time then id, both descending.
func (r *Repository) ListUserManagedPage(ctx context.Context, userID int, f AdminDownloadFilter, after *RegistryPosition, limit int) ([]AdminDownloadRow, error) {
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("download page limit must be 1 to %d", adminPageMaxLimit)
	}
	at, id := adminPagePosition(after)
	rows, err := r.pool.Query(ctx, `SELECT `+qualifiedColumns(downloadColumns, "d")+`,
		COALESCE(mi.title, ''), COALESCE(mi.type, ''), ep.season_number, ep.episode_number, COALESCE(ep.title, '')
	FROM downloads d
	LEFT JOIN media_items mi ON mi.content_id = d.content_id
	LEFT JOIN episodes ep ON ep.content_id = d.episode_id
	WHERE d.user_id = $1 AND d.device_id IS NOT NULL
		AND ($2 = '' OR d.profile_id = $2)
		AND ($3 = '' OR d.device_id = $3)
		AND ($4::timestamptz IS NULL OR (d.created_at, d.id) < ($4, $5))
	ORDER BY d.created_at DESC, d.id DESC
	LIMIT $6`, userID, f.ProfileID, f.DeviceID, at, id, limit)
	if err != nil {
		return nil, fmt.Errorf("paging account downloads: %w", err)
	}
	defer rows.Close()
	out := make([]AdminDownloadRow, 0)
	for rows.Next() {
		var row AdminDownloadRow
		joined := joinedRow{rows, []any{&row.Title, &row.MediaType, &row.SeasonNumber, &row.EpisodeNumber, &row.EpisodeTitle}}
		if err := scanInto(joined, &row.Download); err != nil {
			return nil, fmt.Errorf("scanning account download: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AdminUserDownloadSummary totals an account's managed rows and counts its
// active series monitors.
func (s *Service) AdminUserDownloadSummary(ctx context.Context, userID int) (AdminDownloadSummary, error) {
	return s.repo.UserManagedSummary(ctx, userID)
}

// UserManagedSummary is one aggregate over the account's managed rows plus
// its active monitors. The download_subscriptions table exists whether or not
// monitoring is wired, so the count needs no subscription repository.
func (r *Repository) UserManagedSummary(ctx context.Context, userID int) (AdminDownloadSummary, error) {
	var out AdminDownloadSummary
	err := r.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status <> $2),
		count(*) FILTER (WHERE status = $3),
		count(*) FILTER (WHERE status IN ($4, $5, $6)),
		count(*) FILTER (WHERE status = $7),
		count(*) FILTER (WHERE status = $2),
		COALESCE(sum(file_size) FILTER (WHERE status <> $2), 0)::bigint,
		count(DISTINCT device_id) FILTER (WHERE status <> $2),
		(SELECT count(*) FROM download_subscriptions s WHERE s.user_id = $1 AND s.active)
	FROM downloads
	WHERE user_id = $1 AND device_id IS NOT NULL`,
		userID, StatusRevoked, StatusCompleted, StatusPreparing, StatusReady, StatusDownloading, StatusFailed,
	).Scan(&out.Total, &out.Completed, &out.InProgress, &out.Failed, &out.Revoked, &out.TotalBytes, &out.Devices, &out.MonitoredSeries)
	if err != nil {
		return AdminDownloadSummary{}, fmt.Errorf("summarizing account downloads: %w", err)
	}
	return out, nil
}

// AdminListUserSubscriptionsPage lists an account's series monitors newest
// first across every profile and device unless the filter narrows it.
func (s *Service) AdminListUserSubscriptionsPage(ctx context.Context, userID int, f AdminDownloadFilter, after *RegistryPosition, limit int) ([]AdminSubscriptionRow, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	return s.subRepo.ListUserPage(ctx, userID, f, after, limit)
}

// ListUserPage reads one keyset page of an account's monitors, ordered by
// creation time then id, both descending, with the per-monitor counts.
func (r *SubscriptionRepository) ListUserPage(ctx context.Context, userID int, f AdminDownloadFilter, after *RegistryPosition, limit int) ([]AdminSubscriptionRow, error) {
	if limit < 1 || limit > adminPageMaxLimit {
		return nil, fmt.Errorf("subscription page limit must be 1 to %d", adminPageMaxLimit)
	}
	at, id := adminPagePosition(after)
	rows, err := r.pool.Query(ctx, `SELECT `+qualifiedColumns(subscriptionColumns, "s")+`,
		COALESCE(mi.title, ''),
		(SELECT count(*) FROM downloads d
			WHERE d.user_id = s.user_id AND d.profile_id = s.profile_id AND d.device_id = s.device_id
				AND d.content_id = s.series_id AND d.status = $7),
		(SELECT count(*) FROM downloads d
			WHERE d.user_id = s.user_id AND d.profile_id = s.profile_id AND d.device_id = s.device_id
				AND d.content_id = s.series_id AND d.status IN ($8, $9, $10)),
		(SELECT count(*) FROM download_subscription_exclusions x WHERE x.subscription_id = s.id)
	FROM download_subscriptions s
	LEFT JOIN media_items mi ON mi.content_id = s.series_id
	WHERE s.user_id = $1
		AND ($2 = '' OR s.profile_id = $2)
		AND ($3 = '' OR s.device_id = $3)
		AND ($4::timestamptz IS NULL OR (s.created_at, s.id) < ($4, $5))
	ORDER BY s.created_at DESC, s.id DESC
	LIMIT $6`, userID, f.ProfileID, f.DeviceID, at, id, limit, StatusCompleted, StatusPreparing, StatusReady, StatusDownloading)
	if err != nil {
		return nil, fmt.Errorf("paging account subscriptions: %w", err)
	}
	defer rows.Close()
	out := make([]AdminSubscriptionRow, 0)
	for rows.Next() {
		var row AdminSubscriptionRow
		joined := joinedRow{rows, []any{&row.SeriesTitle, &row.OnDevice, &row.InProgress, &row.RemovedEpisodes}}
		if err := scanSubscriptionInto(joined, &row.Subscription); err != nil {
			return nil, fmt.Errorf("scanning account subscription: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
