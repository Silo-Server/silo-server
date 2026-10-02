package migrations

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestHistoryImportCancellationStatusPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	migrationExec(t, tx, `
CREATE TABLE users (id integer PRIMARY KEY);
CREATE TABLE user_profiles (user_id integer REFERENCES users(id), id text, PRIMARY KEY (user_id, id));
INSERT INTO users VALUES (1);
INSERT INTO user_profiles VALUES (1, 'profile');`)
	// Include the original status constraint and the durable queue guards.
	for _, name := range []string{
		"005_history_import",
		"040_history_import_run_heartbeats",
		"049_admin_history_import",
		"20260702193158_history_import_watchlist_added",
		"20260711114310_history_import_favorites_imported",
		"20260905195839_add_history_import_durable_queue",
		"20260905204703_add_personal_history_import_durability",
	} {
		data, err := FS.ReadFile("sql/" + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(data), "-- +goose Down")
		migrationExec(t, tx, strings.ReplaceAll(up, "public.", schema+"."))
	}
	migrationExec(t, tx, `
SET CONSTRAINTS ALL IMMEDIATE;
INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,fetched)
SELECT status,1,'profile','emby','custom',status,17
FROM unnest(ARRAY['queued','running','completed','failed']) AS status;
UPDATE history_import_runs SET cancel_requested_at=now() WHERE id='running';`)
	snapshot := func() string {
		t.Helper()
		var rows string
		if err := tx.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM history_import_runs r`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := snapshot()
	requireMigrationSQLState(t, tx, `UPDATE history_import_runs SET status='cancelled' WHERE id='running'`, "23514")

	const repair = "20260922124201_allow_cancelled_history_import_runs"
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, false))
	if got := snapshot(); got != before {
		t.Fatalf("migration changed existing runs: before=%s after=%s", before, got)
	}
	migrationExec(t, tx, `UPDATE history_import_runs SET status='cancelled',completed_at=now() WHERE id IN ('queued','running')`)
	requireMigrationSQLState(t, tx, `INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status) VALUES('invalid',1,'profile','emby','custom','invalid')`, "23514")
	requireMigrationSQLState(t, tx, `UPDATE history_import_runs SET status='running' WHERE id='running'`, "23514")

	// Previous application versions also write this status; rollback must retain
	// that status without rewriting the immutable terminal audit rows.
	before = snapshot()
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, true))
	if got := snapshot(); got != before {
		t.Fatalf("rollback changed existing runs: before=%s after=%s", before, got)
	}
	migrationExec(t, tx, `INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status) VALUES('after-rollback',1,'profile','emby','custom','cancelled')`)
	migrationExec(t, tx, adminMigrationSQL(t, repair, schema, false))
}

func TestHistoryImportCancellationRetryPostgres(t *testing.T) {
	tx, schema := adminMigrationFixture(t)
	// An existing invalid row forces validation to fail after replacement.
	migrationExec(t, tx, `CREATE TABLE history_import_runs(status text);
INSERT INTO history_import_runs VALUES('invalid');
ALTER TABLE history_import_runs ADD CONSTRAINT history_import_runs_status_check
CHECK (status IN ('queued','running','completed','failed')) NOT VALID;`)
	config, err := pgx.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	const file = "20260922124201_allow_cancelled_history_import_runs.sql"
	data, err := FS.ReadFile("sql/" + file)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{
		file: &fstest.MapFile{Data: data},
	}, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err == nil {
		t.Fatal("validation accepted an invalid existing status")
	} else if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("expected validation failure, got %v", err)
	}
	// The widened constraint must have committed despite validation failing.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO history_import_runs VALUES('cancelled')`); err != nil {
		t.Fatalf("replacement was rolled back with validation: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO history_import_runs VALUES('invalid')`); err == nil {
		t.Fatal("unvalidated constraint allowed a new invalid status")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE history_import_runs SET status='failed' WHERE status='invalid'`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("retry partially applied migration: %v", err)
	}
	var validated bool
	if err := db.QueryRowContext(t.Context(), `SELECT convalidated FROM pg_constraint
WHERE conrelid='history_import_runs'::regclass AND conname='history_import_runs_status_check'`).Scan(&validated); err != nil || !validated {
		t.Fatalf("constraint validated=%t err=%v", validated, err)
	}
}
