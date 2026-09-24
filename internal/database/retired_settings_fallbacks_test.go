package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestPostgresRetiredSettingsFallbacks runs the registered migration against a
// real database, then calls it directly over seeded legacy rows: the goose
// version gate has already consumed it by then, so the direct call is how the
// upgrade path meets data. The conversion rule is unit-tested in
// internal/settingsmigrate; this covers the SQL: which profiles get a row,
// that stored rows win, and that a second run changes nothing.
func TestPostgresRetiredSettingsFallbacks(t *testing.T) {
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
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	seedUser := func(t *testing.T, legacy map[string]string, profiles ...string) int {
		t.Helper()
		var userID int
		name := fmt.Sprintf("retired-fallback-%d", time.Now().UnixNano())
		if err := pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ($1, $2, 'x', 'user') RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
			t.Fatalf("seeding user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
		for _, profileID := range profiles {
			if _, err := pool.Exec(ctx,
				`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $1)`, profileID, userID); err != nil {
				t.Fatalf("seeding profile %s: %v", profileID, err)
			}
		}
		for key, value := range legacy {
			if _, err := pool.Exec(ctx,
				`INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)`, userID, key, value); err != nil {
				t.Fatalf("seeding legacy %s: %v", key, err)
			}
		}
		return userID
	}
	storeRow := func(t *testing.T, userID int, profileID, key, value string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
VALUES ($1, $2, 'profile', $3, $4::jsonb)`, userID, key, profileID, value); err != nil {
			t.Fatalf("storing %s for %s: %v", key, profileID, err)
		}
	}
	canonical := func(t *testing.T, userID int, profileID, key string) string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `
SELECT value::text FROM user_setting_values
 WHERE user_id = $1 AND profile_id = $2 AND key = $3 AND scope = 'profile'`,
			userID, profileID, key).Scan(&value)
		if err != nil {
			return ""
		}
		return value
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	run := func(t *testing.T) {
		t.Helper()
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		if err := materializeRetiredSettingsFallbacks(ctx, tx); err != nil {
			t.Fatalf("materializeRetiredSettingsFallbacks: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	household := seedUser(t, map[string]string{
		"disabled_library_ids": `[0,4,4,7]`,
		"next_up_mode":         "separate",
	}, "p-new", "p-stored", "p-reset")
	// p-stored chose its own values after the cutover; the fallback never
	// overrode a stored row, so the migration must not either.
	storeRow(t, household, "p-stored", "ui.disabled_library_ids", `[9]`)
	storeRow(t, household, "p-stored", "ui.next_up_mode", `"combined"`)
	unconvertible := seedUser(t, map[string]string{"next_up_mode": "sideways"}, "p-only")
	noLegacy := seedUser(t, nil, "p-plain")

	run(t)

	t.Run("profiles without a row get the value the fallback returned", func(t *testing.T) {
		for _, profileID := range []string{"p-new", "p-reset"} {
			if got := canonical(t, household, profileID, "ui.disabled_library_ids"); got != `[4, 7]` {
				t.Errorf("%s hidden libraries = %q, want [4, 7]", profileID, got)
			}
			if got := canonical(t, household, profileID, "ui.next_up_mode"); got != `"separate"` {
				t.Errorf("%s next-up mode = %q, want \"separate\"", profileID, got)
			}
		}
	})

	t.Run("stored rows win", func(t *testing.T) {
		if got := canonical(t, household, "p-stored", "ui.disabled_library_ids"); got != `[9]` {
			t.Errorf("stored hidden libraries = %q, want [9]", got)
		}
		if got := canonical(t, household, "p-stored", "ui.next_up_mode"); got != `"combined"` {
			t.Errorf("stored next-up mode = %q, want \"combined\"", got)
		}
	})

	t.Run("nothing is written without a convertible legacy value", func(t *testing.T) {
		if got := canonical(t, unconvertible, "p-only", "ui.next_up_mode"); got != "" {
			t.Errorf("unconvertible legacy value wrote %q", got)
		}
		if got := canonical(t, noLegacy, "p-plain", "ui.disabled_library_ids"); got != "" {
			t.Errorf("account without legacy rows got hidden libraries %q", got)
		}
	})

	t.Run("legacy rows are kept", func(t *testing.T) {
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_settings WHERE user_id = $1`, household).Scan(&count); err != nil {
			t.Fatalf("counting legacy rows: %v", err)
		}
		if count != 2 {
			t.Errorf("legacy rows = %d, want 2 left in place", count)
		}
	})

	t.Run("a second run is a no-op", func(t *testing.T) {
		snapshot := func() string {
			var out sql.NullString
			if err := pool.QueryRow(ctx, `
SELECT string_agg(profile_id || ':' || key || '=' || value::text || '@' || revision, ',' ORDER BY profile_id, key)
  FROM user_setting_values WHERE user_id = $1`, household).Scan(&out); err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			return out.String
		}
		before := snapshot()
		run(t)
		if after := snapshot(); after != before {
			t.Errorf("second run changed rows:\nbefore %s\nafter  %s", before, after)
		}
	})
}
