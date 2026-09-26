package historyimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queueTestPool(t *testing.T, seedDuplicates bool) *pgxpool.Pool {
	t.Helper()
	pool := queueTestPoolBeforePersonalMigration(t, seedDuplicates)
	applyPersonalQueueMigration(t, pool)
	return pool
}

func applyPersonalQueueMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	migration, err := os.ReadFile("../../migrations/sql/20260905204703_add_personal_history_import_durability.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
}

func queueTestPoolBeforePersonalMigration(t *testing.T, seedDuplicates bool) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("history_queue_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TABLE history_import_user_mappings(id integer PRIMARY KEY);
 INSERT INTO history_import_user_mappings VALUES(1),(2);
 CREATE TABLE history_import_runs (
 id text PRIMARY KEY,user_id integer NOT NULL DEFAULT 1,profile_id text NOT NULL DEFAULT 'p',
 source_type text NOT NULL DEFAULT 'plex',connection_mode text NOT NULL DEFAULT 'admin_token',
 status text NOT NULL CONSTRAINT history_import_runs_status_check CHECK (status IN ('queued','running','completed','failed','cancelled')),
 mapping_id integer REFERENCES history_import_user_mappings(id) ON DELETE SET NULL,
 fetched integer NOT NULL DEFAULT 0,matched integer NOT NULL DEFAULT 0,unmatched integer NOT NULL DEFAULT 0,
 progress_updated integer NOT NULL DEFAULT 0,history_created integer NOT NULL DEFAULT 0,
 watchlist_added integer NOT NULL DEFAULT 0,favorites_imported integer NOT NULL DEFAULT 0,skipped integer NOT NULL DEFAULT 0,
 warnings jsonb NOT NULL DEFAULT '[]',unmatched_samples jsonb NOT NULL DEFAULT '[]',error_message text,
 created_at timestamptz NOT NULL DEFAULT now(),started_at timestamptz,completed_at timestamptz,last_heartbeat_at timestamptz
 )`)
	if err != nil {
		t.Fatal(err)
	}
	if seedDuplicates {
		if _, err = pool.Exec(t.Context(), `INSERT INTO history_import_runs(id,status,mapping_id) VALUES('legacy-a','queued',1),('legacy-b','running',1)`); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../../migrations/sql/20260905195839_add_history_import_durable_queue.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestQueueMigrationPreservesDuplicatesAndTerminalRows(t *testing.T) {
	pool := queueTestPool(t, true)
	ctx := t.Context()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs WHERE mapping_id=1`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy duplicates count=%d err=%v", count, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO history_import_runs(id,status,mapping_id,dispatch_version) VALUES('new','queued',1,1)`)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("admission=%v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE history_import_runs SET status='failed',error_message='Cannot reconstruct legacy intent',completed_at=now() WHERE id='legacy-a'`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE history_import_runs SET status='running' WHERE id='legacy-a'`,
		`UPDATE history_import_runs SET status='completed',error_message=NULL WHERE id='legacy-a'`,
		`UPDATE history_import_runs SET fetched=100 WHERE id='legacy-a'`,
		`UPDATE history_import_runs SET mapping_id=2 WHERE id='legacy-b'`,
	} {
		if _, err = pool.Exec(ctx, statement); err == nil {
			t.Fatalf("unguarded update accepted: %s", statement)
		}
	}
	// Foreign-key metadata cleanup remains possible without altering the audit.
	if _, err = pool.Exec(ctx, `DELETE FROM history_import_user_mappings WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs WHERE mapping_id IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("detached=%d %v", count, err)
	}
}

func TestQueueAdmissionSeesCommitAfterMappingLockWait(t *testing.T) {
	pool := queueTestPool(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(context.Background()) }()
	second, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Rollback(context.Background()) }()
	if _, err = first.Exec(ctx, `INSERT INTO history_import_runs(id,status,mapping_id) VALUES('first','queued',1)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := second.Exec(ctx, `INSERT INTO history_import_runs(id,status,mapping_id) VALUES('second','queued',1)`)
		result <- err
	}()
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("second admission did not wait: %v", err)
		default:
		}
	}
	if err = first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("post-lock snapshot admitted duplicate: %v", err)
	}
}
