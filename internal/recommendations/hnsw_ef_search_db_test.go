package recommendations

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestFindTasteProfileCandidatesWithGenresAboveEfSearchMaximumDB covers the
// Watch Tonight discover path: a genre filter multiplies the candidate pool by
// five, so a pool of 240 asks pgvector for 1200 ANN candidates, above the
// largest hnsw.ef_search it accepts.
func TestFindTasteProfileCandidatesWithGenresAboveEfSearchMaximumDB(t *testing.T) {
	newEngineTestPool(t) // skips without a migrated database
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// pgvector checks hnsw.ef_search once its library is loaded in the backend.
	// A pooled production connection has usually run a vector query already,
	// so load it on every connection rather than relying on a fresh one.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT '[1]'::vector`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const prefix = "test-hnsw-ef-search-"
	cleanupRecoMediaItems(t, pool, prefix)
	var want []string
	for i := range 3 {
		id := fmt.Sprintf("%smovie-%d", prefix, i)
		seedRecoMediaItem(t, pool, id, "movie", "matched")
		if _, err := pool.Exec(ctx, `UPDATE media_items SET genres = '{Drama}' WHERE content_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
			VALUES ($1, (SELECT array_agg((g % ($2 + 2) + 1)::real) FROM generate_series(1, 3072) g)::vector, 'test-model', $1)
		`, id, i); err != nil {
			t.Fatalf("seed embedding %s: %v", id, err)
		}
		want = append(want, id)
	}

	embedding := make([]float32, CanonicalEmbeddingDimensions)
	for i := range embedding {
		embedding[i] = float32(i%2 + 1)
	}
	const watchTonightGenrePool = 240
	items, genres, err := NewRepo(pool).FindTasteProfileCandidates(ctx, embedding, nil, []string{"Drama"}, watchTonightGenrePool, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("FindTasteProfileCandidates with genres and limit %d: %v", watchTonightGenrePool, err)
	}
	var got []string
	for _, item := range items {
		if slices.Contains(want, item.MediaItemID) {
			got = append(got, item.MediaItemID)
			if !slices.Equal(genres[item.MediaItemID], []string{"Drama"}) {
				t.Fatalf("genres for %s = %v, want [Drama]", item.MediaItemID, genres[item.MediaItemID])
			}
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("taste profile candidates = %v, want the seeded Drama movies %v", got, want)
	}
}
