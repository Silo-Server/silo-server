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
)

// A row scoped to ebooks or manga shows only that type, as its See All page
// does, even when its library also holds titles of another type.
func TestBookMediaScopeRowsKeepTheirTypePostgres(t *testing.T) {
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

	prefix := "book-scope-" + uuid.NewString()[:8] + "-"
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('ebooks', $1, true) RETURNING id`, prefix+"lib").Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})

	types := []string{"ebook", "manga", "audiobook"}
	for _, itemType := range types {
		id := prefix + itemType
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, year)
			VALUES ($1, $2, $1, 'matched', '{}'::text[], 9999)`, id, itemType); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, library, time.Now()); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}

	fetcher := NewFetcher(pool)
	for _, scope := range []string{"ebook", "manga"} {
		for _, sectionType := range []SectionType{SectionRecentlyAdded, SectionRecentlyReleased, SectionRandom} {
			section := ResolvedSection{ID: "scoped", SectionType: sectionType, Title: "Row", ItemLimit: 100, Config: defaultMediaScopeConfig(scope)}
			got, err := fetcher.FetchOne(ctx, section, &library, nil, 7, "p1", catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("FetchOne(%s, %s): %v", sectionType, scope, err)
			}
			ids := contentIDs(got.Items)
			if !slices.Equal(ids, []string{prefix + scope}) {
				t.Errorf("%s scoped to %s = %v, want only %s", sectionType, scope, ids, prefix+scope)
			}
		}
	}
}
