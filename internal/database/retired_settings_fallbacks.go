package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsmigrate"
)

// retiredSettingsFallbacksVersion sorts after every migration that shipped
// while the server still read the legacy disabled_library_ids and next_up_mode
// account settings at request time.
const retiredSettingsFallbacksVersion int64 = 20260923233933

// retiredSettingsFallbacksMigration writes the canonical profile rows that
// replace two read-time fallbacks. Until now, a profile with no
// ui.disabled_library_ids or ui.next_up_mode row fell back to the account's
// frozen legacy user_settings value on every request that resolved it. That is
// one extra query per profile-scoped request for hidden libraries alone, for
// a value that can no longer change: the legacy endpoint stopped accepting
// both keys at the settings cutover.
//
// The cutover backfill fanned these values out to the profiles that existed
// then. This finishes the job for every profile without a row, whether it was
// created later or reset to the default, so each one resolves the value the
// fallback gave it. A Go migration because the conversion is
// settingsmigrate.PlanRetiredFallback, shared with the SQLite backend.
//
// The legacy rows stay in user_settings, like every other legacy row the
// cutover converted. The table itself stays in use for other keys, so a later
// cleanup can delete these two retired keys' rows but not drop the table.
//
// The down migration keeps the written rows. Each one equals what the old
// binary's fallback derives from the untouched legacy row, so an old binary
// resolves the same answer with or without it, and removing rows by value
// could also remove a matching value a user chose afterwards.
func retiredSettingsFallbacksMigration() *goose.Migration {
	return goose.NewGoMigration(
		retiredSettingsFallbacksVersion,
		&goose.GoFunc{RunTx: materializeRetiredSettingsFallbacks},
		&goose.GoFunc{RunTx: func(context.Context, *sql.Tx) error { return nil }},
	)
}

// materializeRetiredSettingsFallbacks writes each legacy value to every
// profile on its account that has no row for the canonical key. ON CONFLICT
// DO NOTHING keeps any stored row, which the fallback never overrode, and makes
// a re-run a no-op.
func materializeRetiredSettingsFallbacks(ctx context.Context, tx *sql.Tx) error {
	contract, err := settingscontract.Load()
	if err != nil {
		return fmt.Errorf("loading settings contract: %w", err)
	}
	planner := settingsmigrate.New(contract, settingscontract.ObjectSchemas())

	type legacyRow struct {
		userID     int
		key, value string
	}
	var legacy []legacyRow
	if err := eachRow(ctx, tx,
		`SELECT user_id, key, value FROM user_settings WHERE key = ANY($1) ORDER BY user_id, key`,
		func(scan func(...any) error) error {
			var row legacyRow
			if err := scan(&row.userID, &row.key, &row.value); err != nil {
				return err
			}
			legacy = append(legacy, row)
			return nil
		}, settingsmigrate.RetiredFallbackKeys()); err != nil {
		return fmt.Errorf("reading legacy retired-fallback rows: %w", err)
	}

	for _, row := range legacy {
		planned, err := planner.PlanRetiredFallback(row.key, row.value)
		if err != nil {
			// The contract cannot store this value, so the cutover
			// backfill rejected it too and recorded it in
			// user_setting_migration_rejects.
			slog.WarnContext(ctx, "legacy setting has no canonical form; leaving it unconverted",
				"component", "database", "user_id", row.userID, "key", row.key, "error", err)
			continue
		}
		for _, value := range planned {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
SELECT p.user_id, $2, 'profile', p.id, $3::jsonb
  FROM user_profiles p
 WHERE p.user_id = $1
ON CONFLICT (user_id, profile_id, key) WHERE scope = 'profile' DO NOTHING`,
				row.userID, value.Key, string(value.Value),
			); err != nil {
				return fmt.Errorf("writing %s for user %d: %w", value.Key, row.userID, err)
			}
		}
	}
	return nil
}
