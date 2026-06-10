package userdb

import (
	"database/sql"
	"fmt"
)

const schemaVersion = 11

func runMigrations(db *sql.DB) error {
	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("unsupported sqlite schema version %d", version)
	}
	if version == 0 {
		return setUserVersion(db, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning sqlite migration transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if version < 2 {
		if err := migrateToV2(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
			return fmt.Errorf("setting sqlite user_version 2: %w", err)
		}
	}

	if version < 3 {
		if err := migrateToV3(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 3"); err != nil {
			return fmt.Errorf("setting sqlite user_version 3: %w", err)
		}
	}

	if version < 4 {
		if err := migrateToV4(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 4"); err != nil {
			return fmt.Errorf("setting sqlite user_version 4: %w", err)
		}
	}

	if version < 5 {
		if err := migrateToV5(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 5"); err != nil {
			return fmt.Errorf("setting sqlite user_version 5: %w", err)
		}
	}

	if version < 6 {
		if err := migrateToV6(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 6"); err != nil {
			return fmt.Errorf("setting sqlite user_version 6: %w", err)
		}
	}

	if version < 7 {
		if err := migrateToV7(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 7"); err != nil {
			return fmt.Errorf("setting sqlite user_version 7: %w", err)
		}
	}

	if version < 8 {
		if err := migrateToV8(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 8"); err != nil {
			return fmt.Errorf("setting sqlite user_version 8: %w", err)
		}
	}

	if version < 9 {
		if _, err := tx.Exec("PRAGMA user_version = 9"); err != nil {
			return fmt.Errorf("setting sqlite user_version 9: %w", err)
		}
	}

	if version < 10 {
		if err := migrateToV10(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 10"); err != nil {
			return fmt.Errorf("setting sqlite user_version 10: %w", err)
		}
	}

	if version < 11 {
		if err := migrateToV11(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 11"); err != nil {
			return fmt.Errorf("setting sqlite user_version 11: %w", err)
		}
	}

	return tx.Commit()
}

// migrateToV11 resets legacy completed watch_progress rows to
// position_seconds = 0. The watch-progress model keeps `completed` as a
// one-way watched latch with no resume point; the Continue Watching
// predicate is position_seconds > 0, so legacy rows (position pinned to the
// duration) would otherwise surface as phantom resume entries. Running this
// as a versioned migration (rather than a predicate-only data fix) matters:
// re-running the UPDATE on every boot would wipe the resume point of any
// rewatch in flight.
func migrateToV11(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		UPDATE watch_progress
		SET position_seconds = 0
		WHERE completed = 1
		  AND position_seconds <> 0`); err != nil {
		return fmt.Errorf("resetting completed watch_progress positions: %w", err)
	}
	return nil
}

func migrateToV10(tx *sql.Tx) error {
	if columnExists(tx, "watch_history", "watch_identity") {
		return nil
	}
	if _, err := tx.Exec("ALTER TABLE watch_history ADD COLUMN watch_identity TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("adding watch_history.watch_identity: %w", err)
	}
	return nil
}

func userVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("querying sqlite user_version: %w", err)
	}
	return version, nil
}

func setUserVersion(db *sql.DB, version int) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("setting sqlite user_version %d: %w", version, err)
	}
	return nil
}

func migrateToV3(tx *sql.Tx) error {
	cols := []struct{ name, ddl string }{
		{"last_file_id", "ALTER TABLE watch_progress ADD COLUMN last_file_id INTEGER"},
		{"last_resolution", "ALTER TABLE watch_progress ADD COLUMN last_resolution TEXT"},
		{"last_hdr", "ALTER TABLE watch_progress ADD COLUMN last_hdr BOOLEAN"},
		{"last_codec_video", "ALTER TABLE watch_progress ADD COLUMN last_codec_video TEXT"},
	}
	for _, c := range cols {
		if columnExists(tx, "watch_progress", c.name) {
			continue
		}
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("adding watch_progress.%s: %w", c.name, err)
		}
	}
	return nil
}

func migrateToV8(tx *sql.Tx) error {
	if columnExists(tx, "watch_progress", "last_edition_key") {
		return nil
	}
	if _, err := tx.Exec("ALTER TABLE watch_progress ADD COLUMN last_edition_key TEXT"); err != nil {
		return fmt.Errorf("adding watch_progress.last_edition_key: %w", err)
	}
	return nil
}

func columnExists(tx *sql.Tx, table, column string) bool {
	var count int
	err := tx.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count)
	return err == nil && count > 0
}

func migrateToV2(tx *sql.Tx) error {
	if _, err := tx.Exec("ALTER TABLE profiles ADD COLUMN library_restrictions_enabled BOOLEAN DEFAULT false"); err != nil {
		return fmt.Errorf("adding profiles.library_restrictions_enabled: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE profiles ADD COLUMN max_playback_quality TEXT DEFAULT ''"); err != nil {
		return fmt.Errorf("adding profiles.max_playback_quality: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS profile_allowed_libraries (
			profile_id TEXT NOT NULL,
			library_id INTEGER NOT NULL,
			PRIMARY KEY (profile_id, library_id)
		)`); err != nil {
		return fmt.Errorf("creating profile_allowed_libraries: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_profile_allowed_libraries_lookup
		ON profile_allowed_libraries(profile_id)`); err != nil {
		return fmt.Errorf("creating idx_profile_allowed_libraries_lookup: %w", err)
	}
	return nil
}

func migrateToV4(tx *sql.Tx) error {
	cols := []struct{ name, ddl string }{
		{"creator_profile_id", "ALTER TABLE personal_collections ADD COLUMN creator_profile_id TEXT NOT NULL DEFAULT ''"},
		{"collection_type", "ALTER TABLE personal_collections ADD COLUMN collection_type TEXT NOT NULL DEFAULT 'manual'"},
		{"is_shared", "ALTER TABLE personal_collections ADD COLUMN is_shared BOOLEAN DEFAULT false"},
		{"query_definition", "ALTER TABLE personal_collections ADD COLUMN query_definition TEXT NOT NULL DEFAULT '{}'"},
		{"sort_config", "ALTER TABLE personal_collections ADD COLUMN sort_config TEXT NOT NULL DEFAULT '{}'"},
	}
	for _, c := range cols {
		if columnExists(tx, "personal_collections", c.name) {
			continue
		}
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("adding personal_collections.%s: %w", c.name, err)
		}
	}

	if _, err := tx.Exec(`
		UPDATE personal_collections
		SET creator_profile_id = profile_id
		WHERE creator_profile_id = ''
	`); err != nil {
		return fmt.Errorf("backfilling creator_profile_id: %w", err)
	}

	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS personal_collection_profiles (
			collection_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			PRIMARY KEY (collection_id, profile_id)
		)`); err != nil {
		return fmt.Errorf("creating personal_collection_profiles: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO personal_collection_profiles (collection_id, profile_id)
		SELECT id, profile_id
		FROM personal_collections
	`); err != nil {
		return fmt.Errorf("backfilling personal_collection_profiles: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_personal_collection_profiles_lookup
		ON personal_collection_profiles(profile_id, collection_id)`); err != nil {
		return fmt.Errorf("creating idx_personal_collection_profiles_lookup: %w", err)
	}
	return nil
}

func migrateToV5(tx *sql.Tx) error {
	// Rename collection_mode → collection_type. SQLite ALTER TABLE RENAME COLUMN
	// is supported in SQLite ≥ 3.25.0.
	if columnExists(tx, "personal_collections", "collection_mode") {
		if _, err := tx.Exec("ALTER TABLE personal_collections RENAME COLUMN collection_mode TO collection_type"); err != nil {
			return fmt.Errorf("renaming personal_collections.collection_mode → collection_type: %w", err)
		}
	}
	return nil
}

func migrateToV6(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS series_playback_preferences (
			profile_id TEXT NOT NULL,
			series_id TEXT NOT NULL,
			resolution TEXT,
			hdr BOOLEAN NOT NULL DEFAULT false,
			codec_video TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (profile_id, series_id)
		)`); err != nil {
		return fmt.Errorf("creating series_playback_preferences: %w", err)
	}
	return nil
}

func migrateToV7(tx *sql.Tx) error {
	cols := []struct {
		table string
		name  string
		ddl   string
	}{
		{
			table: "audio_preferences",
			name:  "audio_track_signature",
			ddl:   "ALTER TABLE audio_preferences ADD COLUMN audio_track_signature TEXT NOT NULL DEFAULT '{}'",
		},
		{
			table: "subtitle_preferences",
			name:  "subtitle_track_signature",
			ddl:   "ALTER TABLE subtitle_preferences ADD COLUMN subtitle_track_signature TEXT NOT NULL DEFAULT '{}'",
		},
	}

	for _, c := range cols {
		if columnExists(tx, c.table, c.name) {
			continue
		}
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("adding %s.%s: %w", c.table, c.name, err)
		}
	}

	return nil
}
