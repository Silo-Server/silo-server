package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// imageDeletePrefixCases covers both branches of imageDeletePrefix and the
// boundaries between them: the "local/" three-segment collapse, a "local/" path
// too short to have three segments, ordinary provider paths, and a path with no
// separator at all.
//
// The LIKE metacharacters matter. The previous correlated-LIKE query treated
// '%' and '_' in a directory name as wildcards, so a directory containing them
// could match paths belonging to a different item. Equality does not, and these
// cases hold that line.
var imageDeletePrefixCases = []string{
	"local/movies/12345/poster/original.webp",
	"local/movies/12345/backdrop/original.webp",
	"local/series/99/poster/original.webp",
	"local/movies/12345/",
	"local/movies",
	"local/",
	"tmdb/movies/550/poster/original.webp",
	"tvdb/series/1234/still/original.webp",
	"tmdb/movies/100%/poster/original.webp",
	"tmdb/movies/a_b/poster/original.webp",
	"nested/a/b/c/d/e/original.webp",
	"tmdb/movies/550/poster/",
	"original.webp",
	"/leading",
	"  tmdb/movies/550/poster/original.webp  ",
	"https://cdn.example/poster.webp",
	"",
}

// TestSQLImageDeletePrefixMatchesGo pins sqlImageDeletePrefix to
// imageDeletePrefix. filterUnreferencedImageDirs compares a SQL-derived
// directory against candidates that Go derived; if the two rules drift, the
// query silently reports referenced directories as unreferenced and the caller
// deletes artwork that is still in use.
func TestSQLImageDeletePrefixMatchesGo(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT t.path, `+sqlImageDeletePrefix+`
		FROM unnest($1::text[]) AS t(path),
		     LATERAL (SELECT btrim(t.path)) AS trimmed(s)
	`, imageDeletePrefixCases)
	if err != nil {
		t.Fatalf("evaluate sqlImageDeletePrefix: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var path, sqlPrefix string
		if err := rows.Scan(&path, &sqlPrefix); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if got := imageDeletePrefix(path); got != sqlPrefix {
			t.Errorf("prefix mismatch for %q: Go %q, SQL %q", path, got, sqlPrefix)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if seen != len(imageDeletePrefixCases) {
		t.Fatalf("expected %d rows, got %d", len(imageDeletePrefixCases), seen)
	}
}
