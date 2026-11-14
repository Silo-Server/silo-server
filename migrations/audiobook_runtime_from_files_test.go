package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const audiobookRuntimeFromFilesMigration = "20261028150609_audiobook_runtime_from_files"

// The backfill gives existing audiobooks the runtime one copy's active files
// add up to, replacing a provider's value for some other edition, and leaves
// everything it has no file duration for alone. Two copies of a recording in
// one library are one book, not a book twice as long.
func TestAudiobookRuntimeFromFilesBackfillPostgres(t *testing.T) {
	db, provider := audiobookRuntimeMigrationFixture(t)
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	seedAudiobookRuntimeMigration(t, db)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var transactions int
	var fileTableLocked bool
	if err := db.QueryRowContext(t.Context(), `SELECT count(DISTINCT transaction_id),coalesce(bool_or(file_table_locked),false) FROM backfill_writes`).Scan(&transactions, &fileTableLocked); err != nil {
		t.Fatal(err)
	}
	if transactions != 3 || fileTableLocked {
		t.Errorf("backfill transactions=%d table lock=%v, want three batches without the installation lock", transactions, fileTableLocked)
	}
	// A retry after partial progress may reinstall the same functions and
	// triggers, but must not rewrite the runtimes already corrected.
	exec("DELETE FROM goose_db_version WHERE version_id>0")
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM backfill_writes").Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 206 {
		t.Errorf("backfill writes=%d, want 206 with no retry rewrites", writes)
	}

	for id, want := range map[string]int{
		"provider":    1,
		"long":        600,
		"two-folders": 25,
		"two-copies":  600,
		"missing":     42,
		"short":       1,
		"unprobed":    42,
		"no-files":    42,
		"movie":       120,
		"locked":      42,
	} {
		var got int
		if err := db.QueryRowContext(t.Context(), "SELECT runtime FROM media_items WHERE content_id = $1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: runtime = %d, want %d", id, got, want)
		}
	}

	// Reassigning a copy must refresh both books, including the book that
	// lost its longest copy. Losing the last playable copy keeps its value.
	exec(`
UPDATE media_files SET duration=18000 WHERE content_id='two-copies' AND canonical_root_path='/a/mp3';
UPDATE media_files SET content_id='provider' WHERE content_id='two-copies' AND canonical_root_path='/a/m4b';`)
	for id, want := range map[string]int{"two-copies": 300, "provider": 600} {
		var got int
		if err := db.QueryRowContext(t.Context(), "SELECT runtime FROM media_items WHERE content_id=$1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("after reassignment %s: runtime = %d, want %d", id, got, want)
		}
	}
	exec(`UPDATE media_files SET missing_since=NOW() WHERE content_id='two-copies'`)
	var retained int
	if err := db.QueryRowContext(t.Context(), "SELECT runtime FROM media_items WHERE content_id='two-copies'").Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 300 {
		t.Errorf("all copies missing: runtime = %d, want 300", retained)
	}
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err := db.QueryRowContext(t.Context(), `SELECT to_regprocedure(current_schema() || '.refresh_audiobook_runtime(text[])') IS NULL`).Scan(&removed); err != nil || !removed {
		t.Fatalf("down removed runtime maintenance=%v: %v", removed, err)
	}
}

func seedAudiobookRuntimeMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`
CREATE TABLE media_items (content_id text PRIMARY KEY, type text, runtime integer, updated_at timestamptz, locked_fields integer[] DEFAULT '{}');
CREATE TABLE media_files (id serial PRIMARY KEY, content_id text, media_folder_id integer, canonical_root_path text, duration integer, missing_since timestamptz);
INSERT INTO media_items (content_id,type,runtime,updated_at) VALUES
	('provider', 'audiobook', 481, NULL),
	('long', 'audiobook', 0, NULL),
	('two-folders', 'audiobook', 0, NULL),
	('two-copies', 'audiobook', 0, NULL),
	('missing', 'audiobook', 42, NULL),
	('short', 'audiobook', 0, NULL),
	('unprobed', 'audiobook', 42, NULL),
	('no-files', 'audiobook', 42, NULL),
	('movie', 'movie', 120, NULL),
	('locked', 'audiobook', 42, NULL);
UPDATE media_items SET locked_fields='{7}' WHERE content_id='locked';
INSERT INTO media_files (content_id,media_folder_id,canonical_root_path,duration,missing_since) VALUES
	('provider', 1, '/a/provider', 60, NULL),
	('long', 1, '/a/long', 36000, NULL),
	('two-folders', 1, '/a/tf', 150, NULL), ('two-folders', 1, '/a/tf', 1350, NULL),
	('two-folders', 2, '/b/tf', 1529, NULL),
	('two-copies', 1, '/a/m4b', 36000, NULL),
	('two-copies', 1, '/a/mp3', 36000, NULL),
	('short', 1, '/a/short', 20, NULL),
	('unprobed', 1, '/a/u', 0, NULL), ('unprobed', 1, '/a/u', 0, NULL),
	('missing', 1, '/a/gone', 900, NOW()),
	('movie', 1, '/a/movie', 60, NULL),
	('locked', 1, '/a/locked', 60, NULL);`)
	// More than two batches, with an observer that records real transactions
	// and any trigger-install table lock retained during the backfill.
	exec(`INSERT INTO media_items (content_id,type,runtime)
 SELECT 'batch-' || lpad(i::text,3,'0'),'audiobook',0 FROM generate_series(1,201) i;
 INSERT INTO media_files (content_id,media_folder_id,canonical_root_path,duration)
 SELECT content_id,1,'/batch/' || content_id,600 FROM media_items WHERE content_id LIKE 'batch-%';
 CREATE TABLE backfill_writes (transaction_id bigint, file_table_locked boolean);
 CREATE FUNCTION record_runtime_backfill() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  INSERT INTO backfill_writes SELECT txid_current(), EXISTS (
   SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation='media_files'::regclass
     AND mode='ShareRowExclusiveLock' AND granted);
  RETURN NEW;
 END; $$;
 CREATE TRIGGER record_runtime_backfill AFTER UPDATE OF runtime ON media_items
 FOR EACH ROW WHEN (OLD.runtime IS DISTINCT FROM NEW.runtime) EXECUTE FUNCTION record_runtime_backfill();`)
}

func audiobookRuntimeMigrationFixture(t *testing.T) (*sql.DB, *goose.Provider) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("audiobook_runtime_migration_%d", time.Now().UnixNano())
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	data, err := FS.ReadFile("sql/" + audiobookRuntimeFromFilesMigration + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL := strings.ReplaceAll(string(data), "public.", schema+".")
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{
		audiobookRuntimeFromFilesMigration + ".sql": &fstest.MapFile{Data: []byte(fixtureSQL)},
	}, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	return db, provider
}
