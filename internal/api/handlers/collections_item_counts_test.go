package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestPersonalCollectionItemCountsDB is the regression test for #1552:
// personal collections reported item_count 0 whatever their membership. Every
// surface that shows the count must report the members the acting profile can
// see, and follow membership changes.
func TestPersonalCollectionItemCountsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Manual", CollectionType: "manual", IncludeInServerCollections: true})
	if err != nil {
		t.Fatal(err)
	}
	// f.ids[0] lives in the library the viewer below cannot see.
	for i, id := range f.ids[:3] {
		if err := store.AddCollectionItem(t.Context(), manual.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	smartDef := fmt.Sprintf(`{"library_ids":[%d,%d],"media_scope":"movie","match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.library, f.hidden)
	smart, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Smart", CollectionType: "smart", QueryDefinition: smartDef, IncludeInServerCollections: true})
	if err != nil {
		t.Fatal(err)
	}

	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	libraryHandler := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, catalog.NewItemRepository(f.pool), nil)
	libraryHandler.FolderRepo = catalog.NewFolderRepository(f.pool)
	libraryHandler.Executor = h.Executor
	libraryHandler.UserCollectionPool = f.pool
	restricted := access.SetScope(t.Context(), access.Scope{AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})

	assertCounts := func(t *testing.T, ctx context.Context, wantManual, wantSmart int) {
		t.Helper()
		want := map[string]int{manual.ID: wantManual, smart.ID: wantSmart}
		list, err := h.ListPersonalCollections(ctx, f.account, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Collections) != len(want) {
			t.Fatalf("listed %d collections, want %d", len(list.Collections), len(want))
		}
		for _, c := range list.Collections {
			if c.ItemCount != want[c.ID] {
				t.Errorf("list: %s item_count = %d, want %d", c.Name, c.ItemCount, want[c.ID])
			}
			detail, err := h.GetPersonalCollection(ctx, f.account, "owner", c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.ItemCount != want[c.ID] {
				t.Errorf("detail: %s item_count = %d, want %d", c.Name, detail.ItemCount, want[c.ID])
			}
		}
		tab, err := libraryHandler.LibraryUserCollections(ctx, f.library, f.account, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if len(tab) != len(want) {
			t.Fatalf("library tab lists %d collections, want %d", len(tab), len(want))
		}
		for _, c := range tab {
			if c.ItemCount != want[c.ID] {
				t.Errorf("library tab: %s item_count = %d, want %d", c.Name, c.ItemCount, want[c.ID])
			}
		}
	}

	t.Run("hidden library members are not counted", func(t *testing.T) {
		assertCounts(t, restricted, 2, 4)
	})
	t.Run("unrestricted viewer counts every member", func(t *testing.T) {
		assertCounts(t, t.Context(), 3, 5)
	})
	t.Run("counts follow membership changes", func(t *testing.T) {
		if err := store.AddCollectionItem(t.Context(), manual.ID, f.ids[3], 3); err != nil {
			t.Fatal(err)
		}
		assertCounts(t, restricted, 3, 4)
		for _, id := range f.ids[1:3] {
			if err := store.RemoveCollectionItem(t.Context(), manual.ID, id); err != nil {
				t.Fatal(err)
			}
		}
		assertCounts(t, restricted, 1, 4)
	})
	t.Run("counts match the catalog view, display filters included", func(t *testing.T) {
		series := fmt.Sprintf("%s-series", f.ids[0])
		f.exec(t, `INSERT INTO media_items(content_id,type,title) VALUES($1,'series','Same title')`, series)
		f.exec(t, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, series, f.library)
		t.Cleanup(func() {
			if _, err := f.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series); err != nil {
				t.Errorf("cleanup series: %v", err)
			}
		})
		display := func(kind string) string {
			return `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"` + kind + `"}]}]}`
		}
		// Every movie in the fixture's two libraries plus the series; the viewer
		// sees f.ids[1:] and the series.
		smartAll := fmt.Sprintf(`{"library_ids":[%d,%d],"match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.library, f.hidden)
		cases := []struct {
			name, kind, query, display string
			members                    []string
			want                       int
		}{
			{"mixed members", "manual", "", "", []string{f.ids[0], f.ids[1], series}, 2},
			{"mixed members, movies only", "manual", "", display("movie"), []string{f.ids[0], f.ids[1], series}, 1},
			{"mixed members, series only", "manual", "", display("series"), []string{f.ids[0], f.ids[1], series}, 1},
			{"smart", "smart", smartAll, "", nil, 5},
			{"smart, movies only", "smart", smartAll, display("movie"), nil, 4},
			{"smart, series only", "smart", smartAll, display("series"), nil, 1},
		}
		resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(f.pool), catalog.NewItemRepository(f.pool)).WithUserStoreProvider(provider)
		viewer := AccessFilterFromContext(restricted, "")
		viewer.UserID, viewer.ProfileID = f.account, "owner"
		for _, tc := range cases {
			c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: tc.name, CollectionType: tc.kind, QueryDefinition: tc.query, DisplayQueryDefinition: tc.display})
			if err != nil {
				t.Fatal(err)
			}
			for i, id := range tc.members {
				if err := store.AddCollectionItem(t.Context(), c.ID, id, i); err != nil {
					t.Fatal(err)
				}
			}
			got, err := h.GetPersonalCollection(restricted, f.account, "owner", c.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := resolver.Resolve(t.Context(), catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID, CursorPaging: true, UseSourceOrder: true, Limit: 1}, viewer)
			if err != nil {
				t.Fatalf("%s: catalog view: %v", tc.name, err)
			}
			if got.ItemCount != tc.want || view.Total != tc.want {
				t.Errorf("%s: item_count = %d, catalog view total = %d, want %d", tc.name, got.ItemCount, view.Total, tc.want)
			}
			if err := store.DeleteCollection(t.Context(), c.ID); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("a new smart collection reports its matches", func(t *testing.T) {
		created, err := h.CreatePersonalCollection(restricted, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{
			Name: "Created smart", CollectionType: "smart", QueryDefinition: []byte(smartDef),
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.DeleteCollection(context.Background(), created.ID) })
		if created.ItemCount != 4 {
			t.Fatalf("create: item_count = %d, want 4", created.ItemCount)
		}
	})
}

func TestPersonalCollectionCountMatchesDisabledLibraryViewDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Disabled libraries", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range f.ids[:3] {
		if err := store.AddCollectionItem(t.Context(), c.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	// One member belongs to a disabled library, one has lost its last library
	// membership, and one remains in the visible library.
	f.exec(t, `DELETE FROM media_item_libraries WHERE content_id=$1`, f.ids[1])
	ctx := access.SetScope(t.Context(), access.Scope{DisabledLibraryIDs: []int{f.hidden}})
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	got, err := h.GetPersonalCollection(ctx, f.account, "owner", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(f.pool), catalog.NewItemRepository(f.pool)).WithUserStoreProvider(provider)
	viewer := AccessFilterFromContext(ctx, "")
	viewer.UserID, viewer.ProfileID = f.account, "owner"
	page, err := resolver.Resolve(ctx, catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID, CursorPaging: true, UseSourceOrder: true, Limit: 10}, viewer)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("count=%d, catalog total=%d, listed=%d", got.ItemCount, page.Total, len(page.Items))
	if got.ItemCount != 1 || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ContentID != f.ids[2] {
		t.Fatalf("disabled or orphan member visible: count=%d total=%d items=%v", got.ItemCount, page.Total, page.Items)
	}
}
