package database

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestPostgresDisplayPrefsMove runs the real goose provider — which registers
// the Go move migration — against a real database, then exercises the move
// directly over seeded legacy rows. The parsing rules are unit-tested in
// internal/jellycompat/displayprefs; this covers what only a live database
// shows: registration, the table's constraints, verbatim copy through real
// text columns, and that re-running or rolling back behaves.
func TestPostgresDisplayPrefsMove(t *testing.T) {
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

	// Migrate first, then seed legacy rows and run the move directly: the
	// goose version gate has already consumed the registered migration, so
	// calling the function is how the upgrade path is exercised against data.
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}
	userID := seedLegacyDisplayPrefsRows(ctx, t, pool)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	runMove := func(fn func(context.Context, *sql.Tx) error, label string) {
		t.Helper()
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin %s: %v", label, err)
		}
		if err := fn(ctx, tx); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", label, err)
		}
	}
	runMove(moveDisplayPrefs, "moveDisplayPrefs")

	countLegacy := func() int {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM user_settings
 WHERE user_id = $1 AND key LIKE 'jellycompat:%'`, userID).Scan(&count); err != nil {
			t.Fatalf("counting legacy rows: %v", err)
		}
		return count
	}

	t.Run("blobs move verbatim", func(t *testing.T) {
		var value string
		if err := pool.QueryRow(ctx, `
SELECT value FROM jellycompat_displayprefs
 WHERE user_id = $1 AND prefs_id = 'usersettings' AND client = 'emby'`, userID).
			Scan(&value); err != nil {
			t.Fatalf("reading moved blob: %v", err)
		}
		if value != `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}` {
			t.Errorf("blob = %q, want it byte-for-byte", value)
		}

		// The empty client is a real identity and must survive the key split.
		if err := pool.QueryRow(ctx, `
SELECT value FROM jellycompat_displayprefs
 WHERE user_id = $1 AND prefs_id = 'f137a2dd' AND client = ''`, userID).
			Scan(&value); err != nil {
			t.Fatalf("reading empty-client blob: %v", err)
		}
	})

	t.Run("user_settings keeps no jellycompat tenants", func(t *testing.T) {
		if count := countLegacy(); count != 0 {
			t.Errorf("%d jellycompat rows still ride user_settings", count)
		}
		var theme string
		if err := pool.QueryRow(ctx, `
SELECT value FROM user_settings WHERE user_id = $1 AND key = 'ui_theme'`, userID).
			Scan(&theme); err != nil || theme != "cobalt-studio" {
			t.Errorf("ui_theme = (%q, %v); the move touched a non-jellycompat row", theme, err)
		}
	})

	t.Run("unparseable rows are recorded, not silently deleted", func(t *testing.T) {
		var value, reason string
		err := pool.QueryRow(ctx, `
SELECT value, reason FROM user_setting_migration_rejects
 WHERE user_id = $1 AND source_table = 'user_settings' AND source_key = 'jellycompat:stray'`,
			userID).Scan(&value, &reason)
		if err != nil {
			t.Fatalf("the stray row was dropped rather than recorded: %v", err)
		}
		if value != "not a displayprefs blob" || reason == "" {
			t.Errorf("reject = (%q, %q); the original value and a reason must survive", value, reason)
		}
	})

	t.Run("a second run is a no-op", func(t *testing.T) {
		counts := func() (blobs, rejects int) {
			t.Helper()
			if err := pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM jellycompat_displayprefs WHERE user_id = $1`, userID).
				Scan(&blobs); err != nil {
				t.Fatalf("counting blobs: %v", err)
			}
			if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM user_setting_migration_rejects
 WHERE user_id = $1 AND source_key LIKE 'jellycompat:%'`, userID).Scan(&rejects); err != nil {
				t.Fatalf("counting rejects: %v", err)
			}
			return blobs, rejects
		}
		blobsBefore, rejectsBefore := counts()

		runMove(moveDisplayPrefs, "moveDisplayPrefs re-run")

		blobsAfter, rejectsAfter := counts()
		if blobsAfter != blobsBefore || rejectsAfter != rejectsBefore {
			t.Errorf("re-run changed counts: blobs %d→%d, rejects %d→%d",
				blobsBefore, blobsAfter, rejectsBefore, rejectsAfter)
		}
	})

	t.Run("rollback restores the legacy rows", func(t *testing.T) {
		runMove(unmoveDisplayPrefs, "unmoveDisplayPrefs")

		if count := countLegacy(); count != 3 {
			t.Errorf("rollback restored %d legacy rows, want 3", count)
		}
		var value string
		if err := pool.QueryRow(ctx, `
SELECT value FROM user_settings
 WHERE user_id = $1 AND key = 'jellycompat:displayprefs:usersettings:emby'`, userID).
			Scan(&value); err != nil {
			t.Fatalf("reading restored blob row: %v", err)
		}
		if value != `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}` {
			t.Errorf("restored blob = %q, want it byte-for-byte", value)
		}

		var blobs int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM jellycompat_displayprefs WHERE user_id = $1`, userID).
			Scan(&blobs); err != nil {
			t.Fatalf("counting blobs after rollback: %v", err)
		}
		if blobs != 0 {
			t.Errorf("rollback left %d rows in jellycompat_displayprefs", blobs)
		}
	})
}

// seedLegacyDisplayPrefsRows writes the pre-cutover user_settings rows: two
// handler-written DisplayPreferences blobs, one jellycompat row only the legacy
// settings API's removed unknown-key carve-out could have produced, and a real
// user setting that must not move.
func seedLegacyDisplayPrefsRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var userID int
	err := pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ('displayprefs-migtest', 'displayprefs-migtest@example.com', 'x', 'user')
ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	// Clear anything a prior run left so the assertions see only this seed.
	for _, stmt := range []string{
		`DELETE FROM user_settings WHERE user_id = $1`,
		`DELETE FROM jellycompat_displayprefs WHERE user_id = $1`,
		`DELETE FROM user_setting_migration_rejects WHERE user_id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, userID); err != nil {
			t.Fatalf("clearing prior rows: %v", err)
		}
	}

	for key, value := range map[string]string{
		"jellycompat:displayprefs:usersettings:emby": `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}`,
		"jellycompat:displayprefs:f137a2dd:":         `{"SortBy":"DateCreated"}`,
		"jellycompat:stray":                          "not a displayprefs blob",
		"ui_theme":                                   "cobalt-studio",
	} {
		if _, err := pool.Exec(ctx, `
INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value`,
			userID, key, value); err != nil {
			t.Fatalf("seeding user_settings %s: %v", key, err)
		}
	}
	return userID
}
