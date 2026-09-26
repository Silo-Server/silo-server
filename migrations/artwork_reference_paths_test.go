package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestArtworkReferenceIndexesRetryPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("artwork_index_test_%d", time.Now().UnixNano())
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	exec("CREATE TABLE " + schema + ".media_items (poster_path text, backdrop_path text, logo_path text)")
	exec("CREATE TABLE " + schema + ".episodes (still_path text)")
	exec("CREATE TABLE " + schema + ".people (photo_path text)")
	exec("CREATE TABLE " + schema + ".seasons (poster_path text)")
	exec("INSERT INTO " + schema + ".media_items VALUES ('poster', 'backdrop', 'logo'), ('poster', 'backdrop', 'logo'), (NULL, NULL, NULL)")

	// A completed first build and a failed second build model a partially
	// applied nontransactional migration. PostgreSQL leaves an invalid index
	// after this unique build fails, just as it can after cancellation.
	exec("CREATE INDEX CONCURRENTLY idx_media_items_poster_path_gc ON " + schema + ".media_items (poster_path) WHERE poster_path IS NOT NULL")
	_, err = db.ExecContext(t.Context(), "CREATE UNIQUE INDEX CONCURRENTLY idx_media_items_backdrop_path_gc ON "+schema+".media_items (backdrop_path) WHERE backdrop_path IS NOT NULL")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected failed concurrent build, got %v", err)
	}
	var valid bool
	if err := db.QueryRowContext(t.Context(), "SELECT indisvalid FROM pg_index WHERE indexrelid = $1::regclass", schema+".idx_media_items_backdrop_path_gc").Scan(&valid); err != nil || valid {
		t.Fatalf("expected invalid index artifact: valid=%t, err=%v", valid, err)
	}

	const file = "20260914145000_index_artwork_reference_paths.sql"
	data, err := FS.ReadFile("sql/" + file)
	if err != nil {
		t.Fatal(err)
	}
	fixture := fstest.MapFS{file: &fstest.MapFile{Data: []byte(strings.ReplaceAll(string(data), "public.", schema+"."))}}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fixture, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	assertIndexes := func(want int) {
		t.Helper()
		var count, usable int
		err := db.QueryRowContext(t.Context(), `
			SELECT count(*), count(*) FILTER (WHERE i.indisvalid AND i.indisready AND NOT i.indisunique)
			FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname LIKE 'idx_%_gc'`, schema).Scan(&count, &usable)
		if err != nil || count != want || usable != want {
			t.Fatalf("indexes=%d usable=%d want=%d: %v", count, usable, want, err)
		}
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("retry partially applied migration: %v", err)
	}
	assertIndexes(6)
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertIndexes(0)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("fresh application: %v", err)
	}
	assertIndexes(6)
	var rows int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+schema+".media_items").Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("catalog rows=%d want=3: %v", rows, err)
	}
}
