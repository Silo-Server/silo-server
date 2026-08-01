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

	"github.com/Silo-Server/silo-server/internal/jellycompat/displayprefs"
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

// TestPostgresDisplayPrefsMoveDoesNotDeleteConcurrentWrites reproduces the
// rolling-deploy window: the goose lock only excludes other migrators, so an
// old-binary app instance can commit a jellycompat row into user_settings
// while the move transaction sits between its SELECT and its deletes. Under
// READ COMMITTED each statement snapshots independently, so a pattern-based
// DELETE would see — and destroy — a row the SELECT never copied. The move
// must instead delete only the exact rows it read, leaving the late row
// stranded in user_settings for a re-run to pick up.
//
// The stall is real: an uncommitted conflicting insert on
// jellycompat_displayprefs blocks the move's ON CONFLICT insert, the late
// legacy row commits during the stall, and releasing the blocker lets the
// move finish.
func TestPostgresDisplayPrefsMoveDoesNotDeleteConcurrentWrites(t *testing.T) {
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

	var userID int
	err = pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ('displayprefs-racetest', 'displayprefs-racetest@example.com', 'x', 'user')
ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	for _, stmt := range []string{
		`DELETE FROM user_settings WHERE user_id = $1`,
		`DELETE FROM jellycompat_displayprefs WHERE user_id = $1`,
		`DELETE FROM user_setting_migration_rejects WHERE user_id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, userID); err != nil {
			t.Fatalf("clearing prior rows: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)`,
		userID, "jellycompat:displayprefs:usersettings:emby", `{"SortBy":"SortName"}`); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// The blocker: an uncommitted insert on the identity the seeded legacy row
	// maps to, so the move's copy insert waits on this transaction.
	blockerTx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	blockerReleased := false
	defer func() {
		if !blockerReleased {
			_ = blockerTx.Rollback()
		}
	}()
	if _, err := blockerTx.ExecContext(ctx, `
INSERT INTO jellycompat_displayprefs (user_id, prefs_id, client, value)
VALUES ($1, 'usersettings', 'emby', 'blocker')`, userID); err != nil {
		t.Fatalf("blocker insert: %v", err)
	}

	moveDone := make(chan error, 1)
	go func() {
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			moveDone <- fmt.Errorf("begin move: %w", err)
			return
		}
		if err := moveDisplayPrefs(ctx, tx); err != nil {
			_ = tx.Rollback()
			moveDone <- fmt.Errorf("moveDisplayPrefs: %w", err)
			return
		}
		moveDone <- tx.Commit()
	}()

	// Wait until the move transaction is provably parked on the blocker's
	// lock: its SELECT over user_settings has happened, its deletes have not.
	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM pg_stat_activity
 WHERE wait_event_type = 'Lock' AND query LIKE '%jellycompat_displayprefs%'`).
			Scan(&waiting); err != nil {
			t.Fatalf("polling pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("the move never blocked on the conflicting insert")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The old binary's handler commits a fresh DisplayPreferences row now —
	// after the move's SELECT, before its deletes.
	const lateKey = "jellycompat:displayprefs:late:acme"
	if _, err := pool.Exec(ctx, `
INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)`,
		userID, lateKey, `{"SortBy":"DateCreated"}`); err != nil {
		t.Fatalf("committing the late legacy row: %v", err)
	}

	// And it updates a row the move has already read: the delete predicate
	// pins the value the SELECT saw, so this newer write must survive too.
	const updatedKey = "jellycompat:displayprefs:usersettings:emby"
	const updatedValue = `{"SortBy":"Runtime"}`
	if _, err := pool.Exec(ctx, `
UPDATE user_settings SET value = $3 WHERE user_id = $1 AND key = $2`,
		userID, updatedKey, updatedValue); err != nil {
		t.Fatalf("committing the late update: %v", err)
	}

	blockerReleased = true
	if err := blockerTx.Rollback(); err != nil {
		t.Fatalf("releasing blocker: %v", err)
	}
	if err := <-moveDone; err != nil {
		t.Fatalf("move under contention: %v", err)
	}

	// The late row must not have been destroyed: it was never copied, so it
	// must still ride user_settings.
	var lateRows int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM user_settings WHERE user_id = $1 AND key = $2`,
		userID, lateKey).Scan(&lateRows); err != nil {
		t.Fatalf("counting the late row: %v", err)
	}
	if lateRows != 1 {
		var copied, rejected int
		_ = pool.QueryRow(ctx, `
SELECT COUNT(*) FROM jellycompat_displayprefs
 WHERE user_id = $1 AND prefs_id = 'late'`, userID).Scan(&copied)
		_ = pool.QueryRow(ctx, `
SELECT COUNT(*) FROM user_setting_migration_rejects
 WHERE user_id = $1 AND source_key = $2`, userID, lateKey).Scan(&rejected)
		t.Fatalf("late row gone from user_settings (copied=%d rejected=%d): "+
			"a concurrently committed row was deleted without being moved",
			copied, rejected)
	}

	// The concurrently updated row must also still be in user_settings, with
	// the newer value: the move copied the old value but its delete named that
	// old value, so it must not have matched the updated row.
	var survivingValue string
	if err := pool.QueryRow(ctx, `
SELECT value FROM user_settings WHERE user_id = $1 AND key = $2`,
		userID, updatedKey).Scan(&survivingValue); err != nil {
		t.Fatalf("updated legacy row gone from user_settings: %v — "+
			"a concurrently updated row was deleted with only its old value moved", err)
	}
	if survivingValue != updatedValue {
		t.Errorf("surviving legacy value = %q, want the late update %q",
			survivingValue, updatedValue)
	}

	// A re-run — which the stranded row exists to allow — picks it up.
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin re-run: %v", err)
	}
	if err := moveDisplayPrefs(ctx, tx); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit re-run: %v", err)
	}
	var copied int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM jellycompat_displayprefs
 WHERE user_id = $1 AND prefs_id = 'late' AND client = 'acme'`, userID).Scan(&copied); err != nil {
		t.Fatalf("counting the re-run copy: %v", err)
	}
	if copied != 1 {
		t.Errorf("re-run copied %d late rows, want 1", copied)
	}
}

// TestPostgresDisplayPrefsRollbackDoesNotDeleteConcurrentUpdates reproduces
// the inverse rolling-deploy window: a new-binary app instance updates a blob
// after the rollback has read it but before the rollback deletes it. The
// rollback may restore its older snapshot to user_settings, but it must leave
// the newer canonical value in place rather than deleting a value it never
// restored.
func TestPostgresDisplayPrefsRollbackDoesNotDeleteConcurrentUpdates(t *testing.T) {
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

	var userID int
	err = pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ('displayprefs-rollback-racetest', 'displayprefs-rollback-racetest@example.com', 'x', 'user')
ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	for _, stmt := range []string{
		`DELETE FROM user_settings WHERE user_id = $1`,
		`DELETE FROM jellycompat_displayprefs WHERE user_id = $1`,
		`DELETE FROM user_setting_migration_rejects WHERE user_id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, userID); err != nil {
			t.Fatalf("clearing prior rows: %v", err)
		}
	}

	const (
		prefsID       = "usersettings"
		client        = "emby"
		originalValue = `{"SortBy":"SortName"}`
		updatedValue  = `{"SortBy":"Runtime"}`
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO jellycompat_displayprefs (user_id, prefs_id, client, value)
VALUES ($1, $2, $3, $4)`, userID, prefsID, client, originalValue); err != nil {
		t.Fatalf("seeding moved row: %v", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Hold the destination identity open so the rollback stalls after reading
	// the canonical row but before its insert and delete.
	blockerTx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	blockerReleased := false
	defer func() {
		if !blockerReleased {
			_ = blockerTx.Rollback()
		}
	}()
	legacyKey := displayprefs.LegacyKey(prefsID, client)
	if _, err := blockerTx.ExecContext(ctx, `
INSERT INTO user_settings (user_id, key, value)
VALUES ($1, $2, 'blocker')`, userID, legacyKey); err != nil {
		t.Fatalf("blocker insert: %v", err)
	}

	rollbackDone := make(chan error, 1)
	go func() {
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			rollbackDone <- fmt.Errorf("begin rollback: %w", err)
			return
		}
		if err := unmoveDisplayPrefs(ctx, tx); err != nil {
			_ = tx.Rollback()
			rollbackDone <- fmt.Errorf("unmoveDisplayPrefs: %w", err)
			return
		}
		rollbackDone <- tx.Commit()
	}()

	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM pg_stat_activity
 WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO user_settings%'`).
			Scan(&waiting); err != nil {
			t.Fatalf("polling pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("the rollback never blocked on the conflicting insert")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := pool.Exec(ctx, `
UPDATE jellycompat_displayprefs SET value = $4
 WHERE user_id = $1 AND prefs_id = $2 AND client = $3`,
		userID, prefsID, client, updatedValue); err != nil {
		t.Fatalf("committing the concurrent canonical update: %v", err)
	}

	blockerReleased = true
	if err := blockerTx.Rollback(); err != nil {
		t.Fatalf("releasing blocker: %v", err)
	}
	if err := <-rollbackDone; err != nil {
		t.Fatalf("rollback under contention: %v", err)
	}

	var survivingValue string
	if err := pool.QueryRow(ctx, `
SELECT value FROM jellycompat_displayprefs
 WHERE user_id = $1 AND prefs_id = $2 AND client = $3`,
		userID, prefsID, client).Scan(&survivingValue); err != nil {
		t.Fatalf("concurrently updated canonical row was deleted: %v", err)
	}
	if survivingValue != updatedValue {
		t.Errorf("surviving canonical value = %q, want %q", survivingValue, updatedValue)
	}

	var restoredValue string
	if err := pool.QueryRow(ctx, `
SELECT value FROM user_settings WHERE user_id = $1 AND key = $2`,
		userID, legacyKey).Scan(&restoredValue); err != nil {
		t.Fatalf("reading restored legacy snapshot: %v", err)
	}
	if restoredValue != originalValue {
		t.Errorf("restored legacy value = %q, want the rollback snapshot %q",
			restoredValue, originalValue)
	}
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
