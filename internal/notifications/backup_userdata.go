package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// BackupData is a profile's notification preferences and muted series
// (userdata.Notifications). Importing it overwrites the preferences when
// they differ and adds missing mutes. Email and Discord delivery are not
// carried: an address must be verified on this server, and a Discord link is
// an external sign-in.
type BackupData struct{}

func (BackupData) ID() string { return userdata.Notifications }
func (BackupData) Tables() []string {
	return []string{"notification_preferences", "profile_series_notification_mutes"}
}

type notificationsDocument struct {
	Preferences *notificationPreferences `json:"preferences"`
	Muted       []mutedSeries            `json:"muted_series"`
}

type notificationPreferences struct {
	Enabled          bool `json:"enabled"`
	Favorites        bool `json:"notify_favorites"`
	Watchlist        bool `json:"notify_watchlist"`
	ContinueWatching bool `json:"notify_continue_watching"`
	NextUp           bool `json:"notify_next_up"`
}

type mutedSeries struct {
	Series  string    `json:"series"`
	MutedAt time.Time `json:"muted_at"`
}

func (BackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (userdata.Export, error) {
	doc := notificationsDocument{Muted: []mutedSeries{}}
	var p notificationPreferences
	err := tx.QueryRow(ctx, `
		SELECT enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up
		FROM notification_preferences WHERE user_id = $1 AND profile_id = $2`, owner.UserID, owner.ProfileID).Scan(
		&p.Enabled, &p.Favorites, &p.Watchlist, &p.ContinueWatching, &p.NextUp)
	switch {
	case err == nil:
		doc.Preferences = &p
	case !errors.Is(err, pgx.ErrNoRows):
		return userdata.Export{}, fmt.Errorf("reading notification preferences: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT series_id, muted_at FROM profile_series_notification_mutes WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`,
		owner.UserID, owner.ProfileID)
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading muted series: %w", err)
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var m mutedSeries
		if err := rows.Scan(&m.Series, &m.MutedAt); err != nil {
			return userdata.Export{}, err
		}
		doc.Muted = append(doc.Muted, m)
		items = append(items, m.Series)
	}
	if err := rows.Err(); err != nil {
		return userdata.Export{}, err
	}
	data, err := json.Marshal(doc)
	count := len(doc.Muted)
	if doc.Preferences != nil {
		count++
	}
	return userdata.Export{Data: data, Count: count, Items: items}, err
}

func decodeNotifications(data json.RawMessage) (notificationsDocument, userdata.Summary, error) {
	var doc notificationsDocument
	var sum userdata.Summary
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, sum, err
	}
	if err := userdata.CheckEntries("muted series", len(doc.Muted)); err != nil {
		return doc, sum, err
	}
	for _, m := range doc.Muted {
		if !userdata.ValidContentID(m.Series) {
			return doc, sum, fmt.Errorf("muted series %q is not valid", m.Series)
		}
		sum.Items = append(sum.Items, m.Series)
	}
	sum.Count = len(doc.Muted)
	if doc.Preferences != nil {
		sum.Count++
	}
	return doc, sum, nil
}

func (BackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodeNotifications(data)
	return sum, err
}

func (BackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeNotifications(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	if p := doc.Preferences; p != nil {
		var inserted bool
		err := tx.QueryRow(ctx, `
			INSERT INTO notification_preferences (user_id, profile_id, enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, profile_id) DO UPDATE SET enabled = EXCLUDED.enabled, notify_favorites = EXCLUDED.notify_favorites,
				notify_watchlist = EXCLUDED.notify_watchlist, notify_continue_watching = EXCLUDED.notify_continue_watching,
				notify_next_up = EXCLUDED.notify_next_up, updated_at = now()
			WHERE (notification_preferences.enabled, notification_preferences.notify_favorites, notification_preferences.notify_watchlist,
				notification_preferences.notify_continue_watching, notification_preferences.notify_next_up)
				IS DISTINCT FROM (EXCLUDED.enabled, EXCLUDED.notify_favorites, EXCLUDED.notify_watchlist,
				EXCLUDED.notify_continue_watching, EXCLUDED.notify_next_up)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, p.Enabled, p.Favorites, p.Watchlist, p.ContinueWatching, p.NextUp).Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Unchanged++
		case err != nil:
			return res, fmt.Errorf("restoring notification preferences: %w", err)
		case inserted:
			res.Added++
		default:
			res.Updated++
		}
	}
	for _, m := range doc.Muted {
		series, ok, err := target.Items.Resolve(ctx, m.Series)
		if err != nil {
			return res, err
		}
		if !ok {
			res.LeftOut++
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO profile_series_notification_mutes (user_id, profile_id, series_id, muted_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, target.UserID, target.ProfileID, series, m.MutedAt)
		if err != nil {
			return res, fmt.Errorf("restoring a muted series: %w", err)
		}
		if tag.RowsAffected() > 0 {
			res.Added++
		} else {
			res.Unchanged++
		}
	}
	return res, nil
}

// Pending counts what Import would do: the preferences when they are not
// here or differ, and mutes the profile lacks.
func (BackupData) Pending(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodeNotifications(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	if p := doc.Preferences; p != nil {
		var found, differs bool
		err := tx.QueryRow(ctx, `
			SELECT true, (enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up) IS DISTINCT FROM ($3, $4, $5, $6, $7)
			FROM notification_preferences WHERE user_id = $1 AND profile_id = $2`,
			target.UserID, target.ProfileID, p.Enabled, p.Favorites, p.Watchlist, p.ContinueWatching, p.NextUp).Scan(&found, &differs)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Added++
		case err != nil:
			return res, fmt.Errorf("comparing notification preferences: %w", err)
		case differs:
			res.Updated++
		default:
			res.Unchanged++
		}
	}
	ids := make([]string, 0, len(doc.Muted))
	for _, m := range doc.Muted {
		ids = append(ids, m.Series)
	}
	if err := userdata.PendingMissing(ctx, tx, target, &res, `
		SELECT x.series FROM unnest($3::text[]) AS x(series)
		WHERE NOT EXISTS (SELECT 1 FROM profile_series_notification_mutes m WHERE m.user_id = $1 AND m.profile_id = $2 AND m.series_id = x.series)`,
		ids); err != nil {
		return res, fmt.Errorf("comparing muted series: %w", err)
	}
	return res, nil
}
