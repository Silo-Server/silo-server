package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// startupTestDatabase is an empty database of its own: the bootstrap and the
// wrapper lock are per database.
func startupTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("silo_migrate_startup_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	return pool
}

// legacyInstall makes pool a legacy install that applied versions through
// schema_versions only: their tables exist, goose has never run.
func legacyInstall(t *testing.T, pool *pgxpool.Pool, versions ...int) {
	t.Helper()
	execSQL(t, pool, `CREATE TABLE schema_versions (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`)
	for _, v := range versions {
		execSQL(t, pool, fmt.Sprintf(`INSERT INTO schema_versions (version) VALUES (%d)`, v))
		execSQL(t, pool, fmt.Sprintf(`CREATE TABLE legacy_%d (id integer)`, v))
	}
}

func execSQL(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func tablePresent(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var present bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&present); err != nil {
		t.Fatal(err)
	}
	return present
}

// startupMigrations creates legacy_1 and legacy_2; extra is appended to
// migration 2.
func startupMigrations(pool *pgxpool.Pool, extra string) func() (*migrationProvider, error) {
	files := fstest.MapFS{
		"1_one.sql": {Data: []byte("-- +goose Up\nCREATE TABLE legacy_1 (id integer);\n")},
		"2_two.sql": {Data: []byte("-- +goose Up\nCREATE TABLE legacy_2 (id integer);\n" + extra)},
	}
	return func() (*migrationProvider, error) { return openMigrationProvider(pool, files, ".", nil) }
}

func noBackupExpected(t *testing.T) func(context.Context) (Guard, error) {
	return func(context.Context) (Guard, error) {
		t.Error("the backup before migrations ran with nothing to migrate")
		return nil, nil
	}
}

type testGuard struct {
	lost     chan struct{}
	released atomic.Bool
}

func newTestGuard() *testGuard               { return &testGuard{lost: make(chan struct{})} }
func (g *testGuard) Lost() <-chan struct{}   { return g.lost }
func (g *testGuard) Release(context.Context) { g.released.Store(true) }

// TestStartupMigrationSequenceDB: the legacy bootstrap runs before the
// pending check, so a legacy install is not taken for pending; a new
// database migrates with no backup; pending migrations on an existing schema
// run after the backup, under its guard; and a failing backup stops startup
// with nothing migrated.
func TestStartupMigrationSequenceDB(t *testing.T) {
	backupErr := errors.New("destination unavailable")
	tests := []struct {
		name       string
		legacy     []int
		backup     func(t *testing.T, pool *pgxpool.Pool) (Guard, error)
		wantBackup bool
		wantErr    error
		wantTwo    bool
	}{
		{name: "legacy versions only", legacy: []int{1, 2}, wantTwo: true},
		{name: "new database", wantTwo: true},
		{
			name: "pending on an existing schema", legacy: []int{1}, wantBackup: true, wantTwo: true,
			backup: func(t *testing.T, pool *pgxpool.Pool) (Guard, error) {
				if tablePresent(t, pool, "legacy_2") {
					t.Error("the backup ran after the migration")
				}
				return newTestGuard(), nil
			},
		},
		{
			name: "failing backup", legacy: []int{1}, wantBackup: true, wantErr: backupErr,
			backup: func(*testing.T, *pgxpool.Pool) (Guard, error) { return nil, backupErr },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := startupTestDatabase(t)
			if tc.legacy != nil {
				legacyInstall(t, pool, tc.legacy...)
			}
			opts := StartupOptions{BeforeMigrate: noBackupExpected(t)}
			var ran bool
			var guard Guard
			if tc.backup != nil {
				opts.BeforeMigrate = func(context.Context) (Guard, error) {
					ran = true
					var err error
					guard, err = tc.backup(t, pool)
					return guard, err
				}
			}
			err := migrateAtStartup(t.Context(), pool, opts, startupMigrations(pool, ""))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("migrateAtStartup = %v, want %v", err, tc.wantErr)
			}
			if ran != tc.wantBackup {
				t.Errorf("backup ran = %v, want %v", ran, tc.wantBackup)
			}
			if got := tablePresent(t, pool, "legacy_2"); got != tc.wantTwo {
				t.Errorf("migration 2 applied = %v, want %v", got, tc.wantTwo)
			}
			if g, ok := guard.(*testGuard); ok && !g.released.Load() {
				t.Error("the guard was not released")
			}
		})
	}
}

// waitingForWrapper counts sessions queued on the wrapper lock.
func waitingForWrapper(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND NOT granted AND objsubid = 1
		  AND classid::bigint = $1 AND objid::bigint = $2
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
		int64(uint64(migrationWrapperLockID)>>32), int64(uint64(migrationWrapperLockID)&0xffffffff)).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStartupMigrationsSerializeServersDB: while one server backs up and
// migrates, another blocks on the wrapper and then finds nothing pending;
// one whose wait runs out fails naming SILO_MIGRATION_WAIT.
func TestStartupMigrationsSerializeServersDB(t *testing.T) {
	pool := startupTestDatabase(t)
	legacyInstall(t, pool, 1)
	ctx := t.Context()
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- migrateAtStartup(ctx, pool, StartupOptions{BeforeMigrate: func(context.Context) (Guard, error) {
			close(entered)
			<-release
			return nil, nil
		}}, startupMigrations(pool, ""))
	}()
	<-entered

	err := migrateAtStartup(ctx, pool, StartupOptions{Wait: 200 * time.Millisecond, BeforeMigrate: noBackupExpected(t)}, startupMigrations(pool, ""))
	if err == nil || !strings.Contains(err.Error(), migrationWaitEnv) {
		t.Fatalf("a server whose wait ran out = %v, want an error naming %s", err, migrationWaitEnv)
	}

	second := make(chan error, 1)
	go func() {
		second <- migrateAtStartup(ctx, pool, StartupOptions{Wait: time.Minute, BeforeMigrate: noBackupExpected(t)}, startupMigrations(pool, ""))
	}()
	poll, timeout := time.NewTicker(20*time.Millisecond), time.After(10*time.Second)
	defer poll.Stop()
	for waitingForWrapper(t, pool) == 0 {
		select {
		case <-poll.C:
		case <-timeout:
			t.Fatal("the second server never queued on the wrapper lock")
		}
	}
	select {
	case err := <-second:
		t.Fatalf("the second server finished while the first held the wrapper: %v", err)
	default:
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first server: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second server: %v", err)
	}
	if !tablePresent(t, pool, "legacy_2") {
		t.Error("migration 2 was not applied")
	}
}

// TestStartupMigrationsStopWhenALockSessionEndsDB: when the wrapper's
// session ends during the backup, or the guard's during goose Up, the work
// in flight is canceled, nothing is migrated, and startup fails.
func TestStartupMigrationsStopWhenALockSessionEndsDB(t *testing.T) {
	t.Run("wrapper during the backup", func(t *testing.T) {
		pool := startupTestDatabase(t)
		legacyInstall(t, pool, 1)
		opts := StartupOptions{CheckInterval: 50 * time.Millisecond, BeforeMigrate: func(ctx context.Context) (Guard, error) {
			execSQL(t, pool, fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_locks
				WHERE locktype = 'advisory' AND granted AND objsubid = 1 AND classid::bigint = %d AND objid::bigint = %d
				  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
				int64(uint64(migrationWrapperLockID)>>32), int64(uint64(migrationWrapperLockID)&0xffffffff)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return nil, errors.New("the backup was not canceled")
			}
		}}
		err := migrateAtStartup(t.Context(), pool, opts, startupMigrations(pool, ""))
		if !errors.Is(err, ErrMigrationLockLost) {
			t.Fatalf("migrateAtStartup = %v, want ErrMigrationLockLost", err)
		}
		if tablePresent(t, pool, "legacy_2") {
			t.Error("migration 2 ran after the wrapper was lost")
		}
	})
	t.Run("guard during migrations", func(t *testing.T) {
		pool := startupTestDatabase(t)
		legacyInstall(t, pool, 1)
		guard := newTestGuard()
		opts := StartupOptions{BeforeMigrate: func(context.Context) (Guard, error) {
			time.AfterFunc(300*time.Millisecond, func() { close(guard.lost) })
			return guard, nil
		}}
		started := time.Now()
		err := migrateAtStartup(t.Context(), pool, opts, startupMigrations(pool, "SELECT pg_sleep(30);\n"))
		if !errors.Is(err, ErrMigrationLockLost) {
			t.Fatalf("migrateAtStartup = %v, want ErrMigrationLockLost", err)
		}
		if elapsed := time.Since(started); elapsed > 20*time.Second {
			t.Errorf("the migration ran on for %s after the guard was lost", elapsed)
		}
		if tablePresent(t, pool, "legacy_2") {
			t.Error("migration 2 committed after the guard was lost")
		}
		if !guard.released.Load() {
			t.Error("the guard was not released")
		}
	})
}
