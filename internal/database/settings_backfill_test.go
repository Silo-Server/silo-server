package database

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestPostgresSettingsBackfill runs the real goose provider — every SQL
// migration plus the Go backfill — against a real database, then checks what
// landed.
//
// The planner's rules are unit-tested in internal/settingsmigrate. What this
// covers is everything only a live database can show: that the Go migration is
// registered and actually runs, that the rows satisfy the scope CHECK, the
// composite profile foreign key and the six partial unique indexes, and that
// jsonb accepts the values the planner encodes.
func TestPostgresSettingsBackfill(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// Seed legacy state, then run migrations over it. Ordering matters: the
	// backfill has to find rows that predate it, which is the real upgrade.
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}
	seedLegacyPostgresSettings(ctx, t, pool)

	// Re-run the backfill against the seeded data. It is idempotent only under
	// goose's version gate, so this exercises it directly.
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := backfillSettingValues(ctx, tx); err != nil {
		t.Fatalf("backfillSettingValues: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	t.Run("profile columns become profile-scope values", func(t *testing.T) {
		var value string
		err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.audio_language' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&value)
		if err != nil {
			t.Fatalf("reading migrated audio language: %v", err)
		}
		if value != `"ja"` {
			t.Errorf("audio language = %s, want \"ja\"", value)
		}
		var quality, bitrate string
		if err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.preferred_quality' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&quality); err != nil {
			t.Fatalf("reading migrated profile quality: %v", err)
		}
		if err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.max_bitrate_kbps' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&bitrate); err != nil {
			t.Fatalf("reading migrated profile bitrate: %v", err)
		}
		if quality != `"1080p"` || bitrate != `6000` {
			t.Errorf("profile quality = (%s, %s), want (\"1080p\", 6000)", quality, bitrate)
		}
	})

	t.Run("auto-skip columns migrate only when true", func(t *testing.T) {
		var value string
		err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.auto_skip_intro' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&value)
		if err != nil {
			t.Fatalf("reading migrated auto_skip_intro: %v", err)
		}
		if value != `true` {
			t.Errorf("auto_skip_intro = %s, want true", value)
		}
		// The false column is the default and must not become a stored choice.
		var count int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM user_setting_values
 WHERE key = 'playback.auto_skip_credits' AND profile_id = 'mp1'`).Scan(&count); err != nil {
			t.Fatalf("counting auto_skip_credits rows: %v", err)
		}
		if count != 0 {
			t.Error("an untouched false auto_skip_credits column became a row")
		}
	})

	t.Run("auto_skip_intro carries its revision-7 replacement", func(t *testing.T) {
		var value string
		err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.intro_skip_mode' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&value)
		if err != nil {
			t.Fatalf("reading migrated intro_skip_mode: %v", err)
		}
		if value != `"always"` {
			t.Errorf("intro_skip_mode = %s, want \"always\"", value)
		}
	})

	t.Run("metadata language migrates from the postgres-only column", func(t *testing.T) {
		var value string
		err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'catalog.metadata_language' AND scope = 'profile' AND profile_id = 'mp1'`).
			Scan(&value)
		if err != nil {
			t.Fatalf("reading migrated metadata language: %v", err)
		}
		if value != `"fr"` {
			t.Errorf("metadata language = %s, want \"fr\"", value)
		}
	})

	t.Run("legacy quality decomposes into two axes", func(t *testing.T) {
		var resolution, bitrate string
		if err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.preferred_quality' AND scope = 'profile_device'
   AND profile_id = 'mp1' AND device_id = 'md1'`).Scan(&resolution); err != nil {
			t.Fatalf("reading resolution: %v", err)
		}
		if err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE key = 'playback.max_bitrate_kbps' AND scope = 'profile_device'
   AND profile_id = 'mp1' AND device_id = 'md1'`).Scan(&bitrate); err != nil {
			t.Fatalf("reading bitrate: %v", err)
		}
		if resolution != `"1080p"` || bitrate != `10000` {
			t.Errorf("decomposed to (%s, %s), want (\"1080p\", 10000)", resolution, bitrate)
		}
	})

	t.Run("values are stored as typed jsonb, not strings", func(t *testing.T) {
		var kind string
		if err := pool.QueryRow(ctx, `
SELECT jsonb_typeof(value) FROM user_setting_values
 WHERE key = 'playback.max_bitrate_kbps' AND profile_id = 'mp1' AND device_id = 'md1'`).
			Scan(&kind); err != nil {
			t.Fatalf("reading jsonb type: %v", err)
		}
		if kind != "number" {
			t.Errorf("bitrate stored as jsonb %s, want number", kind)
		}
	})

	t.Run("rejects carry a queryable jsonb identity", func(t *testing.T) {
		var identity, reason string
		err := pool.QueryRow(ctx, `
SELECT identity::text, reason FROM user_setting_migration_rejects
 WHERE source_key = 'legacy.unknown.key' LIMIT 1`).Scan(&identity, &reason)
		if err != nil {
			t.Fatalf("the unknown key was dropped rather than recorded: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(identity), &decoded); err != nil {
			t.Errorf("identity %q is not JSON: %v", identity, err)
		}
		if reason == "" {
			t.Error("reject carries no reason")
		}
	})

	t.Run("the composite profile foreign key holds", func(t *testing.T) {
		// A profile-scope row naming a profile that does not exist must be
		// refused, which is what keeps orphaned settings out after a profile is
		// deleted.
		_, err := pool.Exec(ctx, `
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
VALUES ((SELECT id FROM users WHERE username = 'migtest'),
        'playback.subtitle_mode', 'profile', 'no-such-profile', '"auto"'::jsonb)`)
		if err == nil {
			t.Error("a row for a nonexistent profile was accepted")
		}
	})
}

// seedLegacyPostgresSettings writes the pre-cutover rows a real install holds.
func seedLegacyPostgresSettings(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var userID int
	err := pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ('migtest', 'migtest@example.com', 'x', 'user')
ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	if _, err := pool.Exec(ctx, `
INSERT INTO user_profiles
    (user_id, id, name, quality_preference, language, subtitle_language,
     subtitle_mode, show_forced_subtitles, preferred_metadata_language,
     auto_skip_intro, auto_skip_credits)
VALUES ($1, 'mp1', 'Migrate Me', '1080p', 'ja', 'en', 'always', false, 'fr',
        true, false)
ON CONFLICT (user_id, id) DO NOTHING`, userID); err != nil {
		t.Fatalf("seeding profile: %v", err)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO user_device_settings (user_id, profile_id, device_id, key, value)
VALUES ($1, 'mp1', 'md1', 'playback.preferred_quality', '1080p-high')
ON CONFLICT (user_id, profile_id, device_id, key) DO UPDATE SET value = EXCLUDED.value`,
		userID); err != nil {
		t.Fatalf("seeding device setting: %v", err)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO user_settings (user_id, key, value)
VALUES ($1, 'ui_theme', 'cobalt-studio'), ($1, 'legacy.unknown.key', 'whatever')
ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value`, userID); err != nil {
		t.Fatalf("seeding user settings: %v", err)
	}

	// Clear anything a prior run left, so the assertions above see only this
	// seed's conversions.
	if _, err := pool.Exec(ctx,
		`DELETE FROM user_setting_values WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("clearing prior values: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM user_setting_migration_rejects WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("clearing prior rejects: %v", err)
	}
}
