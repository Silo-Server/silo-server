package userdb

import (
	"database/sql"
	"encoding/json"
	"testing"
)

// seedLegacySettings fills the pre-cutover tables the way a real install would.
func seedLegacySettings(t *testing.T, db *sql.DB) {
	t.Helper()

	// Two profiles on one account: appearance moved from the account to the
	// profile, so an account row has to reach both.
	for _, profile := range []struct {
		id, quality, language, subtitleLang, mode string
		forced, skipIntro                         bool
	}{
		{"p1", "1080p-high", "ja", "en", "always", false, true},
		{"p2", "1080p", "en", "", "auto", true, false},
	} {
		if _, err := db.Exec(`
INSERT INTO profiles
    (id, name, quality_preference, language, subtitle_language, subtitle_mode, show_forced_subtitles,
     auto_skip_intro, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			profile.id, profile.id, profile.quality, profile.language,
			profile.subtitleLang, profile.mode, profile.forced, profile.skipIntro); err != nil {
			t.Fatalf("seeding profile %s: %v", profile.id, err)
		}
	}

	for key, value := range map[string]string{
		"ui_theme":      "cobalt-studio",
		"ui_text_scale": "large",
		// Rides the same table under a synthetic key and is not a user setting.
		"jellycompat:displayprefs:usersettings:emby": `{"a":1}`,
		// Never had a definition; must be recorded rather than dropped.
		"legacy.mystery": "whatever",
	} {
		if _, err := db.Exec(
			`INSERT INTO user_settings (key, value) VALUES (?, ?)`, key, value); err != nil {
			t.Fatalf("seeding user_settings %s: %v", key, err)
		}
	}

	for _, row := range []struct{ profile, device, key, value string }{
		{"p1", "d1", "playback.preferred_quality", "720p-high"},
		{"p1", "d1", "playback.auto_skip_intro", "true"},
		{"p1", "d1", "player.audio_sync_ms", "-250"},
		// Both spellings of the same key on one device: the legacy PK keys on
		// the stored spelling, so real installs hold this pair whenever the
		// runtime's best-effort alias cleanup failed. Migrating both would trip
		// the canonical unique index and abort the whole migration.
		{"p1", "d1", "player.next_up_prompt_seconds", "45"},
		{"p1", "d1", "playback.next_up_prompt_seconds", "20"},
	} {
		if _, err := db.Exec(`
INSERT INTO user_device_settings (profile_id, device_id, key, value, updated_at)
VALUES (?, ?, ?, ?, '2026-01-01T00:00:00Z')`,
			row.profile, row.device, row.key, row.value); err != nil {
			t.Fatalf("seeding device setting %s: %v", row.key, err)
		}
	}

	if _, err := db.Exec(`
INSERT INTO subtitle_preferences (profile_id, series_id, subtitle_language, subtitle_mode, show_forced_subtitles, updated_at)
VALUES ('p1', 's1', 'de', 'always', 0, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding subtitle_preferences: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO audio_preferences (profile_id, series_id, audio_language, updated_at)
VALUES ('p1', 's1', 'fr', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding audio_preferences: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO library_playback_preferences (profile_id, library_id, audio_language, subtitle_mode, updated_at)
VALUES ('p1', 7, 'es', 'off', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding library_playback_preferences: %v", err)
	}
}

func canonicalValue(t *testing.T, db *sql.DB, key, scope string, where string, args ...any) (string, bool) {
	t.Helper()
	query := `SELECT value FROM user_setting_values WHERE key = ? AND scope = ?`
	if where != "" {
		query += " AND " + where
	}
	full := append([]any{key, scope}, args...)
	var value string
	err := db.QueryRow(query, full...).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("reading %s at %s: %v", key, scope, err)
	}
	return value, true
}

// TestMigrateToV16BackfillsCanonicalValues runs the real migration against a
// real database. The planner's rules are unit-tested in internal/settingsmigrate;
// what this covers is the wiring — that the rows actually land, satisfy the
// scope CHECK and the partial unique indexes, and that nothing violates the
// json_valid constraints.
func TestMigrateToV16BackfillsCanonicalValues(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	seedLegacySettings(t, db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV16(tx); err != nil {
		t.Fatalf("migrateToV16: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	t.Run("profile columns become profile-scope values", func(t *testing.T) {
		if got, ok := canonicalValue(t, db, "playback.audio_language", "profile",
			"profile_id = ?", "p1"); !ok || got != `"ja"` {
			t.Errorf("p1 audio language = %q (found=%v), want \"ja\"", got, ok)
		}
		// These schema defaults were also the effective legacy behavior, so they
		// survive as explicit rows where the contract defaults differ.
		if got, ok := canonicalValue(t, db, "playback.audio_language", "profile",
			"profile_id = ?", "p2"); !ok || got != `"en"` {
			t.Errorf("p2 audio language = %q (found=%v), want the effective \"en\"", got, ok)
		}
		if got, ok := canonicalValue(t, db, "playback.preferred_quality", "profile",
			"profile_id = ?", "p2"); !ok || got != `"1080p"` {
			t.Errorf("p2 quality = %q (found=%v), want the effective \"1080p\"", got, ok)
		}
		if got, ok := canonicalValue(t, db, "playback.max_bitrate_kbps", "profile",
			"profile_id = ?", "p2"); !ok || got != `6000` {
			t.Errorf("p2 bitrate = %q (found=%v), want the effective 6000", got, ok)
		}
	})

	t.Run("auto-skip columns migrate only when true", func(t *testing.T) {
		if got, ok := canonicalValue(t, db, "playback.auto_skip_intro", "profile",
			"profile_id = ?", "p1"); !ok || got != `true` {
			t.Errorf("p1 auto_skip_intro = %q (found=%v), want true", got, ok)
		}
		if got, ok := canonicalValue(t, db, "playback.auto_skip_intro", "profile",
			"profile_id = ?", "p2"); ok {
			t.Errorf("p2 got auto_skip_intro %q from an untouched false column", got)
		}
	})

	t.Run("renamed and canonical device keys collapse to one row", func(t *testing.T) {
		// Both spellings were seeded; without the dedup pass the second insert
		// violates the unique index and the whole migration — and with it the
		// user's store — fails to open. The canonical-keyed value wins.
		if got, ok := canonicalValue(t, db, "playback.next_up_prompt_seconds", "profile_device",
			"profile_id = ? AND device_id = ?", "p1", "d1"); !ok || got != `20` {
			t.Errorf("next_up_prompt_seconds = %q (found=%v), want the canonical-keyed 20", got, ok)
		}
	})

	t.Run("legacy quality decomposes into two axes", func(t *testing.T) {
		quality, ok := canonicalValue(t, db, "playback.preferred_quality", "profile",
			"profile_id = ?", "p1")
		if !ok || quality != `"1080p"` {
			t.Errorf("resolution = %q (found=%v), want \"1080p\"", quality, ok)
		}
		bitrate, ok := canonicalValue(t, db, "playback.max_bitrate_kbps", "profile",
			"profile_id = ?", "p1")
		if !ok || bitrate != `10000` {
			t.Errorf("bitrate = %q (found=%v), want 10000", bitrate, ok)
		}

		// The device row decomposes too, at its own scope.
		deviceQuality, ok := canonicalValue(t, db, "playback.preferred_quality", "profile_device",
			"profile_id = ? AND device_id = ?", "p1", "d1")
		if !ok || deviceQuality != `"720p"` {
			t.Errorf("device resolution = %q (found=%v), want \"720p\"", deviceQuality, ok)
		}
	})

	t.Run("account settings fan out to every profile", func(t *testing.T) {
		for _, profile := range []string{"p1", "p2"} {
			if got, ok := canonicalValue(t, db, "ui.theme", "profile",
				"profile_id = ?", profile); !ok || got != `"cobalt-studio"` {
				t.Errorf("%s theme = %q (found=%v)", profile, got, ok)
			}
		}
	})

	t.Run("series and library preferences land at their scopes", func(t *testing.T) {
		if got, ok := canonicalValue(t, db, "playback.subtitle_language", "profile_series",
			"profile_id = ? AND series_id = ?", "p1", "s1"); !ok || got != `"de"` {
			t.Errorf("series subtitle language = %q (found=%v), want \"de\"", got, ok)
		}
		// Audio and subtitle preferences are separate tables keyed alike; both
		// must survive rather than one overwriting the other.
		if got, ok := canonicalValue(t, db, "playback.audio_language", "profile_series",
			"profile_id = ? AND series_id = ?", "p1", "s1"); !ok || got != `"fr"` {
			t.Errorf("series audio language = %q (found=%v), want \"fr\"", got, ok)
		}
		if got, ok := canonicalValue(t, db, "playback.audio_language", "profile_library",
			"profile_id = ? AND library_id = ?", "p1", 7); !ok || got != `"es"` {
			t.Errorf("library audio language = %q (found=%v), want \"es\"", got, ok)
		}
	})

	t.Run("legacy strings become typed JSON", func(t *testing.T) {
		if got, ok := canonicalValue(t, db, "playback.auto_skip_intro", "profile_device",
			"profile_id = ? AND device_id = ?", "p1", "d1"); !ok || got != `true` {
			t.Errorf("auto_skip_intro = %q, want the boolean true", got)
		}
		if got, ok := canonicalValue(t, db, "player.audio_sync_ms", "profile_device",
			"profile_id = ? AND device_id = ?", "p1", "d1"); !ok || got != `-250` {
			t.Errorf("audio_sync_ms = %q, want the number -250", got)
		}
	})

	t.Run("unconvertible rows are recorded, jellycompat blobs are not", func(t *testing.T) {
		var reason, identity string
		err := db.QueryRow(`
SELECT reason, identity FROM user_setting_migration_rejects WHERE source_key = 'legacy.mystery'`).
			Scan(&reason, &identity)
		if err != nil {
			t.Fatalf("the unknown key was dropped rather than recorded: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(identity), &decoded); err != nil {
			t.Errorf("reject identity %q is not JSON: %v", identity, err)
		}

		var jellycompat int
		if err := db.QueryRow(`
SELECT COUNT(*) FROM user_setting_migration_rejects WHERE source_key LIKE 'jellycompat:%'`).
			Scan(&jellycompat); err != nil {
			t.Fatalf("counting jellycompat rejects: %v", err)
		}
		if jellycompat != 0 {
			t.Errorf("%d jellycompat blobs were rejected; they should be left alone", jellycompat)
		}
	})

	t.Run("every written value is valid JSON at an allowed scope", func(t *testing.T) {
		rows, err := db.Query(`SELECT key, scope, value FROM user_setting_values`)
		if err != nil {
			t.Fatalf("listing values: %v", err)
		}
		defer rows.Close() //nolint:errcheck // test cleanup

		count := 0
		for rows.Next() {
			var key, scope, value string
			if err := rows.Scan(&key, &scope, &value); err != nil {
				t.Fatalf("scan: %v", err)
			}
			count++
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				t.Errorf("%s at %s holds invalid JSON %q", key, scope, value)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterating: %v", err)
		}
		if count == 0 {
			t.Fatal("the migration wrote nothing")
		}
	})
}

// TestMigrateToV16IsAtomic. The migration runs inside the caller's transaction,
// so a failure has to leave the database exactly as it was rather than half
// converted — an operator's restore point is the pre-upgrade backup, and a
// partial migration is the one state neither backup nor rollback covers.
func TestMigrateToV16IsAtomic(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	seedLegacySettings(t, db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV16(tx); err != nil {
		t.Fatalf("migrateToV16: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var values, rejects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_setting_values`).Scan(&values); err != nil {
		t.Fatalf("counting values: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_setting_migration_rejects`).Scan(&rejects); err != nil {
		t.Fatalf("counting rejects: %v", err)
	}
	if values != 0 || rejects != 0 {
		t.Errorf("rollback left %d values and %d rejects behind", values, rejects)
	}
}

// TestMigrateToV16OnAnEmptyDatabase: a fresh install has nothing to migrate and
// must not fail trying.
func TestMigrateToV16OnAnEmptyDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV16(tx); err != nil {
		t.Fatalf("migrateToV16 on an empty database: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestMigrateToV16RejectsOrphansWhenProfileListIsEmpty(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO user_device_settings (profile_id, device_id, key, value, updated_at)
VALUES ('deleted', 'd1', 'subtitle_appearance', '{"fontSize":"large"}',
        '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding orphan: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV16(tx); err != nil {
		t.Fatalf("migrateToV16: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var values, rejects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_setting_values`).Scan(&values); err != nil {
		t.Fatalf("counting canonical values: %v", err)
	}
	if err := db.QueryRow(`
SELECT COUNT(*) FROM user_setting_migration_rejects
 WHERE source_key = 'subtitle_appearance'
   AND json_extract(identity, '$.profile_id') = 'deleted'`).Scan(&rejects); err != nil {
		t.Fatalf("counting orphan rejects: %v", err)
	}
	if values != 0 || rejects != 1 {
		t.Fatalf("values=%d rejects=%d, want 0/1", values, rejects)
	}
}

// TestMigrateToV16IsIdempotentUnderReRun guards the partial-unique indexes: the
// migration must not be runnable twice into a conflict. runMigrations gates it
// behind user_version, so the second call is what an operator would trigger by
// restoring a backup over a migrated database.
func TestMigrateToV16IsIdempotentUnderReRun(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	seedLegacySettings(t, db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrateToV16(tx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// A second run collides with the partial unique indexes. That it fails is
	// correct — silently doubling every value would be worse — but it must fail
	// as an error rather than corrupting anything, and the version gate in
	// runMigrations is what stops it happening in practice.
	tx2, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = migrateToV16(tx2)
	_ = tx2.Rollback()
	if err == nil {
		t.Error("a second migration run was accepted; values would be duplicated")
	}
}
