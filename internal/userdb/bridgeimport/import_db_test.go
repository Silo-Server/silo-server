package bridgeimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/progresssync"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport/testdata"
	"github.com/Silo-Server/silo-server/migrations"
)

func accountImportPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_IMPORT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_IMPORT_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err := pool.QueryRow(t.Context(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "silo_import" {
		t.Fatal("requires the dedicated synthetic import database")
	}
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatal(err)
	}
	return pool
}

type importFixture struct {
	path     string
	identity BackupIdentity
	folder   int
	item     string
}

func newImportFixture(t *testing.T, pool *pgxpool.Pool) importFixture {
	t.Helper()
	f := importFixture{item: "bridge-" + uuid.NewString()}
	f.identity.InstallationID = uuid.NewString()
	if _, err := pool.Exec(t.Context(), "INSERT INTO server_settings(key,value) VALUES('diagnostics.server_instance_id',$1) ON CONFLICT(key) DO NOTHING", f.identity.InstallationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT value FROM server_settings WHERE key='diagnostics.server_instance_id'").Scan(&f.identity.InstallationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", f.item).Scan(&f.identity.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO media_folders(name,type) VALUES($1,'movies') RETURNING id", f.item).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Synthetic import item')", f.item); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"user_collection_revisions", "user_collection_order_revisions"} {
			_, _ = pool.Exec(ctx, "DELETE FROM "+table+" WHERE user_id=$1", f.identity.AccountID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", f.identity.AccountID)
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id=$1", f.item)
		_, _ = pool.Exec(ctx, "DELETE FROM media_folders WHERE id=$1", f.folder)
	})
	f.path = filepath.Join(t.TempDir(), strconv.Itoa(f.identity.AccountID)+".db")
	source, err := testdata.NewSource(f.path, f.identity.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, id := range []string{"parent", "child"} {
		if err := userdb.CreateProfile(source.DB, userdb.Profile{ID: id, Name: id, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO profile_allowed_libraries VALUES('child',?)`, []any{f.folder}},
		{`INSERT INTO watch_progress(profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at) VALUES('parent',?,10,100,0,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO watch_history VALUES('history','parent',?,'2026-01-01T00:00:00Z',100,1,'legacy','{}')`, []any{f.item}},
		{`INSERT INTO hidden_history_items VALUES('parent',?,'2025-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO favorites VALUES('parent',?,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO watchlist VALUES('parent',?,'2026-01-01T00:00:00Z',4)`, []any{f.item}},
		{`INSERT INTO home_item_dismissals VALUES('parent','continue_watching',?,NULL,NULL,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO personal_collections VALUES('collection','parent','parent','Synthetic collection','manual',1,'{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO personal_collection_items VALUES('collection',?,3,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO personal_collection_profiles VALUES('collection','child')`, nil},
		{`INSERT INTO collection_sort_preferences VALUES('parent','user','collection','title','asc','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO audio_preferences VALUES('parent',?,0,'en','{}','2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO subtitle_preferences VALUES('parent',?,'en',0,'','off','{}',NULL,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO series_playback_preferences VALUES('parent',?,NULL,0,NULL,'2026-01-01T00:00:00Z')`, []any{f.item}},
		{`INSERT INTO library_playback_preferences VALUES('parent',?,NULL,NULL,NULL,0,'2026-01-01T00:00:00Z')`, []any{f.folder}},
		{`INSERT INTO profile_onboarding VALUES('parent','tour','step',NULL,NULL,'2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO user_settings VALUES('opaque','synthetic opaque value')`, nil},
		{`INSERT INTO user_device_settings VALUES('parent','device','opaque','synthetic value','Device','web','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO user_devices VALUES('parent','device','Device','web','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO user_setting_values(key,scope,value,revision,created_at,updated_at) VALUES('synthetic','account','null',7,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO user_setting_mutations VALUES('mutation','synthetic-hash','{"revision":7}','2026-01-01T00:00:00Z','2099-01-01T00:00:00Z')`, nil},
		{`INSERT INTO user_setting_migration_rejects(source_table,source_key,identity,value,reason,recorded_at) VALUES('user_settings','bad','{}','synthetic rejected value','invalid','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO jellycompat_displayprefs VALUES('prefs','client','opaque non-JSON','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO profile_section_overrides(id,profile_id,scope,removed,created_at,updated_at) VALUES('override','child','home',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO personal_collection_revisions VALUES('deleted',87)`, nil},
	}

	statements = append(statements, struct {
		query string
		args  []any
	}{`INSERT INTO user_setting_values(key,scope,profile_id,client_family,device_id,library_id,series_id,value,revision,created_at,updated_at) VALUES
 ('playback.audio_language','profile','parent',NULL,NULL,NULL,NULL,'"fr"',8,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('synthetic','profile_client','parent','web',NULL,NULL,NULL,'false',9,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('synthetic','profile_device','parent',NULL,'device',NULL,NULL,'0',10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('synthetic','profile_library','parent',NULL,NULL,?,NULL,'[]',11,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
 ('synthetic','profile_series','parent',NULL,NULL,NULL,?,'{}',12,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, []any{f.folder, f.item}})
	for _, statement := range statements {
		if _, err := source.DB.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	f.identity.SourceSHA256 = sourceDigestForTest(t, f.path)
	return f
}
func sourceDigestForTest(t *testing.T, path string) string {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:])
}

func TestImportCompleteAccountDB(t *testing.T) {
	pool := accountImportPool(t)
	f := newImportFixture(t, pool)
	var generation string
	calls := 0
	transition := func(ctx context.Context, tx pgx.Tx, userID int) (string, error) {
		calls++
		if userID != f.identity.AccountID {
			return "", errors.New("wrong account")
		}
		var err error
		generation, err = progresssync.RotateGeneration(ctx, tx, userID)
		return generation, err
	}
	result, err := ImportAccount(t.Context(), pool, f.path, f.identity, transition)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != accountImported || result.ProviderSwitch != providerSwitchBlocked || len(result.Tables) != 29 || result.ProgressGeneration != generation || calls != 1 {
		t.Fatalf("incomplete result: %+v calls=%d", result, calls)
	}
	for _, mapping := range Manifest() {
		if mapping.Target != "" && result.Tables[mapping.Source].Rows < 1 {
			t.Fatalf("mapped table not exercised: %s", mapping.Source)
		}
	}
	assertImportedStoreReads(t, pool, f)
	if got := sourceDigestForTest(t, f.path); got != f.identity.SourceSHA256 {
		t.Fatal("source changed")
	}
	if _, err := pool.Exec(t.Context(), "UPDATE user_settings SET value='later target edit' WHERE user_id=$1 AND key='opaque'", f.identity.AccountID); err != nil {
		t.Fatal(err)
	}
	replay, err := ImportAccount(t.Context(), pool, f.path, f.identity, transition)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || calls != 1 || replay.ProgressGeneration != generation || !replay.ImportedAt.Equal(result.ImportedAt) {
		t.Fatal("receipt replay repeated transition")
	}
	var durableGeneration string
	if err := pool.QueryRow(t.Context(), "SELECT generation::text FROM user_progress_sync_state WHERE user_id=$1", f.identity.AccountID).Scan(&durableGeneration); err != nil || durableGeneration != generation {
		t.Fatal("receipt generation differs from durable generation")
	}
	var value string
	if err := pool.QueryRow(t.Context(), "SELECT value FROM user_settings WHERE user_id=$1 AND key='opaque'", f.identity.AccountID).Scan(&value); err != nil || value != "later target edit" {
		t.Fatal("receipt replay overwrote target")
	}
}

func TestImportRollbackAndCollisionDB(t *testing.T) {
	pool := accountImportPool(t)
	for _, mode := range []string{"transition failure", "cancellation", "target collision", "source orphan", "source changed", "JSON duplicate", "timestamp precision", "catalog missing", "section collision", "foreign installation", "boolean fractional true", "boolean fractional false", "boolean text"} {
		t.Run(mode, func(t *testing.T) {
			f := newImportFixture(t, pool)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			transition := progresssync.RotateGeneration
			switch mode {
			case "transition failure":
				transition = func(ctx context.Context, tx pgx.Tx, userID int) (string, error) {
					if _, err := progresssync.RotateGeneration(ctx, tx, userID); err != nil {
						return "", err
					}
					return "", errors.New("synthetic failure")
				}
			case "cancellation":
				transition = func(ctx context.Context, tx pgx.Tx, userID int) (string, error) {
					if _, err := progresssync.RotateGeneration(ctx, tx, userID); err != nil {
						return "", err
					}
					cancel()
					return "", ctx.Err()
				}
			case "target collision":
				if _, err := pool.Exec(ctx, "INSERT INTO user_settings VALUES($1,'collision','original')", f.identity.AccountID); err != nil {
					t.Fatal(err)
				}
			case "foreign installation":
				f.identity.InstallationID = uuid.NewString()
			case "source orphan", "source changed", "JSON duplicate", "timestamp precision", "catalog missing", "section collision", "boolean fractional true", "boolean fractional false", "boolean text":
				source, err := sql.Open("sqlite3", f.path)
				if err != nil {
					t.Fatal(err)
				}
				statement := "UPDATE favorites SET profile_id='missing'"
				switch mode {
				case "boolean fractional true":
					statement = "UPDATE watch_progress SET completed=1.5"
				case "boolean fractional false":
					statement = "UPDATE watch_progress SET completed=0.5"
				case "boolean text":
					statement = "UPDATE watch_progress SET completed='invalid'"
				case "JSON duplicate":
					statement = `UPDATE user_setting_values SET value='{"private":1,"private":2}'`
				case "timestamp precision":
					statement = "UPDATE watch_progress SET updated_at='2026-01-01T00:00:00.000000001Z'"
				case "catalog missing":
					statement = "UPDATE favorites SET media_item_id='missing-item'"
				case "section collision":
					statement = "INSERT INTO user_settings VALUES('section_overrides:home:','conflicting private value')"
				}
				if _, err := source.Exec(statement); err != nil {
					t.Fatal(err)
				}
				_ = source.Close()
				if mode != "source changed" {
					f.identity.SourceSHA256 = sourceDigestForTest(t, f.path)
				}
			}
			if _, err := ImportAccount(ctx, pool, f.path, f.identity, transition); err == nil {
				t.Fatal("invalid import succeeded")
			}
			for _, table := range []string{"user_profiles", "user_watch_progress", "userdb_import_receipts", "user_progress_sync_state"} {
				var count int
				if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE user_id=$1", f.identity.AccountID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial %s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestImportTwoAccountsAndConcurrentReplayDB(t *testing.T) {
	pool := accountImportPool(t)
	first := newImportFixture(t, pool)
	second := newImportFixture(t, pool)
	var calls atomic.Int32
	transition := func(ctx context.Context, tx pgx.Tx, userID int) (string, error) {
		calls.Add(1)
		return progresssync.RotateGeneration(ctx, tx, userID)
	}
	errorsFound := make(chan error, 2)
	var work sync.WaitGroup
	for range 2 {
		work.Go(func() {
			_, err := ImportAccount(t.Context(), pool, first.path, first.identity, transition)
			errorsFound <- err
		})
	}
	work.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent replay applied transition twice")
	}
	if _, err := ImportAccount(t.Context(), pool, second.path, second.identity, transition); err != nil {
		t.Fatal(err)
	}
	assertImportedStoreReads(t, pool, first)
	assertImportedStoreReads(t, pool, second)
	for _, f := range []importFixture{first, second} {
		var item string
		if err := pool.QueryRow(t.Context(), "SELECT media_item_id FROM user_watch_history WHERE user_id=$1 AND id='history'", f.identity.AccountID).Scan(&item); err != nil || item != f.item {
			t.Fatal("account identity collision")
		}
	}
	source, err := sql.Open("sqlite3", first.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec("UPDATE user_settings SET value='changed backup'"); err != nil {
		t.Fatal(err)
	}
	_ = source.Close()
	first.identity.SourceSHA256 = sourceDigestForTest(t, first.path)
	if _, err := ImportAccount(t.Context(), pool, first.path, first.identity, transition); err == nil {
		t.Fatal("changed backup overwrote completed receipt")
	}
	if calls.Load() != 2 {
		t.Fatal("changed-source replay rotated generation")
	}
}
