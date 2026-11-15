package audiobooks

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The matcher checks a candidate against the item's tagged narrators and
// series position, so the claim has to load them. A book whose match an admin
// ignored is never claimed, even while it is due.
func TestClaimBatchLoadsNarratorsAndSeriesPositionDB(t *testing.T) {
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

	suffix := uuid.NewString()
	whole, fractional, ignored := "ab-claim-whole-"+suffix, "ab-claim-frac-"+suffix, "ab-claim-ignored-"+suffix
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, "ab-claim-"+suffix,
	).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	// people and item_people ids have no default; the scanner assigns them.
	narrator := time.Now().UnixNano() % 1_000_000_000_000
	second := narrator + 1
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{whole, fractional, ignored})
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = ANY($1)`, []int64{narrator, second})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO people (id, name) VALUES ($1, $2), ($3, $4)`, []any{narrator, "Kate Reading " + suffix, second, "Michael Kramer " + suffix}},
		{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', 'Whole', 'local', '{}'), ($2, 'audiobook', 'Fractional', 'local', '{}')`, []any{whole, fractional}},
		{`INSERT INTO media_items (content_id, type, title, status, genres, match_ignored_at) VALUES ($1, 'audiobook', 'Ignored', 'local', '{}', now())`, []any{ignored}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $3), ($2, $3), ($4, $3)`, []any{whole, fractional, folderID, ignored}},
		{`INSERT INTO item_people (id, content_id, person_id, kind, sort_order) VALUES ($2, $1, $2, 8, 1), ($3, $1, $3, 8, 0)`, []any{whole, narrator, second}},
		{`INSERT INTO audiobook_series (content_id, series_name, series_index) VALUES ($1, 'S', 2), ($2, 'S', 1.5)`, []any{whole, fractional}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	e := &Enricher{pool: pool, state: newEnrichmentStateStore(pool), batchSize: 10_000}
	items, err := e.claimBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]enrichmentItemRow{}
	for _, it := range items {
		got[it.ContentID] = it
	}
	if w := got[whole]; w.SeriesPosition != 2 || !slices.Equal(w.Narrators, []string{"Michael Kramer " + suffix, "Kate Reading " + suffix}) {
		t.Errorf("whole: position %d, narrators %v; want 2 and both narrators in credit order", w.SeriesPosition, w.Narrators)
	}
	if _, ok := got[ignored]; ok {
		t.Error("claimed a book whose match an admin ignored")
	}
	if f, ok := got[fractional]; !ok || f.SeriesPosition != 0 || len(f.Narrators) != 0 {
		t.Errorf("fractional: claimed %v, position %d, narrators %v; want position 0 and no narrators", ok, f.SeriesPosition, f.Narrators)
	}
}
