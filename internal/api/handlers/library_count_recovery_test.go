package handlers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLibraryCardLegacyCountAvailabilityRecovery(t *testing.T) {
	groups := make([]catalog.QueryGroup, 17)
	raw, err := json.Marshal(catalog.QueryDefinition{Groups: groups})
	if err != nil {
		t.Fatal(err)
	}
	c := &models.LibraryCollection{ID: "synthetic", Title: "Legacy", CollectionType: "smart", LibraryID: 1, QueryDefinition: raw, ItemCountStatus: CollectionCountUnavailable}
	if err := catalog.ValidateLibraryCollectionQueryBudget(c.LibraryID, c.LibraryIDs, c.QueryDefinition); !errors.Is(err, catalog.ErrQueryComplexity) {
		t.Fatalf("fixture admission: %v", err)
	}
	h := &LibraryCollectionHandler{}
	card := h.libraryTabCardOf(t.Context(), c)
	if card.ID != c.ID || card.Title != c.Title || card.ItemCountStatus != CollectionCountUnavailable || card.ItemCount != 0 {
		t.Fatalf("legacy card: %+v", card)
	}
	c.QueryDefinition = json.RawMessage(`{"groups":[]}`)
	c.ItemCount = 0
	c.ItemCountStatus = CollectionCountAvailable
	card = h.libraryTabCardOf(t.Context(), c)
	if card.ID != c.ID || card.ItemCountStatus != CollectionCountAvailable || card.ItemCount != 0 {
		t.Fatalf("recovered available zero: %+v", card)
	}
}

func TestUnavailableCuratedCollectionCountSortAndV1(t *testing.T) {
	collections := []*models.LibraryCollection{
		{ID: "unavailable", ItemCount: 99, ItemCountStatus: CollectionCountUnavailable},
		{ID: "zero", ItemCountStatus: CollectionCountAvailable},
		{ID: "populated", ItemCount: 2, ItemCountStatus: CollectionCountAvailable},
	}
	for i, c := range applyCollectionSort(collections, models.GroupSortMostItems) {
		if want := []string{"populated", "zero", "unavailable"}[i]; c.ID != want {
			t.Fatalf("position %d = %s, want %s", i, c.ID, want)
		}
	}
	if err := v1CuratedCollectionCountError(WithNativeAPIV2(t.Context()), collections); err != nil {
		t.Fatalf("v2 lost collection access: %v", err)
	}
	err := v1CuratedCollectionCountError(t.Context(), collections)
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 503 || apiErr.Code != "collection_count_unavailable" {
		t.Fatalf("v1 unavailable count = %v", err)
	}
}

func TestLibraryCollectionLegacyCountRecoveryDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := WithNativeAPIV2(t.Context())
	repo := catalog.NewLibraryCollectionRepository(f.pool)
	repaired := json.RawMessage(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":9999}]}]}`)
	c, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: f.library, Title: "Synthetic legacy", Slug: "synthetic-legacy-count", CollectionType: "smart", QueryDefinition: repaired})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(catalog.QueryDefinition{Groups: make([]catalog.QueryGroup, 17)})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE library_collections SET query_definition=$2::jsonb, item_count_cached=99, item_count_cached_at=NOW() WHERE id=$1`, c.ID, string(legacy))
	refresher := &catalog.SmartCountRefresher{Pool: f.pool, Executor: &catalog.QueryExecutor{Pool: f.pool}}
	if err := refresher.RefreshOne(ctx, c.ID); !errors.Is(err, catalog.ErrQueryComplexity) {
		t.Fatalf("legacy refresh = %v", err)
	}
	loaded, err := repo.GetByID(ctx, c.ID)
	if err != nil || loaded.ItemCountStatus != CollectionCountUnavailable || loaded.ItemCount != 0 {
		t.Fatalf("legacy repository read = %+v, %v", loaded, err)
	}
	var storedCount int
	if err := f.pool.QueryRow(ctx, `SELECT item_count_cached FROM library_collections WHERE id=$1`, c.ID).Scan(&storedCount); err != nil || storedCount != 99 {
		t.Fatalf("read changed stored cache = %d, %v", storedCount, err)
	}
	h := NewLibraryCollectionHandler(repo, nil, catalog.NewItemRepository(f.pool), nil)
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	h.GroupRepo = catalog.NewLibraryCollectionGroupRepository(f.pool)
	assertTab := func(grouped bool, status string) {
		t.Helper()
		tab, err := h.LibraryCollectionsTab(ctx, f.library, f.account, "owner")

		if err != nil {
			t.Fatal(err)
		}
		assertLibraryCollectionTabCount(t, tab, grouped, c.ID, status)
	}
	assertTab(false, CollectionCountUnavailable)
	if _, err := h.LibraryCollectionsTab(t.Context(), f.library, f.account, "owner"); err == nil {
		t.Fatal("v1 returned a false zero instead of a count error")
	}
	server, err := h.ListServerCollections(ctx, catalog.AccessFilter{})
	if err != nil || len(server.Libraries) != 1 || server.Libraries[0].Collections[0].ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("server cards = %+v, %v", server, err)
	}
	admin, err := h.ListAdminCollections(ctx, &f.library)
	if err != nil || len(admin.Collections) != 1 || admin.Collections[0].ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("admin list = %+v, %v", admin, err)
	}
	renamed, err := h.UpdateAdminCollection(ctx, c.ID, AdminCollectionUpdate{Title: new("Still editable")})
	if err != nil || renamed.Title != "Still editable" || renamed.ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("metadata recovery = %+v, %v", renamed, err)
	}
	group, err := h.GroupRepo.Create(ctx, catalog.CreateLibraryCollectionGroupInput{LibraryID: f.library, Name: "Synthetic group", Kind: models.GroupKindRegular, DefaultSortMode: models.GroupSortMostItems})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE library_collection_libraries SET group_id=$2 WHERE collection_id=$1`, c.ID, group.ID)
	assertTab(true, CollectionCountUnavailable)
	if _, err := h.UpdateAdminCollection(ctx, c.ID, AdminCollectionUpdate{QueryDefinition: repaired}); err != nil {
		t.Fatalf("definition repair = %v", err)
	}
	// The existing cache is refreshed asynchronously by the application. Call it
	// directly here so the zero comes from the executor, without timing sleeps.
	if err := refresher.RefreshOne(ctx, c.ID); err != nil {
		t.Fatalf("repaired refresh = %v", err)
	}
	assertTab(true, CollectionCountAvailable)
	if _, err := h.LibraryCollectionsTab(t.Context(), f.library, f.account, "owner"); err != nil {
		t.Fatalf("v1 real zero did not recover: %v", err)
	}
	// Deletion of a rejected definition also stays independent of counting.
	f.exec(t, `UPDATE library_collections SET query_definition=$2::jsonb WHERE id=$1`, c.ID, string(legacy))
	if err := h.DeleteAdminCollection(t.Context(), c.ID); err != nil {
		t.Fatalf("known-ID deletion = %v", err)
	}
}

func assertLibraryCollectionTabCount(t *testing.T, tab LibraryCollectionTabView, grouped bool, id, status string) {
	t.Helper()
	if len(tab.Collections) != 1 || tab.Collections[0].ItemCountStatus != status {
		t.Fatalf("curated response = %+v", tab)
	}
	var cards []libraryTabCollection
	if grouped && len(tab.Groups) == 1 {
		cards = tab.Groups[0].Collections
	} else if !grouped && tab.Ungrouped != nil {
		cards = tab.Ungrouped.Collections
	}
	if len(cards) != 1 || cards[0].ID != id || cards[0].ItemCountStatus != status || cards[0].ItemCount != 0 {
		t.Fatalf("library cards = %+v", cards)
	}
}
