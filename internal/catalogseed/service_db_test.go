package catalogseed

import (
	"context"
	"os"
	"testing"
	"time"

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
