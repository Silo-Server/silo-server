package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedRefreshPerson inserts a person with an external id and incomplete
// metadata, returning its id. Ids grow with each call, so later seeds are
// "newer".
func seedRefreshPerson(t *testing.T, pool *pgxpool.Pool, label string) int64 {
	t.Helper()
	id := time.Now().UnixNano()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO people (id, name, tmdb_id, updated_at)
		VALUES ($1, $2, $3, NOW())`, id, "Refresh "+label, fmt.Sprintf("refresh-%s-%d", label, id)); err != nil {
		t.Fatalf("seed person %s: %v", label, err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id = $1`, id) })
	return id
}
