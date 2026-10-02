package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// TestOverlayWinnerIndexesRetryAllowsWritesPostgres exercises an interrupted
// migration while a reader keeps the index cleanup waiting on its transaction.
func TestOverlayWinnerIndexesRetryAllowsWritesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		// The parse error can quote the DSN, credentials included.
		t.Fatal("invalid SILO_TEST_DATABASE_URL configuration")
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("overlay_index_retry_%d", time.Now().UnixNano())
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
	exec("CREATE TABLE " + schema + ".media_files (id bigint PRIMARY KEY, content_id text, episode_id text, resolution text, hdr boolean, video_tracks jsonb, missing_since timestamptz)")
	exec("INSERT INTO " + schema + ".media_files (id, content_id) VALUES (1, 'series'), (2, 'series')")
	const file = "20260930120217_index_home_overlay_winners.sql"
	raw, err := FS.ReadFile("sql/" + file)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(string(raw))
	first := regexp.MustCompile(`(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_overlay_content.*?;`).FindString(source)
	if first == "" {
		t.Fatal("content ranking index definition missing")
	}
	exec(first)
	// A failed unique build leaves the same invalid-index state as an
	// interrupted concurrent build; the first ranking index is already valid.
	_, err = db.ExecContext(t.Context(), "CREATE UNIQUE INDEX CONCURRENTLY idx_media_files_overlay_episode ON "+schema+".media_files (content_id)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected failed concurrent build, got %v", err)
	}
	reader, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	var id int
	if err := reader.QueryRowContext(t.Context(), "SELECT id FROM "+schema+".media_files LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["application_name"] = schema
	migrationDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = migrationDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, migrationDB,
		fstest.MapFS{file: &fstest.MapFile{Data: []byte(source)}},
		goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() { _, err := provider.Up(ctx); done <- err }()
	joined := false
	defer func() {
		cancel()
		_ = reader.Rollback()
		if !joined {
			<-done
		}
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE application_name = $1
			AND wait_event_type = 'Lock' AND query LIKE '%idx_media_files_overlay_%'
		)`, schema).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("migration finished before waiting for the reader: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 2*time.Second)
	_, writeErr := db.ExecContext(writeCtx, "INSERT INTO "+schema+".media_files (id, content_id) VALUES (3, 'another-series')")
	writeCancel()
	_ = reader.Rollback()
	migrationErr := <-done
	joined = true
	if writeErr != nil {
		t.Errorf("migration retry blocked an unrelated write: %v", writeErr)
	}
	if migrationErr != nil {
		t.Fatalf("retry failed: %v", migrationErr)
	}
	var usable int
	err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname IN ('idx_media_files_overlay_content', 'idx_media_files_overlay_episode')
		AND i.indisvalid AND i.indisready AND NOT i.indisunique AND i.indnkeyatts = 5`, schema).Scan(&usable)
	if err != nil || usable != 2 {
		t.Fatalf("usable ranking indexes=%d, want 2: %v", usable, err)
	}
}
