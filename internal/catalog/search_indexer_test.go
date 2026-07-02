package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttachDocumentVectorsSkipsWhenSemanticDisabled(t *testing.T) {
	docs := []catalogSearchDocument{{ContentID: "movie-1", Title: "Movie"}}
	indexer := &CatalogSearchIndexer{pool: new(pgxpool.Pool)}

	if err := indexer.attachDocumentVectors(context.Background(), docs, "invalid.embedder", false); err != nil {
		t.Fatalf("attachDocumentVectors returned error with semantic disabled: %v", err)
	}
	if docs[0].Vectors != nil {
		t.Fatalf("semantic-disabled document should not carry vectors: %#v", docs[0].Vectors)
	}
}

// TestCountCatalogSearchVectorDocumentsModelCoverage verifies case (a): an
// embedding stored under a different model is excluded when a model is
// requested, but counted when the model filter is empty.
func TestCountCatalogSearchVectorDocumentsModelCoverage(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-model-%d", time.Now().UnixNano())
	current := prefix + "-current"
	other := prefix + "-other"
	cleanupSemanticCoverageItems(t, pool, prefix)

	seedSemanticCoverageMediaItem(t, pool, current, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, other, "movie", "matched")
	seedSemanticCoverageEmbedding(t, pool, current, "text-embedding-3-large")
	seedSemanticCoverageEmbedding(t, pool, other, "stale-model")

	types := []string{"movie"}

	allModels, err := countCatalogSearchVectorDocuments(ctx, pool, types, "")
	if err != nil {
		t.Fatalf("count all models: %v", err)
	}
	if allModels < 2 {
		t.Fatalf("count with empty model = %d, want >= 2 (both rows)", allModels)
	}

	currentModel, err := countCatalogSearchVectorDocuments(ctx, pool, types, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("count current model: %v", err)
	}
	if got := allModels - currentModel; got < 1 {
		t.Fatalf("model filter did not exclude the stale-model row (all=%d current=%d)", allModels, currentModel)
	}

	// The nil-itemTypes branch omits the "AND mi.type = ANY($1)" clause yet still
	// passes a NULL first arg; exercise it through pgx to prove the unreferenced
	// placeholder does not error.
	if _, err := countCatalogSearchVectorDocuments(ctx, pool, nil, "text-embedding-3-large"); err != nil {
		t.Fatalf("count with nil item types: %v", err)
	}
}

// TestCatalogSemanticCoverageByTypeC1Guard verifies case (b) (the C1 regression
// guard) and case (c): an unmatched, non-book item inflates neither numerator
// nor denominator, while a matched item with no embedding row counts toward the
// denominator only.
func TestCatalogSemanticCoverageByTypeC1Guard(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-c1-%d", time.Now().UnixNano())
	matchedVectorized := prefix + "-matched-vec"
	matchedNoVector := prefix + "-matched-novec"
	unmatchedNoVector := prefix + "-unmatched-novec"
	cleanupSemanticCoverageItems(t, pool, prefix)

	seedSemanticCoverageMediaItem(t, pool, matchedVectorized, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, matchedNoVector, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, unmatchedNoVector, "movie", "unmatched")
	seedSemanticCoverageEmbedding(t, pool, matchedVectorized, "text-embedding-3-large")

	rows, err := catalogSemanticCoverageByType(ctx, pool, []string{"movie"}, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("catalogSemanticCoverageByType: %v", err)
	}
	cov := findCoverageRow(t, rows, "movie")

	// Denominator: matchedVectorized + matchedNoVector are eligible; the
	// unmatched movie must NOT be counted.
	if cov.Eligible < 2 {
		t.Fatalf("eligible = %d, want >= 2 (two matched movies)", cov.Eligible)
	}
	// Numerator: only matchedVectorized has a current-model embedding.
	if cov.Vectorized < 1 {
		t.Fatalf("vectorized = %d, want >= 1", cov.Vectorized)
	}
	// The C1 guard: the unmatched movie inflated neither side, so vectorized
	// must trail eligible by at least the matched-no-vector item, and never
	// exceed eligible.
	if cov.Vectorized > cov.Eligible {
		t.Fatalf("numerator %d exceeds denominator %d", cov.Vectorized, cov.Eligible)
	}
	if cov.Eligible-cov.Vectorized < 1 {
		t.Fatalf("matched-no-embedding item not reflected in denominator (eligible=%d vectorized=%d)", cov.Eligible, cov.Vectorized)
	}
}

// TestCatalogSemanticCoverageByTypeStaleEmbeddingRatio verifies case (d): a
// stale embedding row left on a now-unmatched item must not let the numerator
// exceed the denominator, keeping the per-type ratio within [0,1].
func TestCatalogSemanticCoverageByTypeStaleEmbeddingRatio(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-stale-%d", time.Now().UnixNano())
	staleUnmatched := prefix + "-stale-unmatched"
	cleanupSemanticCoverageItems(t, pool, prefix)

	// An item that lost its match but still carries an embedding row.
	seedSemanticCoverageMediaItem(t, pool, staleUnmatched, "movie", "unmatched")
	seedSemanticCoverageEmbedding(t, pool, staleUnmatched, "text-embedding-3-large")

	rows, err := catalogSemanticCoverageByType(ctx, pool, []string{"movie"}, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("catalogSemanticCoverageByType: %v", err)
	}
	for _, cov := range rows {
		if cov.Eligible < 0 || cov.Vectorized < 0 {
			t.Fatalf("negative coverage counts: %+v", cov)
		}
		if cov.Vectorized > cov.Eligible {
			t.Fatalf("type %s ratio out of range: vectorized=%d eligible=%d", cov.Type, cov.Vectorized, cov.Eligible)
		}
	}
}

func newSemanticCoverageTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_embeddings')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_item_embeddings table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

func seedSemanticCoverageMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, $2, $3, $4, '{}'::text[])
	`, contentID, mediaType, contentID, status); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
}

func seedSemanticCoverageEmbedding(t *testing.T, pool *pgxpool.Pool, contentID, model string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES (
			$1,
			(SELECT array_agg(0.0::real) FROM generate_series(1, 3072))::vector,
			$2,
			$1
		)
	`, contentID, model); err != nil {
		t.Fatalf("seed embedding %s: %v", contentID, err)
	}
}

func cleanupSemanticCoverageItems(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// media_item_embeddings rows cascade on the media_items delete.
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
}

func findCoverageRow(t *testing.T, rows []catalogTypeCoverage, mediaType string) catalogTypeCoverage {
	t.Helper()
	for _, row := range rows {
		if row.Type == mediaType {
			return row
		}
	}
	t.Fatalf("no coverage row for type %q in %+v", mediaType, rows)
	return catalogTypeCoverage{}
}
