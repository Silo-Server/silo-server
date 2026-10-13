package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveLibraryCollectionMembershipNarrowsToCollectionLibraries(t *testing.T) {
	cases := []struct {
		name       string
		saved      []int
		collection models.LibraryCollection
		want       []int
		wantOK     bool
	}{
		{name: "no collection libraries keeps the saved scope", saved: []int{1}, want: []int{1}, wantOK: true},
		{name: "no saved scope takes the collection's libraries", collection: models.LibraryCollection{LibraryIDs: []int{2, 3}}, want: []int{2, 3}, wantOK: true},
		{name: "single library collection narrows the saved scope", saved: []int{1, 2}, collection: models.LibraryCollection{LibraryID: 2}, want: []int{2}, wantOK: true},
		{name: "overlap keeps the shared libraries", saved: []int{1, 2}, collection: models.LibraryCollection{LibraryIDs: []int{2, 3}}, want: []int{2}, wantOK: true},
		{name: "no overlap matches nothing", saved: []int{1}, collection: models.LibraryCollection{LibraryIDs: []int{2}}, wantOK: false},
		{name: "no overlap with a single library matches nothing", saved: []int{1}, collection: models.LibraryCollection{LibraryID: 2}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition, err := json.Marshal(QueryDefinition{LibraryIDs: tc.saved})
			if err != nil {
				t.Fatal(err)
			}
			tc.collection.CollectionType = "smart"
			tc.collection.QueryDefinition = definition
			got, err := ResolveLibraryCollectionMembership(&tc.collection, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Live {
				t.Fatal("smart collection resolved as stored")
			}
			if ok := !got.OutOfScope; ok != tc.wantOK {
				t.Fatalf("in scope = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && !slices.Equal(got.Query.LibraryIDs, tc.want) {
				t.Fatalf("LibraryIDs = %v, want %v", got.Query.LibraryIDs, tc.want)
			}
		})
	}
}

// A smart collection whose saved library_ids miss the collection's own
// libraries must resolve empty on every read path, not widen to every library.
func TestLibraryCollectionWithoutLibraryOverlapResolvesEmptyDB(t *testing.T) {
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

	prefix := fmt.Sprintf("catalog-collection-scope-%d", time.Now().UnixNano())
	libraries := make([]int, 2)
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	}()
	for i, library := range libraries {
		id := fmt.Sprintf("%s-item-%d", prefix, i)
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'movie',$1,'released','{}')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library); err != nil {
			t.Fatal(err)
		}
	}

	definition, err := json.Marshal(QueryDefinition{LibraryIDs: []int{libraries[0]}})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewLibraryCollectionRepository(pool)
	c, err := repo.Create(ctx, CreateLibraryCollectionInput{LibraryID: libraries[1], Slug: prefix, Title: "Scope", CollectionType: "smart", QueryDefinition: definition})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = repo.Delete(context.Background(), c.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, c.ID)
	}()

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	for _, access := range []AccessFilter{{}, {AllowedLibraryIDs: libraries}} {
		for _, cursor := range []bool{false, true} {
			req := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: cursor, Limit: 50}
			result, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatalf("cursor=%v allowed=%v: %v", cursor, access.AllowedLibraryIDs, err)
			}
			if len(result.Items) != 0 || result.Total != 0 {
				t.Fatalf("cursor=%v allowed=%v: got %d items (total %d), want none", cursor, access.AllowedLibraryIDs, len(result.Items), result.Total)
			}
		}
		ids, err := resolver.loadCollectionSourceIDs(ctx, CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID}, access)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 0 {
			t.Fatalf("allowed=%v: source IDs = %v, want none", access.AllowedLibraryIDs, ids)
		}
	}
}
