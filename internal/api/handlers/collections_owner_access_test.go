package handlers

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

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
