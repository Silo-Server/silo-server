package historyimport

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type staticWatchlistProvider struct {
	records []Record
}

func (p staticWatchlistProvider) Stream(_ context.Context, _ *StreamPosition, sink RecordSink) error {
	return sink(RecordBatch{Records: p.records})
}

func newPlexWatchlistImportTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := trustLoopback(context.Background())
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, stmt := range []string{
		`CREATE TEMP TABLE media_items (
			content_id text PRIMARY KEY,
			type text NOT NULL,
			title text NOT NULL,
			year integer,
			imdb_id text,
			tmdb_id text,
			tvdb_id text,
			status text NOT NULL DEFAULT 'matched'
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE user_watchlist (
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			media_item_id text NOT NULL,
			added_at timestamptz NOT NULL DEFAULT now(),
			sort_index integer,
			PRIMARY KEY (user_id, profile_id, media_item_id)
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE history_import_runs (
			id text PRIMARY KEY,
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			source_type text NOT NULL,
			connection_mode text NOT NULL,
			status text NOT NULL,
			mapping_id integer,
			fetched integer NOT NULL DEFAULT 0,
			matched integer NOT NULL DEFAULT 0,
			unmatched integer NOT NULL DEFAULT 0,
			progress_updated integer NOT NULL DEFAULT 0,
			history_created integer NOT NULL DEFAULT 0,
			watchlist_added integer NOT NULL DEFAULT 0,
			favorites_imported integer NOT NULL DEFAULT 0,
			skipped integer NOT NULL DEFAULT 0,
			warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
			unmatched_samples jsonb NOT NULL DEFAULT '[]'::jsonb,
			error_message text,
			created_at timestamptz NOT NULL DEFAULT now(),
			started_at timestamptz,
			completed_at timestamptz,
			last_heartbeat_at timestamptz, checkpoint jsonb,
 claim_generation bigint NOT NULL DEFAULT 0, dispatch_version integer, dispatch_kind text NOT NULL DEFAULT 'admin', cancel_requested_at timestamptz,
 dispatch_source_id integer,dispatch_source_revision bigint,dispatch_mapping_id integer,dispatch_mapping_revision bigint,dispatch_external_user_id text
		) ON COMMIT PRESERVE ROWS`,
		`CREATE TEMP TABLE user_favorites (
			user_id integer NOT NULL,
			profile_id text NOT NULL,
			media_item_id text NOT NULL,
			added_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, profile_id, media_item_id)
		) ON COMMIT PRESERVE ROWS`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("create temp test table: %v", err)
		}
	}
	return pool
}
