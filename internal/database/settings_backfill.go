package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsmigrate"
)

// settingsBackfillVersion is the timestamp version this backfill occupies. It
// sorts immediately after 20260727010621_user_setting_values.sql, which creates
// the tables this fills.
const settingsBackfillVersion int64 = 20260727010622

// settingsBackfillMigration is the one-time conversion of legacy settings
// storage into user_setting_values.
//
// A Go migration rather than SQL because the conversion is not expressible in
// SQL without duplicating the contract: every value has to be validated against
// its own definition and re-encoded as typed JSON, and a legacy quality string
// decomposes into two rows. Those rules live in internal/settingsmigrate so
// this and the SQLite backend cannot disagree; this file reads rows, hands them
// over, and writes what comes back.
//
// RunTx, so the whole backfill lands in goose's transaction — a partial
// migration is the one state neither an operator's backup nor a rollback
// covers.
func settingsBackfillMigration() *goose.Migration {
	return goose.NewGoMigration(
		settingsBackfillVersion,
		&goose.GoFunc{RunTx: backfillSettingValues},
		&goose.GoFunc{RunTx: rollbackSettingValues},
	)
}

// backfillSettingValues converts every user's legacy settings.
func backfillSettingValues(ctx context.Context, tx *sql.Tx) error {
	contract, err := settingscontract.Load()
	if err != nil {
		return fmt.Errorf("loading settings contract: %w", err)
	}
	planner := settingsmigrate.New(contract, settingscontract.ObjectSchemas())

	userIDs, err := settingsBackfillUserIDs(ctx, tx)
	if err != nil {
		return err
	}

	for _, userID := range userIDs {
		input, err := readLegacySettingsForUser(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("reading legacy settings for user %d: %w", userID, err)
		}
		result := planner.Plan(input)

		for _, row := range result.Rows {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO user_setting_values
    (user_id, key, scope, profile_id, device_id, library_id, series_id, value)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
				userID, row.Key, string(row.Scope),
				nullText(row.ProfileID), nullText(row.DeviceID),
				nullInt(row.LibraryID), nullText(row.SeriesID),
				string(row.Value),
			); err != nil {
				return fmt.Errorf("writing %s at %s for user %d: %w",
					row.Key, row.Scope, userID, err)
			}
		}

		for _, reject := range result.Rejects {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO user_setting_migration_rejects
    (user_id, source_table, source_key, identity, value, reason)
VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
				userID, reject.SourceTable, reject.SourceKey,
				string(reject.Identity), reject.Value, reject.Reason,
			); err != nil {
				return fmt.Errorf("recording reject %s for user %d: %w",
					reject.SourceKey, userID, err)
			}
		}
	}

	return nil
}

// rollbackSettingValues empties the canonical tables.
//
// The legacy tables are never modified by the up migration, so undoing it is
// simply discarding what was derived from them. This is what makes the cutover
// reversible before the follow-up migration drops the legacy columns.
func rollbackSettingValues(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{
		"user_setting_values",
		"user_setting_migration_rejects",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}
	return nil
}

// settingsBackfillUserIDs returns every user with something to migrate.
//
// A user with no settings and no profiles produces nothing, so they are skipped
// rather than queried five times each.
func settingsBackfillUserIDs(ctx context.Context, tx *sql.Tx) ([]int, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM users
 WHERE EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = users.id)
    OR EXISTS (SELECT 1 FROM user_settings s WHERE s.user_id = users.id)
 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning user id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// readLegacySettingsForUser gathers one user's legacy rows.
func readLegacySettingsForUser(
	ctx context.Context, tx *sql.Tx, userID int,
) (settingsmigrate.Input, error) {
	var input settingsmigrate.Input
	// Non-nil records that the profile list was loaded even when the account
	// has none. The planner uses nil only for callers that could not load
	// profiles; an empty loaded list must reject every profile-scoped orphan.
	input.Profiles = make([]settingsmigrate.LegacyProfile, 0)

	// Profiles. preferred_metadata_language exists only in this schema — the
	// SQLite profiles table never had the column — so this is the sole source
	// for catalog.metadata_language.
	if err := eachRow(ctx, tx, `
SELECT id, quality_preference, language, subtitle_language, subtitle_mode,
       show_forced_subtitles, preferred_metadata_language,
       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview
  FROM user_profiles WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var profile settingsmigrate.LegacyProfile
			var quality, language, subtitle, mode, metadata sql.NullString
			var forced, skipIntro, skipCredits, skipRecap, nextPreview sql.NullBool
			if err := scan(&profile.ID, &quality, &language, &subtitle,
				&mode, &forced, &metadata,
				&skipIntro, &skipCredits, &skipRecap, &nextPreview); err != nil {
				return err
			}
			profile.QualityPreference = nullableString(quality)
			profile.Language = nullableString(language)
			profile.SubtitleLanguage = nullableString(subtitle)
			profile.SubtitleMode = nullableString(mode)
			profile.ShowForcedSubtitles = nullableBool(forced)
			profile.PreferredMetadataLanguage = nullableString(metadata)
			profile.AutoSkipIntro = nullableBool(skipIntro)
			profile.AutoSkipCredits = nullableBool(skipCredits)
			profile.AutoSkipRecap = nullableBool(skipRecap)
			profile.AutoPlayNextPreview = nullableBool(nextPreview)
			input.Profiles = append(input.Profiles, profile)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_profiles: %w", err)
	}

	if err := eachRow(ctx, tx,
		`SELECT key, value FROM user_settings WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacySetting
			if err := scan(&row.Key, &row.Value); err != nil {
				return err
			}
			input.Settings = append(input.Settings, row)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_settings: %w", err)
	}

	if err := eachRow(ctx, tx, `
SELECT profile_id, device_id, key, value
  FROM user_device_settings WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacyDeviceSetting
			if err := scan(&row.ProfileID, &row.DeviceID, &row.Key, &row.Value); err != nil {
				return err
			}
			input.DeviceSettings = append(input.DeviceSettings, row)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_device_settings: %w", err)
	}

	// Subtitle and audio preferences are keyed alike, so they merge into one
	// per-series record; converting them independently would produce two rows
	// racing for the same identity.
	bySeries := map[[2]string]*settingsmigrate.LegacySeriesPreference{}
	seriesRecord := func(profileID, seriesID string) *settingsmigrate.LegacySeriesPreference {
		key := [2]string{profileID, seriesID}
		if existing, ok := bySeries[key]; ok {
			return existing
		}
		record := &settingsmigrate.LegacySeriesPreference{ProfileID: profileID, SeriesID: seriesID}
		bySeries[key] = record
		return record
	}

	if err := eachRow(ctx, tx, `
SELECT profile_id, series_id, subtitle_language, subtitle_mode, show_forced_subtitles
  FROM user_subtitle_preferences WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var profileID, seriesID string
			var language, mode sql.NullString
			var forced sql.NullBool
			if err := scan(&profileID, &seriesID, &language, &mode, &forced); err != nil {
				return err
			}
			record := seriesRecord(profileID, seriesID)
			record.SubtitleSourceTable = "user_subtitle_preferences"
			record.SubtitleLanguage = nullableString(language)
			record.SubtitleMode = nullableString(mode)
			record.ShowForcedSubtitles = nullableBool(forced)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_subtitle_preferences: %w", err)
	}

	if err := eachRow(ctx, tx, `
SELECT profile_id, series_id, audio_language
  FROM user_audio_preferences WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var profileID, seriesID string
			var language sql.NullString
			if err := scan(&profileID, &seriesID, &language); err != nil {
				return err
			}
			record := seriesRecord(profileID, seriesID)
			record.AudioSourceTable = "user_audio_preferences"
			record.AudioLanguage = nullableString(language)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_audio_preferences: %w", err)
	}

	for _, record := range bySeries {
		input.SeriesPrefs = append(input.SeriesPrefs, *record)
	}

	if err := eachRow(ctx, tx, `
SELECT profile_id, library_id, audio_language, subtitle_language, subtitle_mode,
       show_forced_subtitles
  FROM user_library_playback_preferences WHERE user_id = $1`,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacyLibraryPreference
			var audio, subtitle, mode sql.NullString
			var forced sql.NullBool
			if err := scan(&row.ProfileID, &row.LibraryID,
				&audio, &subtitle, &mode, &forced); err != nil {
				return err
			}
			row.SourceTable = "user_library_playback_preferences"
			row.AudioLanguage = nullableString(audio)
			row.SubtitleLanguage = nullableString(subtitle)
			row.SubtitleMode = nullableString(mode)
			row.ShowForcedSubtitles = nullableBool(forced)
			input.LibraryPrefs = append(input.LibraryPrefs, row)
			return nil
		}, userID); err != nil {
		return input, fmt.Errorf("reading user_library_playback_preferences: %w", err)
	}

	return input, nil
}

func eachRow(
	ctx context.Context, tx *sql.Tx, query string,
	fn func(scan func(...any) error) error, args ...any,
) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close() //nolint:errcheck // read-only iteration

	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func nullableBool(value sql.NullBool) *bool {
	if !value.Valid {
		return nil
	}
	flag := value.Bool
	return &flag
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
