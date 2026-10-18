package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// TestPostgresPersonalListPage runs the keyset favorites/watchlist paging
// and membership-change conformance tests against the Postgres backend.
// Skips unless SILO_TEST_DATABASE_URL is set.
func TestPostgresPersonalListPage(t *testing.T) {
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

	newListStore := func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-listpage-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM user_favorites WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_watchlist WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		})
		return newStore(pool, userID)
	}
	storetest.RunPersonalListPage(t, newListStore)
	storetest.RunPersonalListMembership(t, newListStore)
	storetest.RunNotInterested(t, newListStore)
}

// TestPostgresProgressPage runs the keyset progress paging conformance test
// against the Postgres backend. Skips unless SILO_TEST_DATABASE_URL is set.
func TestPostgresProgressPage(t *testing.T) {
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

	storetest.RunProgressPage(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-progresspage-%d", time.Now().UnixNano()),
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

// TestPostgresDeleteProfileRemovesProfileState runs the profile-deletion
// conformance test against the Postgres backend. Skips unless
// SILO_TEST_DATABASE_URL is set.
func TestPostgresDeleteProfileRemovesProfileState(t *testing.T) {
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

	storetest.RunDeleteProfileRemovesProfileState(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-profiledelete-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		// Deleting the user cascades to every user-scoped table.
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
		return newStore(pool, userID)
	}, true)
}

// profileTablesClearedElsewhere are the user tables with a profile_id that
// DeleteProfile leaves to another step, with the step.
var profileTablesClearedElsewhere = map[string]string{
	"user_devices":   "the profile handler's device-library purge (downloads.Repository.PurgeProfileDevices)",
	"user_downloads": "the profile handler's device-library purge (downloads.Repository.PurgeProfileDevices)",
}

// Every user-store table (user_*) keyed by (user_id, profile_id) is cleared
// when a profile is deleted: by profileCascadeTables, by a foreign key to
// user_profiles that cascades, or by a step named in
// profileTablesClearedElsewhere. A new table fails this test until it is given
// one of the three. Tables other packages own (ebook reading data, downloads,
// notifications and the like) are purged by those packages' own steps.
func TestPostgresDeleteProfileCoversEveryProfileTable(t *testing.T) {
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

	rows, err := pool.Query(ctx, `
		SELECT c.table_name
		FROM   information_schema.columns c
		JOIN   information_schema.tables t USING (table_schema, table_name)
		WHERE  c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND  c.table_name LIKE 'user\_%' AND c.column_name = 'profile_id'
		  AND  EXISTS (SELECT 1 FROM information_schema.columns u
		               WHERE u.table_schema = 'public' AND u.table_name = c.table_name AND u.column_name = 'user_id')
		  AND  NOT EXISTS (
		         SELECT 1 FROM pg_constraint fk
		         WHERE fk.contype = 'f' AND fk.confdeltype = 'c'
		           AND fk.conrelid = format('public.%I', c.table_name)::regclass
		           AND fk.confrelid = 'public.user_profiles'::regclass)
		ORDER  BY 1`)
	if err != nil {
		t.Fatalf("list profile tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list profile tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("found no profile tables; the schema query is wrong")
	}
	for _, table := range tables {
		if slices.Contains(profileCascadeTables, table) {
			continue
		}
		if _, ok := profileTablesClearedElsewhere[table]; ok {
			continue
		}
		t.Errorf("%s has a profile_id but nothing removes its rows when the profile is deleted: add it to profileCascadeTables, or to profileTablesClearedElsewhere with the step that does", table)
	}
}

// Profiles saving section overrides for one scope at the same time each keep
// their own: every writer reads, merges and writes the whole value under one
// lock, so no write is lost.
func TestPostgresConcurrentSectionOverrideSaves(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("conf-overrides-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	store := newStore(pool, userID)

	const profiles = 8
	var wg sync.WaitGroup
	errs := make(chan error, profiles)
	for i := range profiles {
		wg.Go(func() {
			errs <- store.SaveSectionOverrides(ctx, fmt.Sprintf("p%d", i), "home", "", []userstore.SectionOverride{{SectionID: "default-recently-added", Hidden: true}})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SaveSectionOverrides: %v", err)
		}
	}
	for i := range profiles {
		got, err := store.ListSectionOverrides(ctx, fmt.Sprintf("p%d", i), "home", "")
		if err != nil || len(got) != 1 {
			t.Fatalf("profile p%d overrides = %d, %v; want its own one", i, len(got), err)
		}
	}
}

// Two profiles with legacy visibility rows on each other's collections can
// be deleted at the same time: each delete locks every collection it touches,
// in id order, before advancing any revision, so neither waits on the other
// in a cycle.
func TestPostgresConcurrentProfileDeletesWithCrossVisibility(t *testing.T) {
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

	for round := range 20 {
		var userID int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-crossdelete-%d-%d", round, time.Now().UnixNano())).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
		store := newStore(pool, userID)
		profiles := []string{"keep", "a", "b"}
		for _, id := range profiles {
			if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
				t.Fatalf("CreateProfile %s: %v", id, err)
			}
		}
		collections := map[string]string{}
		for _, id := range []string{"a", "b"} {
			c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: id, Name: "Picks of " + id})
			if err != nil {
				t.Fatalf("CreateCollection %s: %v", id, err)
			}
			collections[id] = c.ID
		}
		// Legacy allow-list rows: each profile listed on the other's collection.
		for viewer, owner := range map[string]string{"a": "b", "b": "a"} {
			if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id) VALUES ($1, $2, $3)`,
				userID, collections[owner], viewer); err != nil {
				t.Fatalf("seed visibility: %v", err)
			}
		}

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, id := range []string{"a", "b"} {
			wg.Go(func() { errs <- newStore(pool, userID).DeleteProfile(ctx, id) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent DeleteProfile: %v", round, err)
			}
		}
	}
}

// Deleting a profile and deleting another profile's collection that lists it
// can run at the same time: both lock the collection row before its
// revision row.
func TestPostgresConcurrentProfileAndCollectionDeletes(t *testing.T) {
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

	for round := range 20 {
		var userID int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-mixeddelete-%d-%d", round, time.Now().UnixNano())).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
		store := newStore(pool, userID)
		for _, id := range []string{"owner", "listed"} {
			if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
				t.Fatalf("CreateProfile %s: %v", id, err)
			}
		}
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Owner's picks"})
		if err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		if err := store.AddCollectionItem(ctx, c.ID, "movie:pick", 0); err != nil {
			t.Fatalf("AddCollectionItem: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO user_personal_collection_profiles (user_id, collection_id, profile_id) VALUES ($1, $2, 'listed')`,
			userID, c.ID); err != nil {
			t.Fatalf("seed visibility: %v", err)
		}

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Go(func() { errs <- newStore(pool, userID).DeleteProfile(ctx, "listed") })
		wg.Go(func() { errs <- newStore(pool, userID).DeleteCollection(ctx, c.ID) })
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent delete: %v", round, err)
			}
		}
	}
}

// Deleting a profile while it reorders its own collections cannot deadlock:
// both lock the collections in id order. The reorder may find the
// collections gone, which is not a deadlock.
func TestPostgresConcurrentProfileDeleteAndCollectionReorder(t *testing.T) {
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

	for round := range 20 {
		var userID int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-reorderdelete-%d-%d", round, time.Now().UnixNano())).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
		store := newStore(pool, userID)
		for _, id := range []string{"keep", "gone"} {
			if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
				t.Fatalf("CreateProfile %s: %v", id, err)
			}
		}
		var ids []string
		for i := range 6 {
			c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "gone", Name: fmt.Sprintf("List %d", i)})
			if err != nil {
				t.Fatalf("CreateCollection: %v", err)
			}
			ids = append([]string{c.ID}, ids...)
		}

		var wg sync.WaitGroup
		var deleteErr, reorderErr error
		wg.Go(func() { deleteErr = newStore(pool, userID).DeleteProfile(ctx, "gone") })
		wg.Go(func() { reorderErr = newStore(pool, userID).ReorderCollections(ctx, "gone", ids) })
		wg.Wait()
		if deleteErr != nil {
			t.Fatalf("round %d: DeleteProfile: %v", round, deleteErr)
		}
		var pgErr *pgconn.PgError
		if errors.As(reorderErr, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("round %d: ReorderCollections deadlocked: %v", round, reorderErr)
		}
	}
}
