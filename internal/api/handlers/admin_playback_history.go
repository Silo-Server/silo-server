package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminPlaybackHistoryFilter narrows the finalized playback log the same way
// v1 GET /admin/playback-history does. Zero values mean "no filter".
type AdminPlaybackHistoryFilter struct {
	UserID      int
	ProfileID   string
	MediaItemID string
	// Completed is nil for every attempt, otherwise the exact flag.
	Completed *bool
}

// AdminPlaybackHistoryPageKey is the keyset position of the last row a page
// emitted: the log is ordered newest-ended first with the unique session id
// breaking ties, so the next page resumes strictly after it.
type AdminPlaybackHistoryPageKey struct {
	EndedAt   time.Time `json:"ended_at"`
	SessionID string    `json:"session_id"`
}

// AdminPlaybackHistoryRow is one finalized playback attempt as an
// administrator sees it. client_ip is stored on the row but is not part of
// this projection, matching v1.
type AdminPlaybackHistoryRow struct {
	SessionID       string
	UserID          int
	Username        string
	ProfileID       string
	ProfileName     string
	MediaItemID     string
	MediaFileID     int
	MediaTitle      string
	MediaType       string
	PlayMethod      string
	StartedAt       time.Time
	EndedAt         time.Time
	WatchedSeconds  float64
	DurationSeconds *float64
	Completed       bool
}

// AdminPlaybackHistoryPage is one keyset page plus whether more rows follow.
type AdminPlaybackHistoryPage struct {
	Items   []AdminPlaybackHistoryRow
	HasMore bool
}

// ListAdminPlaybackHistoryPage reads one consistent page of the finalized
// playback log, newest ended first. It is the seam v2 listAdminPlaybackHistory
// uses; v1 keeps its offset projection over the same table.
func (h *AdminHandler) ListAdminPlaybackHistoryPage(ctx context.Context, filter AdminPlaybackHistoryFilter, after *AdminPlaybackHistoryPageKey, limit int) (AdminPlaybackHistoryPage, error) {
	var out AdminPlaybackHistoryPage
	if h.pool == nil {
		return out, fmt.Errorf("playback history database unavailable")
	}
	if limit < 1 || limit > 200 {
		return out, fmt.Errorf("invalid playback history page limit")
	}
	var conditions []string
	var args []any
	add := func(sql string, value any) {
		args = append(args, value)
		conditions = append(conditions, sql+"$"+strconv.Itoa(len(args)))
	}
	if filter.UserID != 0 {
		add("h.user_id = ", filter.UserID)
	}
	if filter.ProfileID != "" {
		add("h.profile_id = ", filter.ProfileID)
	}
	if filter.MediaItemID != "" {
		add("h.media_item_id = ", filter.MediaItemID)
	}
	if filter.Completed != nil {
		add("h.completed = ", *filter.Completed)
	}
	if after != nil {
		args = append(args, after.EndedAt, after.SessionID)
		conditions = append(conditions, fmt.Sprintf("(h.ended_at, h.session_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	query := `
		SELECT
			h.session_id,
			h.user_id,
			COALESCE(u.username, ''),
			h.profile_id,
			COALESCE(NULLIF(h.profile_name, ''), h.profile_id),
			h.media_item_id,
			h.media_file_id,
			COALESCE(ep.title, mi.title, ''),
			COALESCE(CASE WHEN ep.content_id IS NOT NULL THEN 'episode' ELSE mi.type END, ''),
			h.play_method,
			h.started_at,
			h.ended_at,
			h.watched_seconds,
			h.duration_seconds,
			h.completed
		FROM admin_playback_history h
		LEFT JOIN users u ON u.id = h.user_id
		LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY h.ended_at DESC, h.session_id DESC LIMIT $" + strconv.Itoa(len(args))

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return out, err
	}
	out.Items = make([]AdminPlaybackHistoryRow, 0)
	for rows.Next() {
		var row AdminPlaybackHistoryRow
		if err = rows.Scan(
			&row.SessionID, &row.UserID, &row.Username, &row.ProfileID, &row.ProfileName,
			&row.MediaItemID, &row.MediaFileID, &row.MediaTitle, &row.MediaType, &row.PlayMethod,
			&row.StartedAt, &row.EndedAt, &row.WatchedSeconds, &row.DurationSeconds, &row.Completed,
		); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.HasMore = true
		out.Items = out.Items[:limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return AdminPlaybackHistoryPage{}, err
	}
	return out, nil
}
