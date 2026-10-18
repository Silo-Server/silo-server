package database

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
	"github.com/jackc/pgx/v5/stdlib"
)

// The rebuild runs while other replicas keep serving the tables. With a reader
// holding a lock on the table, it must wait without blocking anyone else, then
// rebuild the invalid index and build missing ones, leaving valid ones alone.
func TestInvalidIndexRebuildKeepsTablesAvailablePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	connect := func() *pgx.Conn {
		t.Helper()
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	schema := fmt.Sprintf("invalid_index_rebuild_%d", time.Now().UnixNano())
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	exec("CREATE TABLE " + schema + ".media_files (media_folder_id integer, file_path text)")
	exec("CREATE TABLE " + schema + ".media_items (content_id text, type text)")
	exec("CREATE TABLE " + schema + ".user_watch_progress (user_id integer, profile_id text, updated_at timestamptz, media_item_id text, completed boolean)")
	exec("CREATE TABLE " + schema + ".abs_playback_sessions (user_id integer, profile_id text, started_at timestamptz)")
	exec("CREATE TABLE " + schema + ".manga_enrichment_state (next_attempt_at timestamptz)")
	exec("INSERT INTO " + schema + ".media_files VALUES (1, '/a.mkv'), (1, '/b.mkv')")

	exec("CREATE INDEX idx_media_items_content_type ON " + schema + ".media_items (content_id, type)")
	indexOID := func(name string) uint32 {
		t.Helper()
		var oid uint32
		if err := db.QueryRowContext(ctx, "SELECT coalesce(to_regclass($1)::oid, 0)", schema+"."+name).Scan(&oid); err != nil {
			t.Fatal(err)
		}
		return oid
	}
	validOID := indexOID("idx_media_items_content_type")
	// A failed concurrent build leaves an invalid index under the name a later
	// IF NOT EXISTS retry skips.
	_, err = db.ExecContext(ctx, "CREATE UNIQUE INDEX CONCURRENTLY idx_media_files_folder_file_path_pattern ON "+schema+".media_files (media_folder_id)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected failed concurrent build, got %v", err)
	}

	reader := connect()
	readerTx, err := reader.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readerTx.Exec(ctx, "SELECT count(*) FROM "+schema+".media_files"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- rebuildInvalidIndexes(ctx, db, schema) }()

	// Wait until the rebuild is queued behind the reader.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid() AND strpos(query, $1) > 0)`, schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("rebuild finished while a reader held the table: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild never waited for the reader")
		}
		time.Sleep(20 * time.Millisecond)
	}

	writer := connect()
	if _, err := writer.Exec(ctx, "SET lock_timeout = '2s'"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, "INSERT INTO "+schema+".media_files VALUES (2, '/c.mkv')"); err != nil {
		t.Fatalf("write blocked while the rebuild waited: %v", err)
	}
	if _, err := writer.Exec(ctx, "SELECT count(*) FROM "+schema+".media_files"); err != nil {
		t.Fatalf("read blocked while the rebuild waited: %v", err)
	}

	if err := readerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("rebuild did not finish after the reader committed")
	}

	// The invalid index is rebuilt, and the missing ones (as if an earlier
	// attempt stopped right after dropping them) are built.
	for _, index := range rebuiltConcurrentIndexes {
		var valid, unique bool
		var def string
		if err := db.QueryRowContext(ctx, `SELECT indisvalid, indisunique, pg_get_indexdef(indexrelid) FROM pg_index WHERE indexrelid = to_regclass($1)`,
			schema+"."+index.name).Scan(&valid, &unique, &def); err != nil {
			t.Fatalf("index %s: %v", index.name, err)
		}
		if !valid || unique {
			t.Errorf("index %s valid=%t unique=%t: %s", index.name, valid, unique, def)
		}
	}
	var def string
	if err := db.QueryRowContext(ctx, `SELECT pg_get_indexdef($1::regclass)`, schema+".idx_media_files_folder_file_path_pattern").Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "media_folder_id, file_path text_pattern_ops") {
		t.Errorf("rebuilt index definition: %s", def)
	}
	if got := indexOID("idx_media_items_content_type"); got != validOID {
		t.Error("valid index was rebuilt")
	}
}
