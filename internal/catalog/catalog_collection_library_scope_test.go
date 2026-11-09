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

func TestResolveLibraryCollectionMembershipDrawsFromItsRules(t *testing.T) {
	// The libraries a collection shows in never narrow its rules: those only
	// decide which Collections tabs list it.
	cases := []struct {
		name       string
		saved      []int
		collection models.LibraryCollection
		want       []int
	}{
		{name: "rules name libraries", saved: []int{1}, want: []int{1}},
		{name: "rules name none: every library", collection: models.LibraryCollection{LibraryIDs: []int{2, 3}}},
		{name: "a legacy single library leaves the rules alone", saved: []int{1, 2}, collection: models.LibraryCollection{LibraryID: 2}, want: []int{1, 2}},
		{name: "rules reach past the collection's libraries", saved: []int{1, 2}, collection: models.LibraryCollection{LibraryIDs: []int{2, 3}}, want: []int{1, 2}},
		{name: "rules miss the collection's libraries", saved: []int{1}, collection: models.LibraryCollection{LibraryIDs: []int{2}}, want: []int{1}},
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
			if !got.Live || got.OutOfScope {
				t.Fatalf("membership = %+v, want a live query", got)
			}
			if !slices.Equal(got.Query.LibraryIDs, tc.want) {
				t.Fatalf("LibraryIDs = %v, want %v", got.Query.LibraryIDs, tc.want)
			}
		})
	}
}

// A smart collection lists the titles its rules match, from the rules' own
// libraries, even outside the libraries that show it, on every read path. A
// viewer still sees only titles in libraries they may open.
func TestLibraryCollectionDrawsFromItsRulesLibrariesDB(t *testing.T) {
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
	ruleTitle := prefix + "-item-0"
	for _, tc := range []struct {
		access AccessFilter
		want   []string
	}{
		{access: AccessFilter{}, want: []string{ruleTitle}},
		{access: AccessFilter{AllowedLibraryIDs: libraries}, want: []string{ruleTitle}},
		// Opens the collection through the library that shows it, but may
		// not see the rules' library.
		{access: AccessFilter{AllowedLibraryIDs: []int{libraries[1]}}},
	} {
		allowed := tc.access.AllowedLibraryIDs
		for _, cursor := range []bool{false, true} {
			req := CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID, CursorPaging: cursor, Limit: 50}
			result, err := resolver.Resolve(ctx, req, tc.access)
			if err != nil {
				t.Fatalf("cursor=%v allowed=%v: %v", cursor, allowed, err)
			}
			got := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				got = append(got, item.ContentID)
			}
			if !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("cursor=%v allowed=%v: got %v, want %v", cursor, allowed, got, tc.want)
			}
		}
		ids, err := resolver.loadCollectionSourceIDs(ctx, CatalogRequest{Source: CatalogSourceLibraryCollection, CollectionID: c.ID}, tc.access)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids, tc.want) && !(len(ids) == 0 && len(tc.want) == 0) {
			t.Fatalf("allowed=%v: source IDs = %v, want %v", allowed, ids, tc.want)
		}
	}
}
