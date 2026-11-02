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

// Get returns the preferences of the profile on userID's account, defaulting
// a missing row to all-enabled.
func (r *PreferencesRepository) Get(ctx context.Context, userID int, profileID string) (Preferences, error) {
	prefs := DefaultPreferences(userID, profileID)
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, updated_at
		FROM notification_preferences
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
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

// GetMany returns preferences for the given account profiles, defaulting
// missing rows to all-enabled. Used by the fanout worker.
func (r *PreferencesRepository) GetMany(ctx context.Context, tx pgx.Tx, profiles []profileKey) (map[profileKey]Preferences, error) {
	out := make(map[profileKey]Preferences, len(profiles))
	for _, profile := range profiles {
		out[profile] = DefaultPreferences(profile.userID, profile.profileID)
	}
	if len(profiles) == 0 {
		return out, nil
	}
	userIDs, profileIDs := profileKeyArrays(profiles)
	rows, err := tx.Query(ctx, `
		SELECT p.user_id, p.profile_id, p.enabled, p.notify_favorites, p.notify_watchlist,
		       p.notify_continue_watching, p.notify_next_up, p.updated_at
		FROM notification_preferences p
		JOIN unnest($1::integer[], $2::text[]) AS wanted(user_id, profile_id)
		  ON wanted.user_id = p.user_id AND wanted.profile_id = p.profile_id`,
		userIDs, profileIDs)
	if err != nil {
		return nil, fmt.Errorf("get notification preferences batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var prefs Preferences
		if err := rows.Scan(&prefs.UserID, &prefs.ProfileID, &prefs.Enabled, &prefs.NotifyFavorites,
			&prefs.NotifyWatchlist, &prefs.NotifyContinueWatching, &prefs.NotifyNextUp, &prefs.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan notification preferences: %w", err)
		}
		out[profileKey{prefs.UserID, prefs.ProfileID}] = prefs
	}
	return out, rows.Err()
}

// Upsert writes the preferences of the profile on prefs.UserID's account.
// Idempotent.
func (r *PreferencesRepository) Upsert(ctx context.Context, prefs Preferences) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences
			(user_id, profile_id, enabled, notify_favorites, notify_watchlist, notify_continue_watching, notify_next_up, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (user_id, profile_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			notify_favorites = EXCLUDED.notify_favorites,
			notify_watchlist = EXCLUDED.notify_watchlist,
			notify_continue_watching = EXCLUDED.notify_continue_watching,
			notify_next_up = EXCLUDED.notify_next_up,
			updated_at = now()`,
		prefs.UserID, prefs.ProfileID, prefs.Enabled, prefs.NotifyFavorites,
		prefs.NotifyWatchlist, prefs.NotifyContinueWatching, prefs.NotifyNextUp)
	if err != nil {
		return fmt.Errorf("upsert notification preferences: %w", err)
	}
	return nil
}

// DeleteForProfile removes the preference row of a deleted profile on
// userID's account.
func (r *PreferencesRepository) DeleteForProfile(ctx context.Context, userID int, profileID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_preferences WHERE user_id = $1 AND profile_id = $2`, userID, profileID)
	return err
}
