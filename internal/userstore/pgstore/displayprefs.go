package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetJellycompatDisplayPrefs returns the stored DisplayPreferences blob for
// one (prefs id, client), or "" when nothing is stored.
func (s *PostgresUserStore) GetJellycompatDisplayPrefs(ctx context.Context, prefsID, client string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM jellycompat_displayprefs
		 WHERE user_id = $1 AND prefs_id = $2 AND client = $3`,
		s.userID, prefsID, client,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting display prefs %q/%q: %w", prefsID, client, err)
	}
	return value, nil
}

// SetJellycompatDisplayPrefs stores a DisplayPreferences blob verbatim,
// replacing any previous value for the same (prefs id, client).
func (s *PostgresUserStore) SetJellycompatDisplayPrefs(ctx context.Context, prefsID, client, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jellycompat_displayprefs (user_id, prefs_id, client, value, updated_at)
		 VALUES ($1, $2, $3, $4, NOW())
		 ON CONFLICT(user_id, prefs_id, client) DO UPDATE SET
			value = excluded.value,
			updated_at = NOW()`,
		s.userID, prefsID, client, value,
	)
	if err != nil {
		return fmt.Errorf("setting display prefs %q/%q: %w", prefsID, client, err)
	}
	return nil
}
