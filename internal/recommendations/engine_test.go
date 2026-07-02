package recommendations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Compile-time assertions that *Engine satisfies both catalog provider
// interfaces it will be injected as. CatalogSearchQueryVectorizer is the
// existing query-embedding hook; CatalogSemanticModelProvider is added in this
// task and wired in Task 4.
var (
	_ catalog.CatalogSearchQueryVectorizer = (*Engine)(nil)
	_ catalog.CatalogSemanticModelProvider = (*Engine)(nil)
)

// newEngineTestPool mirrors internal/catalog/display_query_filter_test.go: it
// skips when SILO_TEST_DATABASE_URL is unset and verifies the base schema is
// present before returning a usable pool.
func newEngineTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.server_settings')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check server_settings table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

// snapshotEmbeddingLock captures the current embedding lock row (if any) and
// registers cleanup that restores the original state, so these tests do not
// leave behind or clobber a real lock.
func snapshotEmbeddingLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var original *string
	err := pool.QueryRow(ctx,
		`SELECT value FROM server_settings WHERE key = $1`,
		embeddingLockSettingKey,
	).Scan(&original)
	if err != nil {
		original = nil
	}
	t.Cleanup(func() {
		cctx := context.Background()
		if original == nil {
			_, _ = pool.Exec(cctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey)
			return
		}
		_, _ = pool.Exec(cctx, `
			INSERT INTO server_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
		`, embeddingLockSettingKey, *original)
	})
}

func TestEngineActiveEmbeddingModelNoLock(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		t.Fatalf("clear embedding lock: %v", err)
	}

	e := &Engine{repo: NewRepo(pool)}
	model, err := e.ActiveEmbeddingModel(ctx)
	if err != nil {
		t.Fatalf("ActiveEmbeddingModel returned error: %v", err)
	}
	if model != "" {
		t.Fatalf("ActiveEmbeddingModel = %q, want empty string when no lock", model)
	}
}

func TestEngineActiveEmbeddingModelWithLock(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := context.Background()

	repo := NewRepo(pool)
	if err := repo.SetEmbeddingLock(ctx, EmbeddingLock{
		BaseURL:          "http://x",
		Model:            "test-model-x",
		SourceDimensions: CanonicalEmbeddingDimensions,
	}); err != nil {
		t.Fatalf("SetEmbeddingLock: %v", err)
	}

	e := &Engine{repo: repo}
	model, err := e.ActiveEmbeddingModel(ctx)
	if err != nil {
		t.Fatalf("ActiveEmbeddingModel returned error: %v", err)
	}
	if model != "test-model-x" {
		t.Fatalf("ActiveEmbeddingModel = %q, want %q", model, "test-model-x")
	}
}
