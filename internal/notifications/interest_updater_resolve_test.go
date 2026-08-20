package notifications

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestResolveSeriesIDsBatchesLookups pins the batched item→series resolution
// used by the interest flush loop. Marking or unmarking a whole series queues
// one mutation per episode, and those all collapse to a single series; the
// previous per-item lookup issued one query each, which is what made a
// large-series unmark spend minutes in `episodes` lookups.
//
// Uses temp tables shadowing episodes/seasons/media_items, with the pool
// pinned to one connection so every query sees them.
func TestResolveSeriesIDsBatchesLookups(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run the DB-backed series resolution test")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse db config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, ddl := range []string{
		`CREATE TEMP TABLE episodes (content_id text PRIMARY KEY, series_id text NOT NULL)`,
		`CREATE TEMP TABLE seasons (content_id text PRIMARY KEY, series_id text NOT NULL)`,
		`CREATE TEMP TABLE media_items (content_id text PRIMARY KEY, type text NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("create temp table: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id) VALUES ('ep-1','series-1'),('ep-2','series-1'),('ep-3','series-2');
		INSERT INTO seasons (content_id, series_id) VALUES ('season-1','series-1');
		INSERT INTO media_items (content_id, type) VALUES ('series-1','series'),('movie-1','movie');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	updater := &InterestUpdater{pool: pool}

	got, err := updater.resolveSeriesIDs(ctx, []string{
		"ep-1", "ep-2", "ep-3", "season-1", "series-1", "movie-1", "missing", "", "ep-1",
	})
	if err != nil {
		t.Fatalf("resolveSeriesIDs: %v", err)
	}

	for itemID, wantSeries := range map[string]string{
		"ep-1":     "series-1",
		"ep-2":     "series-1",
		"ep-3":     "series-2",
		"season-1": "series-1",
		"series-1": "series-1",
	} {
		if got[itemID] != wantSeries {
			t.Errorf("resolveSeriesIDs[%s] = %q, want %q", itemID, got[itemID], wantSeries)
		}
	}

	// Movies, unknown IDs, and blanks must be absent rather than mapped: the
	// flush loop reads "missing" as "no series interest" and skips them.
	for _, itemID := range []string{"movie-1", "missing", ""} {
		if seriesID, ok := got[itemID]; ok {
			t.Errorf("resolveSeriesIDs[%q] = %q, want absent", itemID, seriesID)
		}
	}

	if empty, err := updater.resolveSeriesIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("resolveSeriesIDs(nil) = %v (%v), want empty", empty, err)
	}
}
