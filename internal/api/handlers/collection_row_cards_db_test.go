package handlers

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestCollectionRowCardsFollowTheViewerDB pins which server collections a row
// of collections shows a viewer, in the row's order: one that is missing,
// hidden, in no library the viewer can open, or whose titles the viewer sees
// none of is left out, and each card carries the viewer's count and a library
// that holds it. On a library page only that library's collections show.
func TestCollectionRowCardsFollowTheViewerDB(t *testing.T) {
	f := seedViewerCountCollections(t)
	ctx := t.Context()
	var other int
	if err := f.pool.QueryRow(ctx, `SELECT library_id FROM library_collection_libraries WHERE collection_id = $1 AND library_id <> $2`, f.ids["mixed"], f.shown).Scan(&other); err != nil {
		t.Fatal(err)
	}
	extra := func(name, visibility string, library int) string {
		t.Helper()
		c, err := f.repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: library, LibraryIDs: []int{library},
			Slug: fmt.Sprintf("row-cards-%s-%s", name, f.ids["mixed"]), Title: name, CollectionType: "manual", Visibility: visibility})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, c.ID)
		})
		return c.ID
	}
	hiddenFromViewers := extra("hidden-from-viewers", "hidden", f.shown)
	elsewhereOnly := extra("elsewhere-only", "visible", other)

	h := NewLibraryCollectionHandler(f.repo, nil, catalog.NewItemRepository(f.pool), nil)
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	cards := &CollectionRowCards{Library: h}
	refs := []recipes.CollectionRef{
		{LibraryCollectionID: f.ids["smart"]},
		{LibraryCollectionID: f.ids["hidden"]},
		{LibraryCollectionID: "no-such-collection"},
		{LibraryCollectionID: hiddenFromViewers},
		{LibraryCollectionID: elsewhereOnly},
		{LibraryCollectionID: f.ids["mixed"]},
		{LibraryCollectionID: f.ids["empty"]},
	}
	pgViewer := access.Scope{AllowedLibraryIDs: []int{f.shown}, LibrariesRestricted: true, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}
	type shown struct {
		id      string
		count   int
		library int
	}
	for _, tc := range []struct {
		name  string
		scope *access.Scope
		page  *int
		want  []shown
	}{
		{"limited viewer on Home", &pgViewer, nil, []shown{{f.ids["smart"], 2, f.shown}, {f.ids["mixed"], 1, f.shown}, {f.ids["empty"], 0, f.shown}}},
		{"other library's page", nil, &other, []shown{{elsewhereOnly, 0, other}, {f.ids["mixed"], 3, other}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.scope != nil {
				ctx = access.SetScope(ctx, *tc.scope)
			}
			got, err := cards.CollectionCards(ctx, refs, sections.CollectionCardScope{LibraryID: tc.page, Filter: AccessFilterFromContext(ctx, ""), AdminRow: true})
			if err != nil {
				t.Fatal(err)
			}
			var gotShown []shown
			for _, card := range got {
				if card.Source != sections.CollectionCardLibrary || card.LibraryID == nil {
					t.Fatalf("card %+v has no server collection library", card)
				}
				gotShown = append(gotShown, shown{card.ID, card.ItemCount, *card.LibraryID})
			}
			if !slices.Equal(gotShown, tc.want) {
				t.Errorf("row shows %v, want %v", gotShown, tc.want)
			}
		})
	}
}

// TestCollectionRowCardsShowVisiblePersonalCollectionsDB: a profile's row of
// collections shows its own personal collections and other profiles' shared
// ones, in the row's order, never another profile's unshared one; an admin
// row shows no personal collection.
func TestCollectionRowCardsShowVisiblePersonalCollectionsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	viewer, other := f.ids[0]+"-viewer", f.ids[0]+"-other"
	for _, profile := range []string{viewer, other} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile}); err != nil {
			t.Fatal(err)
		}
	}
	create := func(owner, name string, shared bool) string {
		t.Helper()
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: owner, Name: name, CollectionType: "manual", IsShared: shared})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	own, shared, private := create(viewer, "Own", false), create(other, "Shared", true), create(other, "Private", false)

	personal := NewCollectionHandler(provider)
	personal.Executor = &catalog.QueryExecutor{Pool: f.pool}
	personal.CollectionOwners = &countingCollectionOwners{}
	cards := &CollectionRowCards{Personal: personal}
	refs := []recipes.CollectionRef{{UserCollectionID: shared}, {UserCollectionID: private}, {UserCollectionID: own}}
	scope := sections.CollectionCardScope{UserID: f.account, ProfileID: viewer}
	got, err := cards.CollectionCards(ctx, refs, scope)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, card := range got {
		if card.Source != sections.CollectionCardUser || card.CreatorProfileID == nil {
			t.Fatalf("card %+v is not a personal collection card", card)
		}
		ids = append(ids, card.ID)
	}
	if want := []string{shared, own}; !slices.Equal(ids, want) {
		t.Errorf("profile row shows %v, want %v", ids, want)
	}
	scope.AdminRow = true
	if got, err := cards.CollectionCards(ctx, refs, scope); err != nil || len(got) != 0 {
		t.Errorf("admin row shows %v (%v), want no personal collections", got, err)
	}
}
