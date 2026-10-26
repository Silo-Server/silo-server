package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLibraryCollectionRailsMatchTheCollectionPageDB checks that a Home or
// library row showing a server collection lists the same titles, in the same
// order, as the collection's own page, for smart collections and for a manual
// collection that carries a query_definition. A cached row costs one statement,
// the revision read that keys it.
func TestLibraryCollectionRailsMatchTheCollectionPageDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	resetResolvedListCacheForTest()
	t.Cleanup(resetResolvedListCacheForTest)
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &nextUpStatementTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("smart-rail-%d", time.Now().UnixNano())
	var shown, other int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix+"-shown").Scan(&shown); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix+"-other").Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{shown, other})
	})
	movie := func(name string, year, library int) string {
		id := prefix + "-" + name
		exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'released','{}',$3)`, id, name, year)
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
		return id
	}
	echo := movie("Echo", 2024, shown)
	alpha := movie("Alpha", 2023, shown)
	delta := movie("Delta", 2022, shown)
	bravo := movie("Bravo", 2010, shown)
	aardvark := movie("Aardvark", 2025, other)

	repo := catalog.NewLibraryCollectionRepository(pool)
	create := func(input catalog.CreateLibraryCollectionInput) *models.LibraryCollection {
		t.Helper()
		input.Slug = prefix + "-" + input.Title
		collection, err := repo.Create(ctx, input)
		if err != nil {
			t.Fatalf("create collection %q: %v", input.Title, err)
		}
		t.Cleanup(func() {
			_ = repo.Delete(context.Background(), collection.ID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id = $1`, collection.ID)
		})
		return collection
	}

	fetcher := NewFetcher(pool)
	fetcher.CollectionRepo = repo
	resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(pool), catalog.NewItemRepository(pool))
	rail := func(collectionID string, libraryID *int, access catalog.AccessFilter) ([]string, int) {
		t.Helper()
		section := ResolvedSection{
			ID:          prefix + "-section",
			SectionType: SectionCollection,
			Title:       "Row",
			ItemLimit:   2,
			Config:      json.RawMessage(fmt.Sprintf(`{"library_collection_id":%q}`, collectionID)),
		}
		got, err := fetcher.FetchOne(ctx, section, libraryID, nil, 0, "", access)
		if err != nil {
			t.Fatalf("fetch rail: %v", err)
		}
		return railContentIDs(got.Items), got.TotalCount
	}
	collectionPage := func(collectionID string, access catalog.AccessFilter) []string {
		t.Helper()
		page, err := resolver.Resolve(ctx, catalog.CatalogRequest{
			Source:         catalog.CatalogSourceLibraryCollection,
			CollectionID:   collectionID,
			CursorPaging:   true,
			UseSourceOrder: true,
			Limit:          10,
		}, access)
		if err != nil {
			t.Fatalf("resolve collection page: %v", err)
		}
		return railContentIDs(page.Items)
	}
	viewer := catalog.AccessFilter{AllowedLibraryIDs: []int{shown}}

	t.Run("smart collection", func(t *testing.T) {
		// The query keeps the three newest movies; the collection's default
		// sort lists them by title. Aardvark is newer but in a library the
		// viewer cannot open, and Bravo is too old to be a member.
		smart := create(catalog.CreateLibraryCollectionInput{
			LibraryIDs:      []int{shown, other},
			Title:           "Smart",
			CollectionType:  "smart",
			QueryDefinition: json.RawMessage(`{"media_scope":"movie","sort":{"field":"year","order":"desc"},"limit":3}`),
			SortConfig:      json.RawMessage(`{"field":"title","order":"asc"}`),
		})

		page := collectionPage(smart.ID, viewer)
		assertContentIDs(t, "collection page", page, alpha, delta, echo)
		got, total := rail(smart.ID, nil, viewer)
		assertContentIDs(t, "rail", got, page[:2]...)
		if total != 3 {
			t.Fatalf("rail total = %d, want 3 members", total)
		}

		// Every request reads the revision, so an edit on any node shows on
		// the next read; the cached titles cost nothing more.
		tracer.take()
		cached, _ := rail(smart.ID, nil, viewer)
		assertContentIDs(t, "cached rail", cached, page[:2]...)
		if queries := tracer.take(); len(queries) != 1 || !strings.Contains(queries[0], "library_collection_revisions") {
			t.Fatalf("cached rail ran %d statements %q, want only the revision read", len(queries), queries)
		}

		libraryRow, _ := rail(smart.ID, &other, catalog.AccessFilter{})
		assertContentIDs(t, "library row", libraryRow, aardvark)

		// A rules edit shows on the next read instead of waiting out the
		// cached rail.
		if err := repo.Update(ctx, catalog.UpdateLibraryCollectionInput{
			ID:         smart.ID,
			SortConfig: json.RawMessage(`{"field":"title","order":"desc"}`),
		}); err != nil {
			t.Fatalf("update collection: %v", err)
		}
		edited, _ := rail(smart.ID, nil, viewer)
		assertContentIDs(t, "rail after edit", edited, echo, delta)
	})

	t.Run("smart collection outside the viewer's libraries", func(t *testing.T) {
		// The query names only a library the viewer cannot open. Narrowed to
		// the viewer's libraries it has no members; it must not fall back to
		// every library.
		hidden := create(catalog.CreateLibraryCollectionInput{
			LibraryIDs:      []int{shown, other},
			Title:           "Hidden",
			CollectionType:  "smart",
			QueryDefinition: json.RawMessage(fmt.Sprintf(`{"library_ids":[%d]}`, shown)),
		})
		outsider := catalog.AccessFilter{AllowedLibraryIDs: []int{other}}
		if got, total := rail(hidden.ID, nil, outsider); len(got) != 0 || total != 0 {
			t.Fatalf("rail = %v (total %d), want nothing", got, total)
		}
		if got := collectionPage(hidden.ID, outsider); len(got) != 0 {
			t.Fatalf("collection page = %v, want nothing", got)
		}
		// Readers that hand the query's own libraries to the executor, such
		// as jellycompat BoxSets and collection items, get nothing either.
		items, total, err := (&catalog.QueryExecutor{Pool: pool}).Preview(ctx, catalog.QueryDefinition{LibraryIDs: []int{shown}}, outsider, 10)
		if err != nil {
			t.Fatalf("preview query: %v", err)
		}
		if got := railContentIDs(items); len(got) != 0 || total != 0 {
			t.Fatalf("query = %v (total %d), want nothing", got, total)
		}
	})

	t.Run("smart collection with an unusable query", func(t *testing.T) {
		// A legacy per-profile rule cannot apply to a shared row. The row
		// lists nothing, like an empty collection, instead of failing every
		// request that shows it.
		legacy := create(catalog.CreateLibraryCollectionInput{
			LibraryIDs:      []int{shown},
			Title:           "Legacy",
			CollectionType:  "smart",
			QueryDefinition: json.RawMessage(`{"groups":[{"match":"all","rules":[{"field":"watched","op":"is","value":true}]}]}`),
		})
		if got, total := rail(legacy.ID, nil, viewer); len(got) != 0 || total != 0 {
			t.Fatalf("rail = %v (total %d), want nothing", got, total)
		}
	})

	t.Run("manual collection with a query definition", func(t *testing.T) {
		// Only collection_type makes a collection live; a manual collection
		// lists its stored items wherever it is shown.
		manual := create(catalog.CreateLibraryCollectionInput{
			LibraryIDs:      []int{shown},
			Title:           "Manual",
			CollectionType:  "manual",
			QueryDefinition: json.RawMessage(`{"media_scope":"movie"}`),
		})
		if err := repo.ReplaceItems(ctx, manual.ID, []catalog.LibraryCollectionItemInput{
			{MediaItemID: bravo, Position: 0},
			{MediaItemID: echo, Position: 1},
		}); err != nil {
			t.Fatalf("store items: %v", err)
		}
		assertContentIDs(t, "collection page", collectionPage(manual.ID, viewer), bravo, echo)
		got, _ := rail(manual.ID, nil, viewer)
		assertContentIDs(t, "rail", got, bravo, echo)
	})
}

func railContentIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

func assertContentIDs(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}
