package subtitles

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func subtitleStorageDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL must name a disposable PostgreSQL test database")
	}
	control, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "subtitle_storage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := control.Exec(t.Context(), "CREATE SCHEMA "+ident); err != nil {
		control.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := control.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Error(err)
		}
		control.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `CREATE TABLE downloaded_subtitles (
 id BIGSERIAL PRIMARY KEY,media_file_id BIGINT NOT NULL,provider TEXT NOT NULL,language TEXT NOT NULL,
 format TEXT NOT NULL,release_name TEXT NOT NULL,s3_key TEXT NOT NULL UNIQUE,score DOUBLE PRECISION NOT NULL,
 hearing_impaired BOOLEAN NOT NULL,downloaded_by BIGINT,created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"20260906031359_subtitle_content_identity.sql", "20261001230717_subtitle_sync.sql"} {
		migration, err := os.ReadFile("../../migrations/sql/" + name)
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(migration), "-- +goose Down")
		if !ok {
			t.Fatalf("%s: missing migration boundary", name)
		}
		if _, err := pool.Exec(t.Context(), up); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return pool
}
