package userdb

import (
	"database/sql"
	"fmt"
	"time"
)

// GetJellycompatDisplayPrefs returns the stored DisplayPreferences blob for
// one (prefs id, client), or "" when nothing is stored.
func GetJellycompatDisplayPrefs(db *sql.DB, prefsID, client string) (string, error) {
	var value string
	err := db.QueryRow(
		"SELECT value FROM jellycompat_displayprefs WHERE prefs_id = ? AND client = ?",
		prefsID, client,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting display prefs %q/%q: %w", prefsID, client, err)
	}
	return value, nil
}

// SetJellycompatDisplayPrefs stores a DisplayPreferences blob verbatim,
// replacing any previous value for the same (prefs id, client).
func SetJellycompatDisplayPrefs(db *sql.DB, prefsID, client, value string) error {
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO jellycompat_displayprefs (prefs_id, client, value, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(prefs_id, client) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		prefsID, client, value, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting display prefs %q/%q: %w", prefsID, client, err)
	}
	return nil
}
