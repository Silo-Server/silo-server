package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// InboxCutoff captures the newest committed delivery currently visible to this
// profile. An empty cursor describes an empty inbox and remains a no-op cutoff.
func (r *DeliveryRepository) InboxCutoff(ctx context.Context, profileID string) (Cursor, error) {
	var cursor Cursor
	err := r.pool.QueryRow(ctx, `SELECT created_at,id FROM notification_deliveries WHERE profile_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, profileID).Scan(&cursor.CreatedAt, &cursor.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cursor{}, nil
	}
	return cursor, err
}

func (r *DeliveryRepository) ListInboxWindow(ctx context.Context, profileID string, unread bool, limit int, before *Cursor, through Cursor) ([]DeliveryRow, bool, error) {
	limit = max(1, min(limit, 200))
	if through.ID == "" {
		return []DeliveryRow{}, false, nil
	}
	var beforeTime any
	beforeID := ""
	if before != nil {
		beforeTime = before.CreatedAt
		beforeID = before.ID
	}
	rows, err := r.pool.Query(ctx, deliveryRowSelect+` WHERE d.profile_id=$1 AND (NOT $2 OR d.read_at IS NULL) AND (d.created_at,d.id)<=($3,$4) AND ($5::timestamptz IS NULL OR (d.created_at,d.id)<($5,$6)) ORDER BY d.created_at DESC,d.id DESC LIMIT $7`, profileID, unread, through.CreatedAt, through.ID, beforeTime, beforeID, limit+1)
	if err != nil {
		return nil, false, err
	}
	result, err := scanDeliveryRows(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, nil
}

// MarkReadThrough never expands the target when the same command is retried.
func (r *DeliveryRepository) MarkReadThrough(ctx context.Context, profileID string, through Cursor) (int64, error) {
	if through.ID == "" {
		return 0, nil
	}
	tag, err := r.pool.Exec(ctx, `UPDATE notification_deliveries SET read_at=now() WHERE profile_id=$1 AND read_at IS NULL AND (created_at,id)<=($2,$3)`, profileID, through.CreatedAt, through.ID)
	if err != nil {
		return 0, fmt.Errorf("mark notification cutoff read: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PreferencePatch preserves omitted fields without a read/whole-row-write race.
type PreferencePatch struct {
	Enabled                *bool
	NotifyFavorites        *bool
	NotifyWatchlist        *bool
	NotifyContinueWatching *bool
	NotifyNextUp           *bool
}

func (r *PreferencesRepository) Patch(ctx context.Context, profileID string, p PreferencePatch) (Preferences, error) {
	prefs := DefaultPreferences(profileID)
	err := r.pool.QueryRow(ctx, `INSERT INTO notification_preferences(profile_id,enabled,notify_favorites,notify_watchlist,notify_continue_watching,notify_next_up,updated_at) VALUES($1,COALESCE($2,true),COALESCE($3,true),COALESCE($4,true),COALESCE($5,true),COALESCE($6,true),now()) ON CONFLICT(profile_id) DO UPDATE SET enabled=COALESCE($2,notification_preferences.enabled),notify_favorites=COALESCE($3,notification_preferences.notify_favorites),notify_watchlist=COALESCE($4,notification_preferences.notify_watchlist),notify_continue_watching=COALESCE($5,notification_preferences.notify_continue_watching),notify_next_up=COALESCE($6,notification_preferences.notify_next_up),updated_at=now() RETURNING enabled,notify_favorites,notify_watchlist,notify_continue_watching,notify_next_up,updated_at`, profileID, p.Enabled, p.NotifyFavorites, p.NotifyWatchlist, p.NotifyContinueWatching, p.NotifyNextUp).Scan(&prefs.Enabled, &prefs.NotifyFavorites, &prefs.NotifyWatchlist, &prefs.NotifyContinueWatching, &prefs.NotifyNextUp, &prefs.UpdatedAt)
	return prefs, err
}
