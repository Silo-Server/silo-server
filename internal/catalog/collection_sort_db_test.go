package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// These tests run the default-sort / saved-override flow against a real
// Postgres: real SQL ordering through the query executor, and a real
// PostgresUserStore writing the override row. They cover the reported
// behavior end to end — a collection's creator picks a default order, a viewer
// re-sorts, and the viewer's choice survives a fresh read.
//
// Set SILO_TEST_DATABASE_URL to a migrated database to run them.

func collectionSortTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.user_collection_sort_preferences')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check user_collection_sort_preferences table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the collection sort preference migration")
	}
	return pool
}

// seedSortableItem inserts one sortable item. Callers choose title/year pairs
// that make a title sort and a year sort disagree, so an order assertion cannot
// pass by coincidence.
func seedSortableItem(t *testing.T, pool *pgxpool.Pool, contentID, title string, year int) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, sort_title, year, genres)
		VALUES ($1, 'movie', $2, $2, $3, '{}'::text[])
	`, contentID, title, year); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
}

func orderedIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}
