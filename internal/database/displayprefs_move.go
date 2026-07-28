package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/jellycompat/displayprefs"
)

// displayPrefsMoveVersion sorts immediately after
// 20260728132326_jellycompat_displayprefs.sql, which creates the table this
// fills — the same pairing user_setting_values uses with the settings
// backfill.
const displayPrefsMoveVersion int64 = 20260728132327

// displayPrefsMoveMigration rehomes the Jellyfin DisplayPreferences blobs from
// user_settings (jellycompat:* keys) into jellycompat_displayprefs, removing
// the last non-settings tenant of the legacy key/value table.
//
// A Go migration rather than SQL because the key parsing and row
// classification live in internal/jellycompat/displayprefs, shared with the
// per-user SQLite backend so the two cannot diverge; SQL could not use those
// rules without duplicating them. Values are copied byte-for-byte — the blobs
// are opaque Jellyfin client JSON and reinterpreting them is not this
// migration's business.
//
// RunTx, so a store comes out fully moved or untouched. Re-running is
// harmless: the up deletes every jellycompat:* source row, so a second pass
// finds nothing, and ON CONFLICT DO NOTHING keeps even a mixed state (a backup
// restored over a migrated database) from failing or clobbering the
// already-moved value.
func displayPrefsMoveMigration() *goose.Migration {
	return goose.NewGoMigration(
		displayPrefsMoveVersion,
		&goose.GoFunc{RunTx: moveDisplayPrefs},
		&goose.GoFunc{RunTx: unmoveDisplayPrefs},
	)
}

// moveDisplayPrefs copies every user's jellycompat rows over, then removes
// them from user_settings. A jellycompat:* row that does not parse as a
// DisplayPreferences key could only have been written through the legacy
// settings API's unknown-key carve-out, which this cutover removes; those rows
// are recorded in user_setting_migration_rejects for operator inspection
// rather than silently deleted.
func moveDisplayPrefs(ctx context.Context, tx *sql.Tx) error {
	type legacyRow struct {
		userID     int
		key, value string
	}
	var legacy []legacyRow
	if err := eachRow(ctx, tx,
		`SELECT user_id, key, value FROM user_settings WHERE key LIKE $1`,
		func(scan func(...any) error) error {
			var row legacyRow
			if err := scan(&row.userID, &row.key, &row.value); err != nil {
				return err
			}
			legacy = append(legacy, row)
			return nil
		}, displayprefs.LegacyKeyPattern()); err != nil {
		return fmt.Errorf("reading jellycompat rows from user_settings: %w", err)
	}

	for _, row := range legacy {
		blob, reject := displayprefs.PlanLegacyRow(row.key, row.value)
		if blob != nil {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO jellycompat_displayprefs (user_id, prefs_id, client, value)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, prefs_id, client) DO NOTHING`,
				row.userID, blob.PrefsID, blob.Client, blob.Value,
			); err != nil {
				return fmt.Errorf("moving display prefs %q for user %d: %w",
					row.key, row.userID, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO user_setting_migration_rejects
    (user_id, source_table, source_key, identity, value, reason)
VALUES ($1, 'user_settings', $2, '{"scope":"account"}'::jsonb, $3, $4)`,
			row.userID, reject.Key, reject.Value, reject.Reason,
		); err != nil {
			return fmt.Errorf("recording displayprefs reject %q for user %d: %w",
				reject.Key, row.userID, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_settings WHERE key LIKE $1`,
		displayprefs.LegacyKeyPattern(),
	); err != nil {
		return fmt.Errorf("deleting jellycompat rows from user_settings: %w", err)
	}
	return nil
}

// unmoveDisplayPrefs is the inverse: unlike the settings backfill, the up
// migration deletes its source rows, so rolling back has to write them back.
// Moved blobs reconstruct their legacy key through the shared rules; rejected
// rows restore from the audit table. Both sides then discard what the up
// migration wrote, so the follow-up table drop removes nothing that is not
// already back in user_settings.
func unmoveDisplayPrefs(ctx context.Context, tx *sql.Tx) error {
	type movedRow struct {
		userID                 int
		prefsID, client, value string
	}
	var moved []movedRow
	if err := eachRow(ctx, tx,
		`SELECT user_id, prefs_id, client, value FROM jellycompat_displayprefs`,
		func(scan func(...any) error) error {
			var row movedRow
			if err := scan(&row.userID, &row.prefsID, &row.client, &row.value); err != nil {
				return err
			}
			moved = append(moved, row)
			return nil
		}); err != nil {
		return fmt.Errorf("reading jellycompat_displayprefs: %w", err)
	}
	for _, row := range moved {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO user_settings (user_id, key, value)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, key) DO NOTHING`,
			row.userID, displayprefs.LegacyKey(row.prefsID, row.client), row.value,
		); err != nil {
			return fmt.Errorf("restoring display prefs %q/%q for user %d: %w",
				row.prefsID, row.client, row.userID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO user_settings (user_id, key, value)
SELECT user_id, source_key, COALESCE(value, '')
  FROM user_setting_migration_rejects
 WHERE source_table = 'user_settings' AND source_key LIKE $1
ON CONFLICT (user_id, key) DO NOTHING`,
		displayprefs.LegacyKeyPattern(),
	); err != nil {
		return fmt.Errorf("restoring rejected jellycompat rows: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM jellycompat_displayprefs`); err != nil {
		return fmt.Errorf("clearing jellycompat_displayprefs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM user_setting_migration_rejects
 WHERE source_table = 'user_settings' AND source_key LIKE $1`,
		displayprefs.LegacyKeyPattern(),
	); err != nil {
		return fmt.Errorf("clearing displayprefs rejects: %w", err)
	}
	return nil
}
