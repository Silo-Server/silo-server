package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PreferencesRepository owns notification_preferences.
type PreferencesRepository struct {
	pool *pgxpool.Pool
}

// NewPreferencesRepository creates a PreferencesRepository.
func NewPreferencesRepository(pool *pgxpool.Pool) *PreferencesRepository {
	return &PreferencesRepository{pool: pool}
}

// Get returns the profile's preferences, defaulting missing rows to
// all-enabled.
func (r *PreferencesRepository) Get(ctx context.Context, profileID string) (Preferences, error) {
	prefs := DefaultPreferences(profileID)
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, updated_at
		FROM notification_preferences
		WHERE profile_id = $1`,
		profileID,
	).Scan(&prefs.Enabled, &prefs.NotifyFavorites, &prefs.NotifyWatchlist,
		&prefs.NotifyContinueWatching, &prefs.NotifyNextUp, &prefs.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return prefs, fmt.Errorf("get notification preferences: %w", err)
	}
	return prefs, nil
}

// GetMany returns preferences for the given profiles, defaulting missing rows
// to all-enabled. Used by the fanout worker.
func (r *PreferencesRepository) GetMany(ctx context.Context, tx pgx.Tx, profileIDs []string) (map[string]Preferences, error) {
	out := make(map[string]Preferences, len(profileIDs))
	for _, profileID := range profileIDs {
		out[profileID] = DefaultPreferences(profileID)
	}
	if len(profileIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT profile_id, enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, updated_at
		FROM notification_preferences
		WHERE profile_id = ANY($1)`,
		profileIDs)
	if err != nil {
		return nil, fmt.Errorf("get notification preferences batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var prefs Preferences
		if err := rows.Scan(&prefs.ProfileID, &prefs.Enabled, &prefs.NotifyFavorites,
			&prefs.NotifyWatchlist, &prefs.NotifyContinueWatching, &prefs.NotifyNextUp, &prefs.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan notification preferences: %w", err)
		}
		out[prefs.ProfileID] = prefs
	}
	return out, rows.Err()
}

// Upsert writes the profile's preferences. Idempotent.
func (r *PreferencesRepository) Upsert(ctx context.Context, prefs Preferences) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences
			(profile_id, enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (profile_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			notify_favorites = EXCLUDED.notify_favorites,
			notify_watchlist = EXCLUDED.notify_watchlist,
			notify_continue_watching = EXCLUDED.notify_continue_watching,
			notify_next_up = EXCLUDED.notify_next_up,
			updated_at = now()`,
		prefs.ProfileID, prefs.Enabled, prefs.NotifyFavorites,
		prefs.NotifyWatchlist, prefs.NotifyContinueWatching, prefs.NotifyNextUp)
	if err != nil {
		return fmt.Errorf("upsert notification preferences: %w", err)
	}
	return nil
}

// DeleteForProfile removes a deleted profile's preference row.
func (r *PreferencesRepository) DeleteForProfile(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_preferences WHERE profile_id = $1`, profileID)
	return err
}
