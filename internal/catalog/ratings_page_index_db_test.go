package catalog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Verify the migration can satisfy both initial and continuation ordering
// without a Sort node. Sequential scans are disabled only in this transaction
// because the isolated test table may be too small to favor an index naturally.
func TestRatingsPageIndexDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"", " AND (rated_at, media_item_id) < ('2026-01-02'::timestamptz, 'movie:z')"} {
		var plan []byte
		err := tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) SELECT user_id, profile_id, media_item_id, rating, rated_at FROM user_ratings WHERE user_id = 1 AND profile_id = 'index-test'`+boundary+` ORDER BY rated_at DESC, media_item_id DESC LIMIT 50`).Scan(&plan)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(plan) {
			t.Fatalf("invalid plan: %s", plan)
		}
		if !strings.Contains(string(plan), `"Index Name": "idx_user_ratings_page_order"`) || strings.Contains(string(plan), `"Node Type": "Sort"`) {
			t.Fatalf("page ordering not supplied by index: %s", plan)
		}
	}
}
