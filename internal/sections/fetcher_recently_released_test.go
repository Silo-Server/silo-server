package sections

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Recently Released orders a row the way the library's Release Date sort
// does: by the release date (a series' premiere when it has no release date),
// newest first, whatever order the titles were added in. Titles with no date
// trail the dated ones, newest year first.
func TestRecentlyReleasedOrdersByReleaseDate(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, fmt.Sprintf("recently-released-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	// Seeded newest release first, so the newest-added title is the oldest
	// release.
	seeds := []struct {
		name, kind, releaseDate, firstAirDate string
		year                                  int
	}{
		{name: "sep-30", kind: "movie", releaseDate: "2026-09-30", year: 2026},
		{name: "jun-17", kind: "movie", releaseDate: "2026-06-17", year: 2026},
		{name: "year-only-2026", kind: "movie", year: 2026},
		{name: "series-may-02", kind: "series", firstAirDate: "2026-05-02", year: 2026},
		{name: "dec-2025", kind: "movie", releaseDate: "2025-12-01", year: 2025},
		{name: "year-only-2024", kind: "movie", year: 2024},
		{name: "mar-12", kind: "movie", releaseDate: "2026-03-12", year: 2026},
		{name: "jan-20", kind: "movie", releaseDate: "2026-01-20", year: 2026},
	}
	ids := make(map[string]string, len(seeds))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, seed := range seeds {
		id := fmt.Sprintf("recently-released-%s-%d", seed.name, suffix)
		ids[id] = seed.name
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id) })
		added := base.Add(time.Duration(i) * time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, year, release_date, first_air_date, created_at)
			VALUES ($1, $2, $3, 'matched', '{}'::text[], $4, NULLIF($5, '')::date, NULLIF($6, ''), $7)`,
			id, seed.kind, seed.name, seed.year, seed.releaseDate, seed.firstAirDate, added); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, folderID, added); err != nil {
			t.Fatalf("seed %s membership: %v", seed.name, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })

	items, _, err := NewFetcher(pool).fetchRecentlyReleased(ctx, ResolvedSection{
		SectionType: SectionRecentlyReleased,
		ItemLimit:   20,
	}, &folderID, nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("fetch recently released: %v", err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, ids[item.ContentID])
	}
	want := []string{"sep-30", "jun-17", "series-may-02", "mar-12", "jan-20", "dec-2025", "year-only-2026", "year-only-2024"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
}
