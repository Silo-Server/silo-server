package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/migrations"
)

// oldSchemaVersion is a release's schema from before plugin_installations
// (031) and every backup table. Every Go migration is newer.
const oldSchemaVersion = 30

// guardCheck runs check when the migrations' lock is released, after goose
// Up returned.
type guardCheck struct {
	database.Guard
	check func()
}

func (g guardCheck) Release(ctx context.Context) {
	g.check()
	g.Guard.Release(ctx)
}

// oldSchemaDatabase is a new database an older release left at
// oldSchemaVersion, with only the artwork folder set. newDatabase makes an
// empty one beside it.
type oldSchemaDatabase struct {
	pool         *pgxpool.Pool
	url, artwork string
	settingsRows int64
	tools        backup.Tools
	newDatabase  func() string
}

func newOldSchemaDatabase(t *testing.T) oldSchemaDatabase {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	var db oldSchemaDatabase
	var err error
	if db.tools, err = backup.FindTools(ctx); err != nil {
		t.Skipf("PostgreSQL client tools: %v", err)
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	db.newDatabase = func() string {
		name := "silo_before_migrate_" + randomSuffix(t)
		if _, err := admin.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`) })
		url, err := backup.WithDatabase(dsn, name)
		if err != nil {
			t.Fatal(err)
		}
		return url
	}
	db.url = db.newDatabase()
	if db.pool, err = pgxpool.New(ctx, db.url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.pool.Close)
	sub, err := fs.Sub(migrations.FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(db.pool), sub, goose.WithTableName("public.goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	if _, err := provider.UpTo(ctx, oldSchemaVersion); err != nil {
		t.Fatal(err)
	}
	db.artwork = t.TempDir()
	if _, err := db.pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ('artwork.local_path', $1)`, db.artwork); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM server_settings`).Scan(&db.settingsRows); err != nil {
		t.Fatal(err)
	}
	return db
}

// beforeUpgradeSource opens the one before-upgrade backup at the default
// destination under the identity the backup minted, and checks its manifest:
// the old schema version, a plain db.dump alone, plugin_installations absent,
// and the minted identity among the settings rows.
func (db oldSchemaDatabase) beforeUpgradeSource(t *testing.T) *backup.Source {
	t.Helper()
	var serverID string
	if err := db.pool.QueryRow(t.Context(), `SELECT value FROM server_settings WHERE key = $1`, serveridentity.Key).Scan(&serverID); err != nil || serverID == "" {
		t.Fatalf("server identity %q, %v; want one minted by the backup", serverID, err)
	}
	folders, err := filepath.Glob(filepath.Join(db.artwork, "backups", serverID, "*-before-upgrade"))
	if err != nil || len(folders) != 1 {
		t.Fatalf("before-upgrade folders %v, %v; want one", folders, err)
	}
	src, err := backup.OpenSource(folders[0])
	if err != nil {
		t.Fatal(err)
	}
	m := src.Manifest
	if m.GooseVersion != oldSchemaVersion || m.RestoreSet != nil || len(m.Objects) != 1 || m.Objects[0].Name != backup.DumpName || m.Encrypted {
		t.Fatalf("manifest: schema %d, restore set %v, objects %+v, encrypted %v; want schema %d and a plain db.dump alone",
			m.GooseVersion, m.RestoreSet, m.Objects, m.Encrypted, oldSchemaVersion)
	}
	inventory := map[string]backup.TableCount{}
	for _, table := range m.Tables {
		inventory[table.Name] = table
	}
	if inventory["plugin_installations"].Present || inventory["server_settings"].Rows != db.settingsRows+1 {
		t.Fatalf("inventory = %+v, want plugin_installations absent and %d server_settings rows", m.Tables, db.settingsRows+1)
	}
	return src
}

// TestBeforeMigrateBackupOnAnOldSchemaDB runs the startup sequence through
// the server's wiring on a database an older release left, with only the
// artwork folder set: settings take their defaults, the server identity is
// minted, and the backup reads only what the old schema has, records absent
// tables, holds the database alone, and restores. The backup lock is held
// through the migrations, so another server's nightly run skips.
func TestBeforeMigrateBackupOnAnOldSchemaDB(t *testing.T) {
	db := newOldSchemaDatabase(t)
	ctx := t.Context()
	bc := &config.BootstrapConfig{DatabaseURL: db.url, SecretKey: []byte("before-migrate-wiring-key-0123456789abcdef")}
	cipher, err := secret.New(bc.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	hook := beforeMigrateBackup(bc, db.pool, cipher, time.Minute)
	otherServer := backup.New(backup.Options{Pool: db.pool, Locker: backup.PostgresLocker{Pool: db.pool}})
	var checked bool
	err = database.MigrateAtStartup(ctx, db.pool, migrations.FS, "sql", database.StartupOptions{
		BeforeMigrate: func(ctx context.Context) (database.Guard, error) {
			guard, err := hook(ctx)
			if err != nil {
				return nil, err
			}
			return guardCheck{Guard: guard, check: func() {
				checked = true
				var migrated bool
				if err := db.pool.QueryRow(ctx, `SELECT to_regclass('public.backup_status') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
					t.Errorf("the lock was released before the migrations finished (%v)", err)
				}
				if result, err := otherServer.RunTask(ctx, nil); err != nil || result.Skipped != backup.SkippedBusy {
					t.Errorf("another server's nightly run during the migrations = %+v, %v; want %q", result, err, backup.SkippedBusy)
				}
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("the migrations did not run under the backup lock")
	}
	src := db.beforeUpgradeSource(t)
	if err := backup.VerifyBackup(ctx, src, backup.PGLoader{Tools: db.tools}, db.newDatabase(), backup.ValidateOptions{}, io.Discard); err != nil {
		t.Fatalf("verify the before-upgrade backup: %v", err)
	}
}
