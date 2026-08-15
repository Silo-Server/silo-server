package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

// TestPostgresProgressSince runs the offline-sync progress-reconciliation
// conformance test (invariant 1) against the Postgres backend, exercising the
// user_watch_progress synced_seq trigger and event_at LWW comparison. Skips
// unless SILO_TEST_DATABASE_URL is set and the migration is applied.
func TestPostgresProgressSince(t *testing.T) {
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

	var col *string
	err = pool.QueryRow(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'user_watch_progress' AND column_name = 'synced_seq'`).Scan(&col)
	if errors.Is(err, pgx.ErrNoRows) || col == nil {
		t.Skip("watch_progress offline-sync migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check migration: %v", err)
	}

	storetest.RunProgressSince(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		})
		return newStore(pool, userID)
	})
}

// TestPostgresMarkWatchedBatch runs the batch mark-watched conformance test
// (series/season mark-watched) against the Postgres backend. The per-user
// SQLite backend runs the same suite in internal/userdb, which is what keeps
// the two transactional implementations from drifting. Skips unless
// SILO_TEST_DATABASE_URL is set.
func TestPostgresMarkWatchedBatch(t *testing.T) {
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

	storetest.RunMarkWatchedBatch(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-markwatched-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_history_hidden_items WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		})
		return newStore(pool, userID)
	})
}

// TestPostgresSettingValues runs the canonical settings-contract storage
// conformance tests against the Postgres backend. The per-user SQLite backend
// runs the same suite in internal/userdb, which is what keeps the two from
// drifting on scope identity, partial uniqueness and delete behavior. Skips
// unless SILO_TEST_DATABASE_URL is set and the migration is applied.
func TestPostgresSettingValues(t *testing.T) {
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

	var table *string
	err = pool.QueryRow(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'user_setting_values'`).Scan(&table)
	if errors.Is(err, pgx.ErrNoRows) || table == nil {
		t.Skip("settings contract storage migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check migration: %v", err)
	}

	storetest.RunSettingValues(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-settings-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		// user_setting_values and user_setting_mutations cascade from users.
		// The cleanup asserts the cascade actually fired: silently discarding
		// this error would let a dropped FK leak seeded rows into the shared
		// test database run after run with no failure signal.
		t.Cleanup(func() {
			deleteUserAssertingCascade(t, pool, userID,
				"user_setting_values", "user_setting_mutations", "user_profiles")
		})
		return newStore(pool, userID)
	})
}

// deleteUserAssertingCascade removes a seeded user and fails the test if the
// delete errors or any named child table still holds the user's rows — i.e.
// if an ON DELETE CASCADE these cleanups rely on ever goes missing.
func deleteUserAssertingCascade(t *testing.T, pool *pgxpool.Pool, userID int, childTables ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Errorf("cleanup: deleting user %d: %v", userID, err)
		return
	}
	for _, table := range childTables {
		var leaked int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE user_id = $1`, userID,
		).Scan(&leaked); err != nil {
			t.Errorf("cleanup: counting %s rows for user %d: %v", table, userID, err)
			continue
		}
		if leaked != 0 {
			t.Errorf("cleanup: %d %s rows survived deleting user %d — the ON DELETE CASCADE this cleanup relies on is gone",
				leaked, table, userID)
		}
	}
}

// TestPostgresJellycompatDisplayPrefs runs the Jellyfin DisplayPreferences
// storage conformance tests against the Postgres backend; the per-user SQLite
// backend runs the same suite in internal/userdb. Skips unless
// SILO_TEST_DATABASE_URL is set and the migration is applied.
func TestPostgresJellycompatDisplayPrefs(t *testing.T) {
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

	var table *string
	err = pool.QueryRow(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'jellycompat_displayprefs'`).Scan(&table)
	if errors.Is(err, pgx.ErrNoRows) || table == nil {
		t.Skip("jellycompat_displayprefs migration has not been applied")
	}
	if err != nil {
		t.Fatalf("check migration: %v", err)
	}

	storetest.RunJellycompatDisplayPrefs(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-displayprefs-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		// jellycompat_displayprefs cascades from users; assert it, as above.
		t.Cleanup(func() {
			deleteUserAssertingCascade(t, pool, userID, "jellycompat_displayprefs")
		})
		return newStore(pool, userID)
	})
}

func TestPostgresCollectionSortPreferences(t *testing.T) {
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

	var tableName *string
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.user_collection_sort_preferences')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check preference table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("collection sort preference migration has not been applied")
	}

	storetest.RunCollectionSortPreferences(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("sort-pref-conf-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() {
			deleteUserAssertingCascade(t, pool, userID,
				"user_collection_sort_preferences", "user_profiles")
		})
		return newStore(pool, userID)
	})
}
