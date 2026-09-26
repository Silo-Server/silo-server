package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionListItemIDsPageDB: the paged membership query answers
// one window of the stored order and probes one row past it for has_more, so
// a page never has to read the whole collection. Set SILO_TEST_DATABASE_URL
// to a migrated database to run it.
func TestLibraryCollectionListItemIDsPageDB(t *testing.T) {
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
	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, TRUE)
		RETURNING id
	`, fmt.Sprintf("page-repo-%d", suffix)).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})

	ids := make([]string, 0, 5)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("page-repo-item-%d-%d", i, suffix)
		seedSortableItem(t, pool, id, id, 2000+i)
		ids = append(ids, id)
	}

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("page-repo-%d", suffix), Title: "Paged", CollectionType: "manual"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })
	// ReplaceItems numbers positions from input order, so feeding the ids in
	// reverse stores them in reverse: a page that came back in id order
	// would be wrong.
	inputs := make([]LibraryCollectionItemInput, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		inputs = append(inputs, LibraryCollectionItemInput{MediaItemID: ids[i]})
	}
	if err := repo.ReplaceItems(ctx, collection.ID, inputs); err != nil {
		t.Fatalf("replace items: %v", err)
	}
	stored := []string{ids[4], ids[3], ids[2], ids[1], ids[0]}

	cases := []struct {
		name          string
		limit, offset int
		want          []string
		hasMore       bool
	}{
		{"first window", 2, 0, stored[0:2], true},
		{"middle window", 2, 2, stored[2:4], true},
		{"last window is short", 2, 4, stored[4:5], false},
		{"window ends exactly at the last row", 5, 0, stored, false},
		{"offset past the end", 2, 9, []string{}, false},
		{"negative offset is the start", 1, -3, stored[0:1], true},
		{"zero limit answers nothing", 0, 0, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, hasMore, err := repo.ListItemIDsPage(ctx, collection.ID, tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("ListItemIDsPage: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) || hasMore != tc.hasMore {
				t.Fatalf("page = %v has_more=%v, want %v has_more=%v", got, hasMore, tc.want, tc.hasMore)
			}
		})
	}

	// The full list and the concatenated pages agree, so the two queries
	// cannot drift in ordering.
	all, err := repo.ListItems(ctx, collection.ID)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	var paged []string
	for offset := 0; ; offset += 2 {
		page, more, err := repo.ListItemIDsPage(ctx, collection.ID, 2, offset)
		if err != nil {
			t.Fatalf("ListItemIDsPage: %v", err)
		}
		paged = append(paged, page...)
		if !more {
			break
		}
	}
	full := make([]string, 0, len(all))
	for _, item := range all {
		full = append(full, item.MediaItemID)
	}
	if fmt.Sprint(paged) != fmt.Sprint(full) {
		t.Fatalf("paged ids = %v, want ListItems order %v", paged, full)
	}
}
