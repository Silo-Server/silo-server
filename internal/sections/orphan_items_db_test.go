package sections

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// A media_items row left without any library membership, as happens when its
// last file is removed, appears in no section: on Home, where no library
// narrows the scope, exactly as in a library's own sections.
func TestSectionsSkipItemsInNoLibraryPostgres(t *testing.T) {
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

	prefix := "orphan-scope-" + uuid.NewString()[:8] + "-"
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix+"lib").Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})

	present, orphan := prefix+"present", prefix+"orphan"
	for _, id := range []string{present, orphan} {
		// Far-future year and a high rating put both titles at the top of
		// Recently Released and inside Critically Acclaimed.
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, year, tmdb_vote_average, tmdb_vote_count)
			VALUES ($1, 'movie', $1, 'matched', '{}'::text[], 9999, 9.5, 100000)`, id); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, present, library, time.Now()); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	fetcher := NewFetcher(pool)
	fetcher.RecommendationReader = &poolReader{pool: []recommendations.ScoredItem{
		{MediaItemID: orphan, Score: 2},
		{MediaItemID: present, Score: 1},
	}}

	scopes := []struct {
		name      string
		libraryID *int
		filter    catalog.AccessFilter
	}{
		{name: "home", libraryID: nil},
		{name: "home with a disabled library", libraryID: nil, filter: catalog.AccessFilter{DisabledLibraryIDs: []int{-1}}},
		{name: "library", libraryID: &library},
	}
	for _, sectionType := range []SectionType{SectionRecommendedForYou, SectionRecentlyReleased, SectionCriticallyAcclaimed} {
		for _, scope := range scopes {
			got, err := fetcher.FetchOne(ctx, ResolvedSection{ID: "orphans", SectionType: sectionType, Title: "Row", ItemLimit: 100}, scope.libraryID, nil, 7, "p1", scope.filter)
			if err != nil {
				t.Fatalf("FetchOne(%s, %s): %v", sectionType, scope.name, err)
			}
			ids := contentIDs(got.Items)
			if slices.Contains(ids, orphan) {
				t.Errorf("%s on %s returned %s, which is in no library", sectionType, scope.name, orphan)
			}
			if !slices.Contains(ids, present) {
				t.Errorf("%s on %s = %v, want it to keep %s", sectionType, scope.name, ids, present)
			}
		}
	}
}
