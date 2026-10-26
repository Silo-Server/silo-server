package metadata

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestArtworkSweepScheduledOriginalsPostgres runs the GC-schedule lookup
// against the migrated schema. It must return candidates the GC has armed and
// leave out parked ones: live artwork is parked, and counting it would let a
// broken reference check pass the anomaly guard.
func TestArtworkSweepScheduledOriginalsPostgres(t *testing.T) {
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

	const armed = "sweeptest-schedule/items/1/poster/original.aaa.webp"
	const parked = "sweeptest-schedule/items/2/poster/original.bbb.webp"
	const untracked = "sweeptest-schedule/items/3/poster/original.ccc.webp"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{armed, parked})
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (original_path, image_type, not_before, next_attempt_at)
		VALUES ($1, 'poster', now(), now()), ($2, 'poster', now(), NULL)`, armed, parked); err != nil {
		t.Fatal(err)
	}

	sweeper := NewArtworkStorageSweeper(pool, &fakeArtworkStorage{})
	got, err := sweeper.scheduled(ctx, []string{armed, parked, untracked})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[armed]; !ok || len(got) != 1 {
		t.Fatalf("scheduled = %v, want only %q", got, armed)
	}
}
