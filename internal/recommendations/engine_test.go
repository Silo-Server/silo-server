package recommendations

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/dbtest"
	"github.com/Silo-Server/silo-server/migrations"
)

// Compile-time assertions that *Engine satisfies both catalog provider
// interfaces it will be injected as. CatalogSearchQueryVectorizer is the
// existing query-embedding hook; CatalogSemanticModelProvider is added in this
// task and wired in Task 4.
var (
	_ catalog.CatalogSearchQueryVectorizer = (*Engine)(nil)
	_ catalog.CatalogSemanticModelProvider = (*Engine)(nil)
)

// ownedTestConfig points at the database TestMain provisioned for this binary.
// It is nil when SILO_TEST_DATABASE_URL is unset, which makes every DB-backed
// test in the package skip as before.
var ownedTestConfig *pgxpool.Config

// TestMain gives this package its own database.
//
// EmbedAll walks every embed-eligible row in media_items, so its tests can only
// assert "nothing needed embedding" while no other writer exists. Prefix-scoping
// inside the test cannot fix that — the operation under test is global.
func TestMain(m *testing.M) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		os.Exit(m.Run())
	}

	config, drop, err := dbtest.Provision(context.Background(), dsn, "silo_recommendations",
		func(ctx context.Context, pool *pgxpool.Pool) error {
			return database.RunMigrations(ctx, pool, migrations.FS, "sql")
		})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recommendations tests: %v\n", err)
		os.Exit(1)
	}
	ownedTestConfig = config

	code := m.Run()
	if err := drop(); err != nil {
		// Report but do not fail the run: the tests already passed or failed on
		// their own merits, and a leaked database is a cleanup problem, not a
		// verdict on the code under test.
		fmt.Fprintf(os.Stderr, "recommendations tests: %v\n", err)
	}
	os.Exit(code)
}

// newEngineTestPool returns a pool on the database TestMain provisioned for
// this binary, skipping when SILO_TEST_DATABASE_URL is unset.
func newEngineTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if ownedTestConfig == nil {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	// Copy: a *pgxpool.Config may not back more than one pool.
	pool, err := pgxpool.NewWithConfig(ctx, ownedTestConfig.Copy())
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
