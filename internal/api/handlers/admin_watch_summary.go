package handlers

import (
	"context"
	"fmt"
	"time"
)

// AdminWatchSummaryQuery selects one account's finalized plays that ended in
// the last Days days before Now, optionally for one profile.
type AdminWatchSummaryQuery struct {
	UserID, Days int
	ProfileID    string
	Now          time.Time
}

// AdminWatchSummaryView totals the finalized playback log over a window.
// LastPlayedAt is nil when no attempt ended in the window.
type AdminWatchSummaryView struct {
	Since                 time.Time
	Plays, CompletedPlays int
	WatchedSeconds        float64
	LastPlayedAt          *time.Time
}

// ReadAdminWatchSummary counts an account's finalized playback attempts that
// ended at or after Now minus Days. It reads the same log as
// ListAdminPlaybackHistoryPage, so a list with the same window and profile
// pages through exactly these attempts.
func (h *AdminHandler) ReadAdminWatchSummary(ctx context.Context, q AdminWatchSummaryQuery) (AdminWatchSummaryView, error) {
	var out AdminWatchSummaryView
	if h.pool == nil {
		return out, fmt.Errorf("playback history database unavailable")
	}
	if q.UserID <= 0 || q.Days < 1 || q.Days > 365 || q.Now.IsZero() {
		return out, fmt.Errorf("invalid watch summary query")
	}
	out.Since = q.Now.UTC().AddDate(0, 0, -q.Days)
	err := h.pool.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE completed),
			COALESCE(sum(watched_seconds), 0),
			max(ended_at)
		FROM admin_playback_history
		WHERE user_id = $1 AND ended_at >= $2 AND ($3 = '' OR profile_id = $3)`,
		q.UserID, out.Since, q.ProfileID,
	).Scan(&out.Plays, &out.CompletedPlays, &out.WatchedSeconds, &out.LastPlayedAt)
	if err != nil {
		return AdminWatchSummaryView{}, err
	}
	return out, nil
}
