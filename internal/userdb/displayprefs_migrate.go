package userdb

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/jellycompat/displayprefs"
)

// moveJellycompatDisplayPrefs is the one-time move of Jellyfin
// DisplayPreferences blobs out of user_settings and into the dedicated
// jellycompat_displayprefs table. Both tables exist by the time this runs:
// InitSchema creates them with IF NOT EXISTS on every open, before migrations.
//
// The key-parsing and classification rules live in
// internal/jellycompat/displayprefs so this backend and Postgres cannot
// disagree about them; everything here is reading rows, handing them over, and
// writing what comes back. Values are copied byte-for-byte — the blobs are
// opaque Jellyfin client JSON and reinterpreting them is not this migration's
// business.
//
// It runs inside the caller's transaction, so the store comes out fully moved
// or untouched. Re-running is harmless: the first run deletes every
// jellycompat:* source row, so a second pass finds nothing, and ON CONFLICT DO
// NOTHING keeps even a mixed state (a backup restored over a migrated
// database) from failing or clobbering the already-moved value.
//
// A jellycompat:* row that does not parse as a DisplayPreferences key could
// only have been written through the legacy settings API's unknown-key
// carve-out, which this cutover removes. Those rows are recorded in
// user_setting_migration_rejects for operator inspection rather than silently
// deleted.
func moveJellycompatDisplayPrefs(tx *sql.Tx) error {
	type legacyRow struct{ key, value string }
	var legacy []legacyRow

	rows, err := tx.Query(
		`SELECT key, value FROM user_settings WHERE key LIKE ?`,
		displayprefs.LegacyKeyPattern(),
	)
	if err != nil {
		return fmt.Errorf("reading jellycompat rows from user_settings: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.key, &row.value); err != nil {
			return fmt.Errorf("scanning jellycompat row: %w", err)
		}
		legacy = append(legacy, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating jellycompat rows: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range legacy {
		blob, reject := displayprefs.PlanLegacyRow(row.key, row.value)
		if blob != nil {
			if _, err := tx.Exec(`
INSERT INTO jellycompat_displayprefs (prefs_id, client, value, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(prefs_id, client) DO NOTHING`,
				blob.PrefsID, blob.Client, blob.Value, now,
			); err != nil {
				return fmt.Errorf("moving display prefs %q: %w", row.key, err)
			}
			continue
		}
		if _, err := tx.Exec(`
INSERT INTO user_setting_migration_rejects
    (source_table, source_key, identity, value, reason, recorded_at)
VALUES ('user_settings', ?, '{"scope":"account"}', ?, ?, ?)`,
			reject.Key, reject.Value, reject.Reason, now,
		); err != nil {
			return fmt.Errorf("recording displayprefs reject for %q: %w", reject.Key, err)
		}
	}

	if _, err := tx.Exec(
		`DELETE FROM user_settings WHERE key LIKE ?`,
		displayprefs.LegacyKeyPattern(),
	); err != nil {
		return fmt.Errorf("deleting jellycompat rows from user_settings: %w", err)
	}
	return nil
}
