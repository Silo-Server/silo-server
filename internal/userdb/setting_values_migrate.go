package userdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsmigrate"
)

const (
	sqliteAudioPreferencesTable    = "audio_preferences"
	sqliteSubtitlePreferencesTable = "subtitle_preferences"
	sqliteLibraryPreferencesTable  = "library_playback_preferences"
)

// migrateSettingsToCanonical is the one-time backfill from legacy settings
// storage into user_setting_values.
//
// The conversion rules live in internal/settingsmigrate so this backend and
// Postgres cannot disagree about them; everything here is reading rows, handing
// them over, and writing what comes back. It runs inside the caller's
// transaction, so a failure anywhere leaves the database exactly as it was —
// the design's "completes and verifies atomically or leaves the database
// unchanged".
//
// The legacy tables are left in place. Dropping them belongs to the follow-up
// migration named in the design's post-cutover cleanup, once migrated counts
// have been verified against a backup.
func migrateSettingsToCanonical(tx *sql.Tx) error {
	contract, err := settingscontract.Load()
	if err != nil {
		return fmt.Errorf("loading settings contract: %w", err)
	}

	input, err := readLegacySettings(tx)
	if err != nil {
		return err
	}

	planner := settingsmigrate.New(contract, settingscontract.ObjectSchemas())
	result := planner.Plan(input)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range result.Rows {
		if _, err := tx.Exec(`
INSERT INTO user_setting_values
    (key, scope, profile_id, device_id, library_id, series_id, value, revision, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			row.Key, string(row.Scope),
			nullableText(row.ProfileID), nullableText(row.DeviceID),
			nullableInt(row.LibraryID), nullableText(row.SeriesID),
			string(row.Value), now, now,
		); err != nil {
			return fmt.Errorf("writing migrated setting %s at %s: %w", row.Key, row.Scope, err)
		}
	}

	for _, reject := range result.Rejects {
		if _, err := tx.Exec(`
INSERT INTO user_setting_migration_rejects
    (source_table, source_key, identity, value, reason, recorded_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			reject.SourceTable, reject.SourceKey, string(reject.Identity),
			reject.Value, reject.Reason, now,
		); err != nil {
			return fmt.Errorf("recording migration reject for %s: %w", reject.SourceKey, err)
		}
	}

	return nil
}

// readLegacySettings gathers every source the migration reads.
//
// Each query tolerates a missing table: this runs against databases created at
// any schema version, and a table an older install never had is simply empty
// rather than fatal.
func readLegacySettings(tx *sql.Tx) (settingsmigrate.Input, error) {
	var input settingsmigrate.Input

	profiles, err := readLegacyProfiles(tx)
	if err != nil {
		return input, err
	}
	input.Profiles = profiles

	if err := eachRow(tx, `SELECT key, value FROM user_settings`,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacySetting
			if err := scan(&row.Key, &row.Value); err != nil {
				return err
			}
			input.Settings = append(input.Settings, row)
			return nil
		}); err != nil {
		return input, fmt.Errorf("reading user_settings: %w", err)
	}

	if err := eachRow(tx, `SELECT profile_id, device_id, key, value FROM user_device_settings`,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacyDeviceSetting
			if err := scan(&row.ProfileID, &row.DeviceID, &row.Key, &row.Value); err != nil {
				return err
			}
			input.DeviceSettings = append(input.DeviceSettings, row)
			return nil
		}); err != nil {
		return input, fmt.Errorf("reading user_device_settings: %w", err)
	}

	// Subtitle and audio preferences are two tables keyed the same way, so they
	// merge into one per-series record rather than producing two rows that
	// would each overwrite the other's scope.
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

	if err := eachRow(tx, `
SELECT profile_id, series_id, subtitle_language, subtitle_mode, show_forced_subtitles
	FROM `+sqliteSubtitlePreferencesTable,
		func(scan func(...any) error) error {
			var profileID, seriesID string
			var language, mode sql.NullString
			var forced sql.NullBool
			if err := scan(&profileID, &seriesID, &language, &mode, &forced); err != nil {
				return err
			}
			record := seriesRecord(profileID, seriesID)
			record.SubtitleSourceTable = sqliteSubtitlePreferencesTable
			record.SubtitleLanguage = nullString(language)
			record.SubtitleMode = nullString(mode)
			record.ShowForcedSubtitles = nullBool(forced)
			return nil
		}); err != nil {
		return input, fmt.Errorf("reading %s: %w", sqliteSubtitlePreferencesTable, err)
	}

	if err := eachRow(tx, `SELECT profile_id, series_id, audio_language FROM `+sqliteAudioPreferencesTable,
		func(scan func(...any) error) error {
			var profileID, seriesID string
			var language sql.NullString
			if err := scan(&profileID, &seriesID, &language); err != nil {
				return err
			}
			record := seriesRecord(profileID, seriesID)
			record.AudioSourceTable = sqliteAudioPreferencesTable
			record.AudioLanguage = nullString(language)
			return nil
		}); err != nil {
		return input, fmt.Errorf("reading %s: %w", sqliteAudioPreferencesTable, err)
	}

	for _, record := range bySeries {
		input.SeriesPrefs = append(input.SeriesPrefs, *record)
	}

	if err := eachRow(tx, `
SELECT profile_id, library_id, audio_language, subtitle_language, subtitle_mode, show_forced_subtitles
	FROM `+sqliteLibraryPreferencesTable,
		func(scan func(...any) error) error {
			var row settingsmigrate.LegacyLibraryPreference
			var audio, subtitle, mode sql.NullString
			var forced sql.NullBool
			if err := scan(&row.ProfileID, &row.LibraryID,
				&audio, &subtitle, &mode, &forced); err != nil {
				return err
			}
			row.SourceTable = sqliteLibraryPreferencesTable
			row.AudioLanguage = nullString(audio)
			row.SubtitleLanguage = nullString(subtitle)
			row.SubtitleMode = nullString(mode)
			row.ShowForcedSubtitles = nullBool(forced)
			input.LibraryPrefs = append(input.LibraryPrefs, row)
			return nil
		}); err != nil {
		return input, fmt.Errorf("reading %s: %w", sqliteLibraryPreferencesTable, err)
	}

	return input, nil
}

// readLegacyProfiles reads the preference columns off the profiles table.
//
// preferred_metadata_language is deliberately absent: the column exists only in
// the Postgres schema, so catalog.metadata_language has no SQLite source and
// the field stays nil here.
func readLegacyProfiles(tx *sql.Tx) ([]settingsmigrate.LegacyProfile, error) {
	// Preserve loaded-empty versus not-loaded. The migration planner rejects
	// profile-scoped rows against an empty loaded list, while nil means profile
	// ownership was unavailable to check.
	profiles := make([]settingsmigrate.LegacyProfile, 0)
	err := eachRow(tx, `
SELECT id, quality_preference, language, subtitle_language, subtitle_mode, show_forced_subtitles,
       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview
  FROM profiles`,
		func(scan func(...any) error) error {
			var profile settingsmigrate.LegacyProfile
			var quality, language, subtitle, mode sql.NullString
			var forced, skipIntro, skipCredits, skipRecap, nextPreview sql.NullBool
			if err := scan(&profile.ID, &quality, &language, &subtitle, &mode, &forced,
				&skipIntro, &skipCredits, &skipRecap, &nextPreview); err != nil {
				return err
			}
			profile.QualityPreference = nullString(quality)
			profile.Language = nullString(language)
			profile.SubtitleLanguage = nullString(subtitle)
			profile.SubtitleMode = nullString(mode)
			profile.ShowForcedSubtitles = nullBool(forced)
			profile.AutoSkipIntro = nullBool(skipIntro)
			profile.AutoSkipCredits = nullBool(skipCredits)
			profile.AutoSkipRecap = nullBool(skipRecap)
			profile.AutoPlayNextPreview = nullBool(nextPreview)
			profiles = append(profiles, profile)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading profiles: %w", err)
	}
	return profiles, nil
}

// eachRow runs a query and calls fn per row, treating a missing table as no
// rows. Every legacy table this migration reads was added at some schema
// version, so a database older than that simply has nothing to migrate from it.
func eachRow(tx *sql.Tx, query string, fn func(scan func(...any) error) error) error {
	rows, err := tx.Query(query)
	if err != nil {
		if isMissingTable(err) {
			return nil
		}
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

func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func nullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func nullBool(value sql.NullBool) *bool {
	if !value.Valid {
		return nil
	}
	flag := value.Bool
	return &flag
}
