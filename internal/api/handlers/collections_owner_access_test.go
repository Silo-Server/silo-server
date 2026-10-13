package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// failingCollectionOwners stands in for an owner whose access cannot be
// resolved, such as a policy evaluation failure.
type failingCollectionOwners struct{}

func (failingCollectionOwners) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, errors.New("owner scope unavailable")
}

// TestSharedPersonalCollectionOwnerAccessDB pins #1615's membership rule: a
// personal collection shared with another profile shows that profile only the
// titles both the owner and the viewer can access, on every read surface, and
// follows the owner's access as it changes.
func TestSharedPersonalCollectionOwnerAccessDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	// The account repository behind the production resolver reads these.
	f.exec(t, `UPDATE users SET email=username||'@example.test', password_hash='' WHERE id=$1`, f.account)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "viewer"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// f.ids[0] lives in f.hidden, the rest in f.library. Ages: G, G, R,
	// PG, unrated. A PG ceiling admits f.ids[0], f.ids[1] and f.ids[3].
	// Each title also carries its own genre, so the catalog filter facets
	// show which titles a collection's facets were built from.
	genre := func(i int) string { return fmt.Sprintf("owner-access-genre-%d", i) }
	for i, age := range []any{0, 0, 17, 8, nil} {
		f.exec(t, `UPDATE media_items SET content_rating_age=$2, genres=ARRAY[$3::text] WHERE content_id=$1`, f.ids[i], age, genre(i))
	}

	shared := func(name, kind, query, display string) *userstore.Collection {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: "owner", Name: name, CollectionType: kind, QueryDefinition: query, DisplayQueryDefinition: display,
			IsShared: true, IncludeInServerCollections: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if kind == "manual" {
			for i, id := range f.ids {
				if err := store.AddCollectionItem(ctx, c.ID, id, i); err != nil {
					t.Fatal(err)
				}
			}
		}
		return c
	}
	manual := shared("Manual", "manual", "", "")
	// A display filter moves the count onto the dynamic count path.
	displayed := shared("Displayed", "manual", "", `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"movie"}]}]}`)
	smart := shared("Smart", "smart", fmt.Sprintf(`{"library_ids":[%d,%d],"media_scope":"movie","match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.library, f.hidden), "")
	// A default sort moves the home row onto the sorted query path.
	sorted := shared("Sorted", "manual", "", "")
	sortConfig := `{"field":"title","order":"asc"}`
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: sorted.ID, RequestProfileID: "owner", SortConfig: &sortConfig}); err != nil {
		t.Fatal(err)
	}
	collections := []*userstore.Collection{manual, displayed, smart, sorted}

	engine, err := policy.NewEngine(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolver := policy.NewViewerResolver(auth.NewUserRepository(f.pool), provider, nil, policy.NewPDP(engine), access.NewGroupStore(f.pool))
	owners := usercollections.NewOwnerAccess(resolver)

	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	libraryHandler := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, catalog.NewItemRepository(f.pool), nil)
	libraryHandler.FolderRepo = catalog.NewFolderRepository(f.pool)
	libraryHandler.Executor = h.Executor
	libraryHandler.UserCollectionPool = f.pool
	catalogResolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(f.pool), catalog.NewItemRepository(f.pool)).WithUserStoreProvider(provider)
	fetcher := sections.NewFetcher(f.pool)
	fetcher.StoreProvider = provider
	wire := func(o catalog.PersonalCollectionAccess) {
		h.CollectionOwners = o
		libraryHandler.CollectionOwners = o
		catalogResolver.WithPersonalCollectionAccess(o)
		fetcher.CollectionOwners = o
	}
	wire(owners)
	sectionHandler := NewSectionHandler(sections.NewRepository(f.pool), fetcher)
	sectionHandler.StoreProvider = provider
	// Each profile's home page carries a row of every collection.
	for _, profile := range []string{"owner", "viewer"} {
		rows := make([]userstore.SectionOverride, 0, len(collections))
		for i, c := range collections {
			position, limit := 1000+i, 50
			rows = append(rows, userstore.SectionOverride{
				ID: "row-" + c.ID, Scope: "home", Position: &position, ItemLimit: &limit, IsUserAdded: true,
				UserSectionType: string(sections.SectionCollection), UserTitle: c.Name,
				UserConfig: fmt.Sprintf(`{"user_collection_id":%q}`, c.ID),
			})
		}
		if err := store.SaveSectionOverrides(ctx, profile, "home", "", rows); err != nil {
			t.Fatal(err)
		}
	}

	setProfile := func(t *testing.T, id, rating string, libraries []int) {
		t.Helper()
		restricted := libraries != nil
		if err := store.UpdateProfile(ctx, id, userstore.UpdateProfileInput{MaxContentRating: &rating, LibraryRestrictionsEnabled: &restricted, AllowedLibraryIDs: &libraries}); err != nil {
			t.Fatal(err)
		}
	}
	reset := func(t *testing.T) {
		t.Helper()
		setProfile(t, "owner", "", nil)
		setProfile(t, "viewer", "", nil)
		wire(owners)
	}
	// readerContext is what the viewer access gate stores for a request.
	readerContext := func(t *testing.T, profileID string) (context.Context, catalog.AccessFilter) {
		t.Helper()
		scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: f.account, ProfileID: profileID, SkipPINVerification: true})
		if err != nil {
			t.Fatal(err)
		}
		reqCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
		reqCtx = apimw.SetProfileID(reqCtx, profileID)
		reqCtx = access.SetScope(reqCtx, scope)
		filter := AccessFilterFromContext(reqCtx, "")
		filter.UserID, filter.ProfileID = f.account, profileID
		return reqCtx, filter
	}
	ids := func(items []string) []string {
		slices.Sort(items)
		return items
	}
	want := func(indexes ...int) []string {
		out := make([]string, 0, len(indexes))
		for _, i := range indexes {
			out = append(out, f.ids[i])
		}
		return ids(out)
	}

	// assertMembers checks every read surface shows profileID exactly the
	// fixture titles at indexes in each collection.
	assertMembers := func(t *testing.T, profileID string, indexes ...int) {
		t.Helper()
		reqCtx, filter := readerContext(t, profileID)
		expected := want(indexes...)
		expectedGenres := make([]string, 0, len(indexes))
		for _, i := range indexes {
			expectedGenres = append(expectedGenres, genre(i))
		}
		slices.Sort(expectedGenres)
		counts := map[string]int{}
		list, err := h.ListPersonalCollections(reqCtx, f.account, profileID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list.Collections {
			counts[c.ID] = c.ItemCount
		}
		tab, err := libraryHandler.LibraryUserCollections(reqCtx, f.library, f.account, profileID)
		if err != nil {
			t.Fatal(err)
		}
		tabCounts := map[string]int{}
		for _, c := range tab {
			tabCounts[c.ID] = c.ItemCount
		}
		for _, c := range collections {
			if got := counts[c.ID]; got != len(expected) {
				t.Errorf("%s: list item_count = %d, want %d", c.Name, got, len(expected))
			}
			if got := tabCounts[c.ID]; got != len(expected) {
				t.Errorf("%s: library tab item_count = %d, want %d", c.Name, got, len(expected))
			}
			detail, err := h.GetPersonalCollection(reqCtx, f.account, profileID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.ItemCount != len(expected) {
				t.Errorf("%s: detail item_count = %d, want %d", c.Name, detail.ItemCount, len(expected))
			}

			var paged []string
			page, err := h.PersonalCollectionItemsPage(reqCtx, f.account, profileID, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 50}, nil)
			if err != nil {
				t.Fatalf("%s: items page: %v", c.Name, err)
			}
			for _, item := range page.Items {
				paged = append(paged, item.MediaItemID)
			}
			if got := ids(paged); !slices.Equal(got, expected) {
				t.Errorf("%s: items page = %v, want %v", c.Name, got, expected)
			}

			for _, cursor := range []bool{true, false} {
				result, err := catalogResolver.Resolve(reqCtx, catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID, CursorPaging: cursor, UseSourceOrder: true, Limit: 50}, filter)
				if err != nil {
					t.Fatalf("%s: catalog (cursor %t): %v", c.Name, cursor, err)
				}
				var got []string
				for _, item := range result.Items {
					got = append(got, item.ContentID)
				}
				if got = ids(got); !slices.Equal(got, expected) {
					t.Errorf("%s: catalog (cursor %t) = %v, want %v", c.Name, cursor, got, expected)
				}
			}

			// The v1 bridge lists stored members (smart collections store
			// none), limited the same way.
			if c.CollectionType == "manual" {
				v1, err := h.ListPersonalCollectionItems(reqCtx, f.account, profileID, c.ID)
				if err != nil {
					t.Fatalf("%s: v1 items: %v", c.Name, err)
				}
				var listed []string
				for _, item := range v1.Items {
					listed = append(listed, item.MediaItemID)
				}
				if got := ids(listed); !slices.Equal(got, expected) {
					t.Errorf("%s: v1 items = %v, want %v", c.Name, got, expected)
				}
			}

			// The catalog filter facets are built from the same members.
			source := catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID}
			facets, err := catalogResolver.ListFilters(reqCtx, source, filter)
			if err != nil {
				t.Fatalf("%s: catalog filters: %v", c.Name, err)
			}
			if got := ids(facets.Genres); !slices.Equal(got, expectedGenres) {
				t.Errorf("%s: catalog filter genres = %v, want %v", c.Name, got, expectedGenres)
			}
			matches, err := catalogResolver.SearchFacet(reqCtx, source, filter, "genre", "owner-access-genre", 50)
			if err != nil {
				t.Fatalf("%s: catalog facet search: %v", c.Name, err)
			}
			if got := ids(matches.Matches); !slices.Equal(got, expectedGenres) {
				t.Errorf("%s: catalog facet search = %v, want %v", c.Name, got, expectedGenres)
			}

			// The home row goes through the production path, which also
			// scopes the row to the viewer's own allowed libraries.
			row, err := sectionHandler.HomeSectionItems(reqCtx, "row-"+c.ID, SectionViewer{Access: filter})
			if err != nil {
				t.Fatalf("%s: home row: %v", c.Name, err)
			}
			var rowIDs []string
			for _, item := range row.Items {
				rowIDs = append(rowIDs, item.ContentID)
			}
			if got := ids(rowIDs); !slices.Equal(got, expected) {
				t.Errorf("%s: home row = %v, want %v", c.Name, got, expected)
			}
		}
	}

	t.Run("both unrestricted see every member", func(t *testing.T) {
		reset(t)
		assertMembers(t, "viewer", 0, 1, 2, 3, 4)
	})
	t.Run("a PG owner limits an unrestricted viewer", func(t *testing.T) {
		reset(t)
		setProfile(t, "owner", "PG", nil)
		assertMembers(t, "viewer", 0, 1, 3)
		// The owner's limit does not hide those titles from the viewer
		// anywhere else: the viewer's own reads are unchanged.
		_, viewerFilter := readerContext(t, "viewer")
		visible, err := catalog.NewItemRepository(f.pool).GetByIDsWithAccess(ctx, f.ids, viewerFilter)
		if err != nil {
			t.Fatal(err)
		}
		if len(visible) != len(f.ids) {
			t.Fatalf("viewer's own access sees %d titles, want %d", len(visible), len(f.ids))
		}
	})
	t.Run("an unrestricted owner keeps a PG viewer's limit", func(t *testing.T) {
		reset(t)
		setProfile(t, "viewer", "PG", nil)
		assertMembers(t, "viewer", 0, 1, 3)
	})
	t.Run("an owner who loses a library hides its titles until access returns", func(t *testing.T) {
		reset(t)
		setProfile(t, "owner", "", []int{f.library})
		assertMembers(t, "viewer", 1, 2, 3, 4)
		setProfile(t, "owner", "", nil)
		assertMembers(t, "viewer", 0, 1, 2, 3, 4)
	})
	t.Run("a restricted viewer sees only the narrower owner's libraries", func(t *testing.T) {
		reset(t)
		setProfile(t, "viewer", "", []int{f.library, f.hidden})
		setProfile(t, "owner", "", []int{f.library})
		assertMembers(t, "viewer", 1, 2, 3, 4)
		setProfile(t, "owner", "", []int{f.hidden})
		assertMembers(t, "viewer", 0)
		// A viewer allowed no library sees no home row member, even of an
		// unrestricted owner's collection. (The library tab and other
		// surfaces answer not found for such a viewer, so only the row is
		// checked.)
		setProfile(t, "owner", "", nil)
		setProfile(t, "viewer", "", []int{})
		reqCtx, filter := readerContext(t, "viewer")
		for _, c := range collections {
			row, err := sectionHandler.HomeSectionItems(reqCtx, "row-"+c.ID, SectionViewer{Access: filter})
			if err != nil {
				t.Fatalf("%s: home row: %v", c.Name, err)
			}
			if len(row.Items) != 0 {
				t.Errorf("%s: home row shows %d items to a viewer allowed no library, want none", c.Name, len(row.Items))
			}
		}
	})
	t.Run("both limits apply together", func(t *testing.T) {
		reset(t)
		setProfile(t, "owner", "", []int{f.library})
		setProfile(t, "viewer", "PG", nil)
		assertMembers(t, "viewer", 1, 3)
	})
	t.Run("disjoint library limits hide a title both reach through different libraries", func(t *testing.T) {
		// Known limit of IntersectAccess: allowed libraries intersect as
		// lists, not per title. f.ids[1] is in both libraries and each
		// profile can open it, but no library is allowed to both, so the
		// shared collection hides it. It fails closed: nothing outside
		// either profile's access is shown.
		reset(t)
		f.exec(t, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.ids[1], f.hidden)
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, f.ids[1], f.hidden)
		})
		setProfile(t, "owner", "", []int{f.hidden})
		setProfile(t, "viewer", "", []int{f.library})
		for _, profile := range []string{"owner", "viewer"} {
			_, own := readerContext(t, profile)
			visible, err := catalog.NewItemRepository(f.pool).GetByIDsWithAccess(ctx, f.ids[1:2], own)
			if err != nil {
				t.Fatal(err)
			}
			if len(visible) != 1 {
				t.Fatalf("%s cannot open the title in both libraries on its own", profile)
			}
		}
		assertMembers(t, "viewer")
	})
	t.Run("the owner's hidden libraries do not limit the viewer", func(t *testing.T) {
		reset(t)
		// A restricted owner is the case that matters: hiding a library
		// removes it from the owner's own allowed list.
		setProfile(t, "owner", "", []int{f.library, f.hidden})
		if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
			Key: settingskeys.UiDisabledLibraryIds, Scope: settingscontract.ScopeProfile, ProfileID: "owner",
		}, json.RawMessage(fmt.Sprintf(`[%d]`, f.hidden))); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
				Key: settingskeys.UiDisabledLibraryIds, Scope: settingscontract.ScopeProfile, ProfileID: "owner",
			}, json.RawMessage(`[]`)); err != nil {
				t.Error(err)
			}
		})
		assertMembers(t, "viewer", 0, 1, 2, 3, 4)
	})
	t.Run("the owner reads its own collections with its own access and no owner lookup", func(t *testing.T) {
		reset(t)
		setProfile(t, "owner", "", []int{f.library})
		wire(failingCollectionOwners{})
		assertMembers(t, "owner", 1, 2, 3, 4)
	})
	t.Run("an unresolvable owner hides the collection rather than widening it", func(t *testing.T) {
		reset(t)
		setProfile(t, "owner", "PG", nil)
		wire(failingCollectionOwners{})
		reqCtx, filter := readerContext(t, "viewer")
		list, err := h.ListPersonalCollections(reqCtx, f.account, "viewer")
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Collections) != 0 {
			t.Errorf("list shows %d collections whose owner is unavailable, want none", len(list.Collections))
		}
		tab, err := libraryHandler.LibraryUserCollections(reqCtx, f.library, f.account, "viewer")
		if err != nil {
			t.Fatal(err)
		}
		if len(tab) != 0 {
			t.Errorf("library tab shows %d collections whose owner is unavailable, want none", len(tab))
		}
		// Ordering never reads members, so it never depends on resolving an
		// owner. A profile orders only its own collections: the viewer has
		// none, and the owner's order still offers all of its own.
		order, err := h.PersonalCollectionOrderEditor(reqCtx, f.account, "viewer")
		if err != nil {
			t.Fatal(err)
		}
		if len(order.OrderedIDs) != 0 {
			t.Errorf("viewer's order editor = %v, want none of the owner's collections", order.OrderedIDs)
		}
		order, err = h.PersonalCollectionOrderEditor(reqCtx, f.account, "owner")
		if err != nil {
			t.Fatal(err)
		}
		wantOrder := []string{manual.ID, displayed.ID, smart.ID, sorted.ID}
		if got := ids(slices.Clone(order.OrderedIDs)); !slices.Equal(got, ids(wantOrder)) {
			t.Errorf("owner's order editor = %v, want %v", order.OrderedIDs, wantOrder)
		}
		for _, c := range collections {
			if _, err := h.GetPersonalCollection(reqCtx, f.account, "viewer", c.ID); err == nil {
				t.Errorf("%s: detail answered without its owner's access", c.Name)
			}
			_, err := h.PersonalCollectionItemsPage(reqCtx, f.account, "viewer", c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 50}, nil)
			assertPagingAPIStatus(t, err, 500)
			if c.CollectionType == "manual" {
				_, err = h.ListPersonalCollectionItems(reqCtx, f.account, "viewer", c.ID)
				assertPagingAPIStatus(t, err, 500)
			}
			for _, cursor := range []bool{true, false} {
				if _, err := catalogResolver.Resolve(reqCtx, catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID, CursorPaging: cursor, UseSourceOrder: true, Limit: 50}, filter); !errors.Is(err, catalog.ErrPersonalCollectionOwnerAccess) {
					t.Errorf("%s: catalog (cursor %t) err = %v, want ErrPersonalCollectionOwnerAccess", c.Name, cursor, err)
				}
			}
			source := catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID}
			if _, err := catalogResolver.ListFilters(reqCtx, source, filter); !errors.Is(err, catalog.ErrPersonalCollectionOwnerAccess) {
				t.Errorf("%s: catalog filters err = %v, want ErrPersonalCollectionOwnerAccess", c.Name, err)
			}
			if _, err := fetcher.FetchOne(reqCtx, sections.ResolvedSection{
				ID: "row-" + c.ID, SectionType: sections.SectionCollection, Title: c.Name, ItemLimit: 50,
				Config: json.RawMessage(fmt.Sprintf(`{"user_collection_id":%q}`, c.ID)),
			}, nil, nil, f.account, "viewer", filter); !errors.Is(err, catalog.ErrPersonalCollectionOwnerAccess) {
				t.Errorf("%s: home row err = %v, want ErrPersonalCollectionOwnerAccess", c.Name, err)
			}
		}
	})
	t.Run("a smart collection outside the allowed libraries shows nothing", func(t *testing.T) {
		// The query's own libraries share none with the reader's
		// (owner-narrowed) allowed list. Dropping the empty intersection
		// would run the query across every library.
		reset(t)
		hiddenQuery := fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.hidden)
		outside := []*userstore.Collection{
			shared("Hidden smart", "smart", hiddenQuery, ""),
			shared("Hidden smart displayed", "smart", hiddenQuery, `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"movie"}]}]}`),
		}
		for _, profile := range []string{"owner", "viewer"} {
			rows := make([]userstore.SectionOverride, 0, len(outside))
			for i, c := range outside {
				position, limit := 2000+i, 50
				rows = append(rows, userstore.SectionOverride{
					ID: "row-" + c.ID, Scope: "home", Position: &position, ItemLimit: &limit, IsUserAdded: true,
					UserSectionType: string(sections.SectionCollection), UserTitle: c.Name,
					UserConfig: fmt.Sprintf(`{"user_collection_id":%q}`, c.ID),
				})
			}
			if err := store.SaveSectionOverrides(ctx, profile, "home", "", rows); err != nil {
				t.Fatal(err)
			}
		}
		assertNone := func(t *testing.T, profileID string) {
			t.Helper()
			reqCtx, filter := readerContext(t, profileID)
			for _, c := range outside {
				detail, err := h.GetPersonalCollection(reqCtx, f.account, profileID, c.ID)
				if err != nil {
					t.Fatal(err)
				}
				if detail.ItemCount != 0 {
					t.Errorf("%s: detail item_count = %d, want 0", c.Name, detail.ItemCount)
				}
				page, err := h.PersonalCollectionItemsPage(reqCtx, f.account, profileID, c.ID, filter, userstore.CollectionItemsPageOptions{Limit: 50}, nil)
				if err != nil {
					t.Fatalf("%s: items page: %v", c.Name, err)
				}
				if len(page.Items) != 0 {
					t.Errorf("%s: items page shows %d items, want none", c.Name, len(page.Items))
				}
				for _, cursor := range []bool{true, false} {
					result, err := catalogResolver.Resolve(reqCtx, catalog.CatalogRequest{Source: catalog.CatalogSourceUserCollection, CollectionID: c.ID, CursorPaging: cursor, UseSourceOrder: true, Limit: 50}, filter)
					if err != nil {
						t.Fatalf("%s: catalog (cursor %t): %v", c.Name, cursor, err)
					}
					if len(result.Items) != 0 {
						t.Errorf("%s: catalog (cursor %t) shows %d items, want none", c.Name, cursor, len(result.Items))
					}
				}
				row, err := sectionHandler.HomeSectionItems(reqCtx, "row-"+c.ID, SectionViewer{Access: filter})
				if err != nil {
					t.Fatalf("%s: home row: %v", c.Name, err)
				}
				if len(row.Items) != 0 {
					t.Errorf("%s: home row shows %d items, want none", c.Name, len(row.Items))
				}
				// A library page scopes the row to its own library, here
				// one outside the allowed list.
				libraryRow, err := fetcher.FetchOne(reqCtx, sections.ResolvedSection{
					ID: "row-" + c.ID, SectionType: sections.SectionCollection, Title: c.Name, ItemLimit: 50,
					Config: json.RawMessage(fmt.Sprintf(`{"user_collection_id":%q}`, c.ID)),
				}, &f.hidden, nil, f.account, profileID, filter)
				if err != nil {
					t.Fatalf("%s: library row: %v", c.Name, err)
				}
				if len(libraryRow.Items) != 0 {
					t.Errorf("%s: library row shows %d items, want none", c.Name, len(libraryRow.Items))
				}
			}
		}
		setProfile(t, "owner", "", []int{f.library})
		// An unrestricted viewer's home row carries no library scope.
		assertNone(t, "viewer")
		// A restricted viewer's home row carries its own allowed libraries.
		setProfile(t, "viewer", "", []int{f.library, f.hidden})
		assertNone(t, "viewer")
		// The owner's own reads stay within its own allowed libraries.
		assertNone(t, "owner")
	})
}

type countingCollectionOwners struct{ calls []string }

func (c *countingCollectionOwners) OwnerFilter(_ context.Context, _ int, owner string) (catalog.AccessFilter, error) {
	c.calls = append(c.calls, owner)
	return catalog.AccessFilter{}, nil
}

// TestPersonalCollectionOwnerLookupBudgetDB pins what the owner limit costs:
// a listing resolves each other owner once however many of its collections
// it shows, and a profile reading its own collections resolves nobody.
func TestPersonalCollectionOwnerLookupBudgetDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	create := func(creator, kind, query, display string, shared bool) *userstore.Collection {
		t.Helper()
		input := userstore.CreateCollectionInput{CreatorProfileID: creator, Name: kind, CollectionType: kind, QueryDefinition: query, DisplayQueryDefinition: display, IncludeInServerCollections: true}
		if shared {
			input.IsShared = true
		}
		c, err := store.CreateCollection(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddCollectionItem(ctx, c.ID, f.ids[1], 0); err != nil {
			t.Fatal(err)
		}
		return c
	}
	smartDef := fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","match":"all","groups":[],"sort":{"field":"title","order":"asc"}}`, f.library)
	ownerManual := create("owner", "manual", "", "", true)
	create("owner", "smart", smartDef, "", true)
	create("owner", "manual", "", `{"match":"all","groups":[{"match":"all","rules":[{"field":"type","op":"is","value":"movie"}]}]}`, true)
	create("viewer", "manual", "", "", false)

	owners := &countingCollectionOwners{}
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	h.CollectionOwners = owners
	libraryHandler := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, catalog.NewItemRepository(f.pool), nil)
	libraryHandler.FolderRepo = catalog.NewFolderRepository(f.pool)
	libraryHandler.Executor = h.Executor
	libraryHandler.UserCollectionPool = f.pool
	libraryHandler.CollectionOwners = owners

	expect := func(t *testing.T, what string, want []string) {
		t.Helper()
		if !slices.Equal(owners.calls, want) {
			t.Fatalf("%s resolved owners %v, want %v", what, owners.calls, want)
		}
		owners.calls = nil
	}
	list, err := h.ListPersonalCollections(ctx, f.account, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Collections) != 4 {
		t.Fatalf("viewer lists %d collections, want 4", len(list.Collections))
	}
	expect(t, "the viewer's listing", []string{"owner"})
	tab, err := libraryHandler.LibraryUserCollections(ctx, f.library, f.account, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(tab) != 4 {
		t.Fatalf("viewer's library tab lists %d collections, want 4", len(tab))
	}
	expect(t, "the viewer's library tab", []string{"owner"})

	if _, err := h.ListPersonalCollections(ctx, f.account, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.GetPersonalCollection(ctx, f.account, "owner", ownerManual.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PersonalCollectionItemsPage(ctx, f.account, "owner", ownerManual.ID, catalog.AccessFilter{}, userstore.CollectionItemsPageOptions{Limit: 10}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := libraryHandler.LibraryUserCollections(ctx, f.library, f.account, "owner"); err != nil {
		t.Fatal(err)
	}
	expect(t, "the owner's own reads", nil)
}
