package metadata

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestArtworkSweepDisplacedOriginalsPostgres runs the displaced-revision
// lookup against the migrated schema: it must return exactly the candidate
// paths the artwork revision GC holds a row for.
func TestArtworkSweepDisplacedOriginalsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const displaced = "sweeptest-displaced/items/1/poster/original.aaa.webp"
	const live = "sweeptest-displaced/items/2/poster/original.bbb.webp"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, displaced)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates (original_path, image_type, not_before) VALUES ($1, 'poster', now())`, displaced); err != nil {
		t.Fatal(err)
	}

	sweeper := NewArtworkStorageSweeper(pool, &fakeArtworkStorage{})
	got, err := sweeper.displaced(ctx, []string{displaced, live})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[displaced]; !ok || len(got) != 1 {
		t.Fatalf("displaced = %v, want only %q", got, displaced)
	}
}
