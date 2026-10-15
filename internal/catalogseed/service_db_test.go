package catalogseed

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newCatalogSeedTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestBulkUpdatePeopleComparesIDsAsBigint(t *testing.T) {
	ctx := context.Background()
	pool := newCatalogSeedTestPool(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	personID := time.Now().UnixNano()
	if _, err := tx.Exec(ctx, `
		INSERT INTO people (id, name, sort_name, tmdb_id)
		VALUES ($1, 'Import Test Person', 'import test person', '111')
	`, personID); err != nil {
		t.Fatalf("seed person: %v", err)
	}

	people := map[int64]*importPersonState{
		personID: {
			ID:          personID,
			Name:        "Import Test Person",
			TmdbID:      "222",
			NeedsUpdate: true,
		},
	}
	if err := bulkUpdatePeople(ctx, tx, people); err != nil {
		t.Fatalf("bulkUpdatePeople: %v", err)
	}

	var tmdbID string
	if err := tx.QueryRow(ctx, `SELECT tmdb_id FROM people WHERE id = $1`, personID).Scan(&tmdbID); err != nil {
		t.Fatalf("read back person: %v", err)
	}
	if tmdbID != "222" {
		t.Fatalf("tmdb_id = %q, want %q", tmdbID, "222")
	}
}

// A seed bundle can carry vectors from another installation's embedding
// model. Import leaves them out, keeps existing rows, and reports the skip.
// The records' model is one no test ever locks, so the outcome does not depend
// on the shared lock row other packages' tests change concurrently.
func TestImportSkipsEmbeddingsFromAnotherModel(t *testing.T) {
	ctx := context.Background()
	pool := newCatalogSeedTestPool(t)
	prefix := fmt.Sprintf("seed-emb-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	// Pending items are not embedding-eligible, so embedding tests running
	// against the same database leave their vectors alone.
	for _, id := range []string{"existing", "new"} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'movie', $1, 'pending')`, prefix+id); err != nil {
			t.Fatal(err)
		}
	}
	seedTestEmbedding(t, pool, prefix+"existing", "local-model", "existing")

	vector := []float32{0.1, 0.2, 0.3, 0.4}
	var data bytes.Buffer
	writer := gzip.NewWriter(&data)
	if err := json.NewEncoder(writer).Encode(Bundle{Manifest: Manifest{FormatVersion: CurrentBundleVersion}, Embeddings: []EmbeddingRecord{
		{MediaItemID: prefix + "existing", Embedding: vector, Model: "seed-test-foreign-model", CanonicalText: "foreign text"},
		{MediaItemID: prefix + "new", Embedding: vector, Model: "seed-test-foreign-model", CanonicalText: "foreign text"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := NewService(pool, nil, nil).WithEmbeddingModel("seed-test-configured-model").
		Import(ctx, data.Bytes(), ImportOptions{ConflictMode: ConflictModeOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	if result.EmbeddingsImported != 0 || result.EmbeddingsSkipped != 2 {
		t.Fatalf("import result = %+v", result)
	}
	if model, text, _ := testEmbedding(t, pool, prefix+"existing"); model != "local-model" || text != "existing" {
		t.Fatalf("foreign record replaced the local vector: %q/%q", model, text)
	}
	if _, _, ok := testEmbedding(t, pool, prefix+"new"); ok {
		t.Fatal("foreign record was stored")
	}
}

// Import reads the embedding lock inside its transaction and keeps only the
// locked model's records, or the configured model's when nothing is locked,
// and never writes the lock. The test sets the lock in an uncommitted
// transaction, so tests in other packages never see it.
func TestAcceptedEmbeddingsFollowTheLockInTheImportTransaction(t *testing.T) {
	ctx := context.Background()
	pool := newCatalogSeedTestPool(t)
	const lockKey = "recommendations.embedding_lock"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	prefix := fmt.Sprintf("seed-lock-%d-", time.Now().UnixNano())
	for _, id := range []string{"same", "foreign", "configured"} {
		if _, err := tx.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'movie', $1, 'pending')`, prefix+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES ($1, (SELECT array_agg(0.25::real) FROM generate_series(1, 3072))::vector, 'local-model', 'existing')`, prefix+"foreign"); err != nil {
		t.Fatal(err)
	}
	lockValue := func() *string {
		t.Helper()
		var value *string
		if err := tx.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, lockKey).Scan(&value); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return value
	}
	embedding := func(id string) (model, text string) {
		t.Helper()
		err := tx.QueryRow(ctx, `SELECT model, canonical_text FROM media_item_embeddings WHERE media_item_id = $1`, prefix+id).Scan(&model, &text)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return model, text
	}
	service := NewService(pool, nil, nil).WithEmbeddingModel("configured-model")
	importRecords := func(records ...EmbeddingRecord) (imported, skipped int) {
		t.Helper()
		accepted, err := service.acceptedEmbeddings(ctx, tx, records)
		if err != nil {
			t.Fatal(err)
		}
		result := &ImportResult{}
		if err := service.importEmbeddings(ctx, tx, accepted, false, result, nil); err != nil {
			t.Fatal(err)
		}
		return result.EmbeddingsImported, len(records) - len(accepted)
	}
	vector := []float32{0.1, 0.2, 0.3, 0.4}

	// With a lock, its model decides, whatever the configured model says.
	locked := `{"base_url":"http://embed.test","model":"local-model","source_dimensions":4,"storage_dimensions":3072}`
	if _, err := tx.Exec(ctx, `
		INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, lockKey, locked); err != nil {
		t.Fatal(err)
	}
	imported, skipped := importRecords(
		EmbeddingRecord{MediaItemID: prefix + "same", Embedding: vector, Model: "local-model", CanonicalText: "same text"},
		EmbeddingRecord{MediaItemID: prefix + "foreign", Embedding: vector, Model: "gemini-embedding-001", CanonicalText: "foreign text"},
		EmbeddingRecord{MediaItemID: prefix + "configured", Embedding: vector, Model: "configured-model", CanonicalText: "configured text"},
	)
	if imported != 1 || skipped != 2 {
		t.Fatalf("locked import: imported %d, skipped %d", imported, skipped)
	}
	if model, text := embedding("same"); model != "local-model" || text != "same text" {
		t.Fatalf("same-model record stored as %q/%q", model, text)
	}
	if model, text := embedding("foreign"); model != "local-model" || text != "existing" {
		t.Fatalf("foreign record replaced the local vector: %q/%q", model, text)
	}
	if got := lockValue(); got == nil || *got != locked {
		t.Fatalf("lock changed to %v", got)
	}

	// Without a lock, the configured model decides, and the lock stays unset.
	if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, lockKey); err != nil {
		t.Fatal(err)
	}
	imported, skipped = importRecords(
		EmbeddingRecord{MediaItemID: prefix + "configured", Embedding: vector, Model: "configured-model", CanonicalText: "configured text"},
		EmbeddingRecord{MediaItemID: prefix + "foreign", Embedding: vector, Model: "local-model", CanonicalText: "other text"},
	)
	if imported != 1 || skipped != 1 {
		t.Fatalf("unlocked import: imported %d, skipped %d", imported, skipped)
	}
	if model, _ := embedding("configured"); model != "configured-model" {
		t.Fatalf("configured-model record stored as %q", model)
	}
	if got := lockValue(); got != nil {
		t.Fatalf("import wrote the lock %q", *got)
	}
}

func seedTestEmbedding(t *testing.T, pool *pgxpool.Pool, id, model, text string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES ($1, (SELECT array_agg(0.25::real) FROM generate_series(1, 3072))::vector, $2, $3)`, id, model, text); err != nil {
		t.Fatal(err)
	}
}

func testEmbedding(t *testing.T, pool *pgxpool.Pool, id string) (model, text string, ok bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT model, canonical_text FROM media_item_embeddings WHERE media_item_id = $1`, id).Scan(&model, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return model, text, true
}
