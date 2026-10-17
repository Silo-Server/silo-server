package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionReplaceItemsWritesOnlyTheDifferenceDB pins the
// set-based membership replace a library list sync uses: titles that stay
// keep their created_at, moved titles get their new position and source rank,
// and a replace that changes nothing touches neither the collection row nor
// its revision. Set SILO_TEST_DATABASE_URL to a migrated database to run it.
func TestLibraryCollectionReplaceItemsWritesOnlyTheDifferenceDB(t *testing.T) {
	f := newLibraryReplaceItemsFixture(t)
	f.replace(t, "a", "b", "c")
	if _, err := f.pool.Exec(context.Background(), `UPDATE library_collection_items SET created_at = $2 WHERE collection_id = $1`, f.collectionID, f.old); err != nil {
		t.Fatalf("age items: %v", err)
	}

	cases := []struct {
		name         string
		names, kept  []string
		changesState bool
	}{
		{"unchanged", []string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{"reordered", []string{"c", "a", "b"}, []string{"a", "b", "c"}, true},
		{"grown", []string{"c", "d", "a", "b", "e"}, []string{"a", "b", "c"}, true},
		{"shrunk", []string{"a", "e"}, []string{"a"}, true},
		{"emptied", nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.state(t)
			f.replace(t, tc.names...)
			f.expect(t, tc.kept, tc.names...)
			after := f.state(t)
			if tc.changesState {
				if after.revision == before.revision || !after.updatedAt.After(before.updatedAt) {
					t.Fatalf("replace left the collection at %+v (was %+v)", after, before)
				}
			} else if after != before {
				t.Fatalf("unchanged replace moved the collection from %+v to %+v", before, after)
			}
		})
	}

	t.Run("duplicate keeps the first occurrence", func(t *testing.T) {
		if err := f.repo.ReplaceItems(context.Background(), f.collectionID, []LibraryCollectionItemInput{
			{MediaItemID: f.id("e"), SourceRank: 10}, {MediaItemID: f.id("a"), SourceRank: 20}, {MediaItemID: f.id("e"), SourceRank: 30},
		}); err != nil {
			t.Fatalf("replace items: %v", err)
		}
		f.expect(t, nil, "e", "a")
	})
}

type libraryReplaceItemsFixture struct {
	pool         *pgxpool.Pool
	repo         *LibraryCollectionRepository
	suffix       int64
	collectionID string
	old          time.Time
}

type libraryReplaceItemsState struct {
	revision  int64
	updatedAt time.Time
}

func newLibraryReplaceItemsFixture(t *testing.T) *libraryReplaceItemsFixture {
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

	f := &libraryReplaceItemsFixture{pool: pool, repo: NewLibraryCollectionRepository(pool), suffix: time.Now().UnixNano(), old: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, TRUE)
		RETURNING id
	`, fmt.Sprintf("replace-repo-%d", f.suffix)).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		seedSortableItem(t, pool, f.id(name), name, 2000+i)
	}
	collection, err := f.repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("replace-repo-%d", f.suffix), Title: "Replaced", CollectionType: "mdblist"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = f.repo.Delete(context.Background(), collection.ID) })
	f.collectionID = collection.ID
	return f
}

func (f *libraryReplaceItemsFixture) id(name string) string {
	return fmt.Sprintf("replace-repo-%s-%d", name, f.suffix)
}

// replace stores names in order, ranked 10, 20, 30… so a rank change is
// visible apart from a position change.
func (f *libraryReplaceItemsFixture) replace(t *testing.T, names ...string) {
	t.Helper()
	inputs := make([]LibraryCollectionItemInput, len(names))
	for i, name := range names {
		inputs[i] = LibraryCollectionItemInput{MediaItemID: f.id(name), Position: i, SourceRank: (i + 1) * 10}
	}
	if err := f.repo.ReplaceItems(context.Background(), f.collectionID, inputs); err != nil {
		t.Fatalf("replace items: %v", err)
	}
}

func (f *libraryReplaceItemsFixture) state(t *testing.T) libraryReplaceItemsState {
	t.Helper()
	var s libraryReplaceItemsState
	if err := f.pool.QueryRow(context.Background(), `
		SELECT v.revision, c.updated_at
		FROM library_collections c JOIN library_collection_revisions v ON v.collection_id = c.id
		WHERE c.id = $1
	`, f.collectionID).Scan(&s.revision, &s.updatedAt); err != nil {
		t.Fatalf("read collection state: %v", err)
	}
	return s
}

// expect checks the stored order, ranks, and which members kept the aged
// created_at.
func (f *libraryReplaceItemsFixture) expect(t *testing.T, kept []string, names ...string) {
	t.Helper()
	items, err := f.repo.ListItems(context.Background(), f.collectionID)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	got := make([]string, 0, len(items))
	for i, item := range items {
		got = append(got, item.MediaItemID)
		if item.Position != i || item.SourceRank != (i+1)*10 {
			t.Fatalf("item %s at position %d rank %d, want %d rank %d", item.MediaItemID, item.Position, item.SourceRank, i, (i+1)*10)
		}
		wasKept := slices.ContainsFunc(kept, func(name string) bool { return f.id(name) == item.MediaItemID })
		if item.CreatedAt.Equal(f.old) != wasKept {
			t.Fatalf("item %s created_at %s, kept = %v", item.MediaItemID, item.CreatedAt, wasKept)
		}
	}
	want := make([]string, len(names))
	for i, name := range names {
		want[i] = f.id(name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}
