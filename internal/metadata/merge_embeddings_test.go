package metadata

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A merge into a target with no embedding keeps the source's, like the
// provider-ID merge, so the target stays in similarity results until the
// backfill re-embeds it. A target's own embedding wins.
func TestMergeKeepsTheSourceEmbeddingPostgres(t *testing.T) {
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

	prefix := "merge-emb-" + uuid.NewString()[:8] + "-"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	// Pending items are not embedding-eligible, so embedding tests running
	// against the same database leave their vectors alone.
	for _, id := range []string{"source", "bare-target", "other-source", "embedded-target"} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id, type, title, status) VALUES($1, 'movie', $1, 'pending')`, prefix+id); err != nil {
			t.Fatal(err)
		}
	}
	embed := func(id, model, text string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
			VALUES ($1, (SELECT array_agg(0.5::real) FROM generate_series(1, 3072))::vector, $2, $3)`, prefix+id, model, text); err != nil {
			t.Fatal(err)
		}
	}
	embedding := func(id string) (model, text string, ok bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT model, canonical_text FROM media_item_embeddings WHERE media_item_id = $1`, prefix+id).Scan(&model, &text)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false
		}
		if err != nil {
			t.Fatal(err)
		}
		return model, text, true
	}
	embed("source", "model-a", "source text")
	embed("other-source", "model-a", "other source text")
	embed("embedded-target", "model-a", "target text")

	s := &MetadataService{dbPool: pool}
	if err := s.rebindItemToExistingItem(ctx, prefix+"source", prefix+"bare-target", true); err != nil {
		t.Fatal(err)
	}
	if model, text, ok := embedding("bare-target"); !ok || model != "model-a" || text != "source text" {
		t.Fatalf("target embedding after the merge = %q/%q (present %v), want the source's", model, text, ok)
	}
	if _, _, ok := embedding("source"); ok {
		t.Fatal("the merged-away source still has an embedding")
	}

	if err := s.rebindItemToExistingItem(ctx, prefix+"other-source", prefix+"embedded-target", true); err != nil {
		t.Fatal(err)
	}
	if _, text, ok := embedding("embedded-target"); !ok || text != "target text" {
		t.Fatalf("target embedding after the merge = %q (present %v), want the target's own", text, ok)
	}
}
