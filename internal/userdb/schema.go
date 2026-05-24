// Package userdb provides per-user SQLite database management for Silo.
// Each user gets their own SQLite file storing profiles, watch progress,
// favorites, collections, playback sessions, and settings.
package userdb

import (
	"database/sql"
	"fmt"
)

// Schema is the full SQLite schema for per-user databases.
const Schema = `
CREATE TABLE IF NOT EXISTS profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    avatar TEXT,
    pin_hash TEXT,
    is_child BOOLEAN DEFAULT false,
    is_primary BOOLEAN NOT NULL DEFAULT false,
    max_content_rating TEXT,
    quality_preference TEXT DEFAULT '1080p',
    language TEXT DEFAULT 'en',
    subtitle_language TEXT,
    subtitle_mode TEXT DEFAULT 'auto',
    auto_skip_intro BOOLEAN DEFAULT false,
    auto_skip_credits BOOLEAN DEFAULT false,
    auto_skip_recap BOOLEAN DEFAULT false,
    auto_play_next_preview BOOLEAN DEFAULT false,
    show_forced_subtitles BOOLEAN NOT NULL DEFAULT true,
    library_restrictions_enabled BOOLEAN DEFAULT false,
    max_playback_quality TEXT DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS profile_allowed_libraries (
    profile_id TEXT NOT NULL,
    library_id INTEGER NOT NULL,
    PRIMARY KEY (profile_id, library_id)
);

CREATE TABLE IF NOT EXISTS watch_progress (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position_seconds REAL NOT NULL,
    duration_seconds REAL NOT NULL,
    completed BOOLEAN DEFAULT false,
    updated_at TEXT NOT NULL,
    last_file_id INTEGER,
    last_resolution TEXT,
    last_hdr BOOLEAN,
    last_codec_video TEXT,
    last_edition_key TEXT,
    PRIMARY KEY (profile_id, media_item_id)
);

CREATE TABLE IF NOT EXISTS watch_history (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    watched_at TEXT NOT NULL,
    duration_seconds REAL,
    completed BOOLEAN DEFAULT false,
    source TEXT NOT NULL DEFAULT 'legacy',
    watch_identity TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS hidden_history_items (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    hidden_before TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, media_item_id)
);

CREATE TABLE IF NOT EXISTS playback_sessions (
    session_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_file_id INTEGER NOT NULL,
    play_method TEXT NOT NULL,
    position_seconds REAL DEFAULT 0,
    is_paused BOOLEAN DEFAULT false,
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS favorites (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, media_item_id)
);

CREATE TABLE IF NOT EXISTS watchlist (
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, media_item_id)
);

CREATE TABLE IF NOT EXISTS home_item_dismissals (
    profile_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    series_id TEXT,
    progress_updated_at TEXT,
    dismissed_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, surface, media_item_id)
);

CREATE TABLE IF NOT EXISTS personal_collections (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    creator_profile_id TEXT NOT NULL,
    name TEXT NOT NULL,
    collection_type TEXT NOT NULL DEFAULT 'manual',
    is_shared BOOLEAN DEFAULT false,
    query_definition TEXT NOT NULL DEFAULT '{}',
    sort_config TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS personal_collection_items (
    collection_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position INTEGER,
    added_at TEXT NOT NULL,
    PRIMARY KEY (collection_id, media_item_id)
);

CREATE TABLE IF NOT EXISTS personal_collection_profiles (
    collection_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    PRIMARY KEY (collection_id, profile_id)
);

CREATE TABLE IF NOT EXISTS subtitle_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    subtitle_language TEXT,
    subtitle_track_index INT,
    external_subtitle_path TEXT,
    subtitle_mode TEXT,
    subtitle_track_signature TEXT NOT NULL DEFAULT '{}',
    show_forced_subtitles BOOLEAN,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);

CREATE TABLE IF NOT EXISTS audio_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    audio_track_index INT,
    audio_language TEXT,
    audio_track_signature TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);

CREATE TABLE IF NOT EXISTS series_playback_preferences (
    profile_id TEXT NOT NULL,
    series_id TEXT NOT NULL,
    resolution TEXT,
    hdr BOOLEAN NOT NULL DEFAULT false,
    codec_video TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, series_id)
);

CREATE TABLE IF NOT EXISTS library_playback_preferences (
    profile_id TEXT NOT NULL,
    library_id INTEGER NOT NULL,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT,
    show_forced_subtitles BOOLEAN,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, library_id)
);

CREATE TABLE IF NOT EXISTS user_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS user_device_settings (
    profile_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    device_platform TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, device_id, key)
);

CREATE TABLE IF NOT EXISTS downloads (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    media_file_id INTEGER NOT NULL,
    quality TEXT,
    transcoded BOOLEAN DEFAULT false,
    file_size INTEGER,
    expires_at TEXT,
    downloaded_at TEXT
);

CREATE TABLE IF NOT EXISTS profile_section_overrides (
    id                TEXT    PRIMARY KEY,
    profile_id        TEXT    NOT NULL,
    scope             TEXT    NOT NULL CHECK (scope IN ('home', 'library')),
    library_id        TEXT,
    section_id        TEXT,
    position          INTEGER,
    hidden            INTEGER NOT NULL DEFAULT 0,
    removed           INTEGER NOT NULL DEFAULT 0,
    section_type      TEXT,
    title             TEXT,
    featured          INTEGER,
    item_limit        INTEGER,
    config            TEXT,
    is_user_added     INTEGER NOT NULL DEFAULT 0,
    user_section_type TEXT,
    user_config       TEXT,
    user_title        TEXT,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_profile_section_overrides_lookup
    ON profile_section_overrides(profile_id, scope, library_id);

CREATE INDEX IF NOT EXISTS idx_profile_allowed_libraries_lookup
    ON profile_allowed_libraries(profile_id);

CREATE INDEX IF NOT EXISTS idx_personal_collection_profiles_lookup
    ON personal_collection_profiles(profile_id, collection_id);

CREATE INDEX IF NOT EXISTS idx_home_item_dismissals_lookup
    ON home_item_dismissals(profile_id, surface);

CREATE INDEX IF NOT EXISTS idx_hidden_history_items_lookup
    ON hidden_history_items(profile_id, hidden_before);
`

// InitSchema creates all tables in the given SQLite database.
func InitSchema(db *sql.DB) error {
	_, err := db.Exec(Schema)
	if err != nil {
		return err
	}
	if err := ensureProfileSectionOverridesRemovedColumn(db); err != nil {
		return err
	}
	if err := ensureProfileSectionOverridesUserAddedColumns(db); err != nil {
		return err
	}
	if err := ensureWatchHistorySourceColumn(db); err != nil {
		return err
	}
	if err := ensureShowForcedSubtitleColumns(db); err != nil {
		return err
	}
	if err := ensureProfileIsPrimaryColumn(db); err != nil {
		return err
	}
	if err := ensureDeviceSettingsProfileColumn(db); err != nil {
		return err
	}
	if err := ensureAutoSkipRecapPreviewColumns(db); err != nil {
		return err
	}
	if err := ensureWatchHistoryIdentityColumn(db); err != nil {
		return err
	}
	return migratePlaybackSettingsToDeviceScope(db)
}

func ensureAutoSkipRecapPreviewColumns(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "auto_skip_recap", definition: "BOOLEAN DEFAULT false"},
		{name: "auto_play_next_preview", definition: "BOOLEAN DEFAULT false"},
	}

	for _, column := range columns {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
			"profiles",
			column.name,
		).Scan(&count); err != nil {
			return fmt.Errorf("checking profiles.%s column: %w", column.name, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(
			fmt.Sprintf("ALTER TABLE profiles ADD COLUMN %s %s", column.name, column.definition),
		); err != nil {
			return fmt.Errorf("adding profiles.%s column: %w", column.name, err)
		}
	}
	return nil
}

func ensureWatchHistoryIdentityColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		"watch_history",
		"watch_identity",
	).Scan(&count); err != nil {
		return fmt.Errorf("checking watch_history.watch_identity column: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE watch_history ADD COLUMN watch_identity TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("adding watch_history.watch_identity: %w", err)
	}
	return nil
}

func ensureProfileIsPrimaryColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		"profiles",
		"is_primary",
	).Scan(&count); err != nil {
		return fmt.Errorf("checking profiles.is_primary column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(
			"ALTER TABLE profiles ADD COLUMN is_primary BOOLEAN NOT NULL DEFAULT false",
		); err != nil {
			return fmt.Errorf("adding profiles.is_primary column: %w", err)
		}
	}
	// Backfill: the oldest existing profile (per-user sqlite file, so just the
	// oldest row overall) is the primary. No-op if a primary already exists.
	if _, err := db.Exec(`
		UPDATE profiles
		SET is_primary = 1
		WHERE id = (
			SELECT id FROM profiles
			WHERE NOT EXISTS (SELECT 1 FROM profiles WHERE is_primary)
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		)
	`); err != nil {
		return fmt.Errorf("backfilling profiles.is_primary: %w", err)
	}
	return nil
}

func ensureDeviceSettingsProfileColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		"user_device_settings",
		"profile_id",
	).Scan(&count); err != nil {
		return fmt.Errorf("checking user_device_settings.profile_id column: %w", err)
	}
	if count > 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning user_device_settings profile migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`
		CREATE TABLE user_device_settings_new (
			profile_id TEXT NOT NULL,
			device_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			device_name TEXT NOT NULL DEFAULT '',
			device_platform TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			PRIMARY KEY (profile_id, device_id, key)
		)
	`); err != nil {
		return fmt.Errorf("creating user_device_settings_new: %w", err)
	}

	if _, err := tx.Exec(`
			INSERT INTO profiles (id, name, is_primary, created_at, updated_at)
			SELECT 'default', 'Default', 1, datetime('now'), datetime('now')
			WHERE NOT EXISTS (SELECT 1 FROM profiles)
		`); err != nil {
		return fmt.Errorf("ensuring default profile for user_device_settings migration: %w", err)
	}

	if _, err := tx.Exec(`
			INSERT INTO user_device_settings_new (
				profile_id, device_id, key, value, device_name, device_platform, updated_at
			)
			SELECT p.id, uds.device_id, uds.key, uds.value, uds.device_name, uds.device_platform, uds.updated_at
			FROM user_device_settings uds
			JOIN (
				SELECT id
				FROM profiles
				ORDER BY is_primary DESC, created_at ASC, id ASC
				LIMIT 1
			) p ON 1 = 1
		`); err != nil {
		return fmt.Errorf("backfilling profile-aware user_device_settings: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE user_device_settings`); err != nil {
		return fmt.Errorf("dropping legacy user_device_settings: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE user_device_settings_new RENAME TO user_device_settings`); err != nil {
		return fmt.Errorf("renaming profile-aware user_device_settings: %w", err)
	}

	return tx.Commit()
}

func ensureProfileSectionOverridesRemovedColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		"profile_section_overrides",
		"removed",
	).Scan(&count); err != nil {
		return fmt.Errorf("checking profile_section_overrides.removed column: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE profile_section_overrides ADD COLUMN removed INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("adding profile_section_overrides.removed: %w", err)
	}
	return nil
}

// ensureProfileSectionOverridesUserAddedColumns adds the four columns that back
// user-added (profile-built) sections so SaveSectionOverrides can round-trip
// IsUserAdded / UserSectionType / UserConfig / UserTitle. Without these columns
// those fields are silently dropped on save and lost on subsequent reads.
func ensureProfileSectionOverridesUserAddedColumns(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "is_user_added", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "user_section_type", definition: "TEXT"},
		{name: "user_config", definition: "TEXT"},
		{name: "user_title", definition: "TEXT"},
	}
	for _, c := range columns {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
			"profile_section_overrides",
			c.name,
		).Scan(&count); err != nil {
			return fmt.Errorf("checking profile_section_overrides.%s column: %w", c.name, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			"ALTER TABLE profile_section_overrides ADD COLUMN %s %s", c.name, c.definition,
		)); err != nil {
			return fmt.Errorf("adding profile_section_overrides.%s: %w", c.name, err)
		}
	}
	return nil
}

func ensureWatchHistorySourceColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		"watch_history",
		"source",
	).Scan(&count); err != nil {
		return fmt.Errorf("checking watch_history.source column: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE watch_history ADD COLUMN source TEXT NOT NULL DEFAULT 'legacy'"); err != nil {
		return fmt.Errorf("adding watch_history.source: %w", err)
	}
	return nil
}

func ensureShowForcedSubtitleColumns(db *sql.DB) error {
	columns := []struct {
		table      string
		column     string
		definition string
	}{
		{table: "profiles", column: "show_forced_subtitles", definition: "BOOLEAN NOT NULL DEFAULT true"},
		{table: "library_playback_preferences", column: "show_forced_subtitles", definition: "BOOLEAN"},
		{table: "subtitle_preferences", column: "show_forced_subtitles", definition: "BOOLEAN"},
	}

	for _, column := range columns {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
			column.table,
			column.column,
		).Scan(&count); err != nil {
			return fmt.Errorf("checking %s.%s column: %w", column.table, column.column, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.column, column.definition),
		); err != nil {
			return fmt.Errorf("adding %s.%s column: %w", column.table, column.column, err)
		}
	}

	return nil
}

func migratePlaybackSettingsToDeviceScope(db *sql.DB) error {
	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if version == 0 || version >= 9 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning playback settings device-scope migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO user_device_settings (
			profile_id, device_id, key, value, device_name, device_platform, updated_at
		)
		SELECT
			devices.profile_id,
			devices.device_id,
			settings.key,
			settings.value,
			devices.device_name,
			devices.device_platform,
			devices.updated_at
			FROM user_settings AS settings
			JOIN (
				SELECT DISTINCT profile_id, device_id, device_name, device_platform, updated_at
				FROM user_device_settings
			) AS devices ON 1 = 1
			WHERE settings.key IN (
				'playback.preferred_quality',
				'playback.audio_language',
				'playback.auto_skip_intro',
				'playback.auto_skip_credits',
				'playback.auto_play_next'
			)
			AND NOT EXISTS (
				SELECT 1
				FROM user_device_settings AS existing
				WHERE existing.profile_id = devices.profile_id
				  AND existing.device_id = devices.device_id
				  AND existing.key = settings.key
			)
			`); err != nil {
		return fmt.Errorf("backfilling playback settings into user_device_settings: %w", err)
	}

	return tx.Commit()
}
