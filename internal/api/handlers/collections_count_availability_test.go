package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestCollectionCountAvailabilityRecoveryDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	rules := make([]catalog.QueryRule, 129)
	for i := range rules {
		rules[i] = catalog.QueryRule{Field: "year", Op: "gte", Value: 2000 + i}
	}
	raw, err := json.Marshal(catalog.QueryDefinition{MediaScope: "movie", Groups: []catalog.QueryGroup{{Rules: rules}}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Legacy", CollectionType: "smart", QueryDefinition: string(raw), IncludeInServerCollections: true})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE user_personal_collections SET item_count=99 WHERE user_id=$1 AND id=$2`, f.account, legacy.ID)
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	assert503 := func(err error) {
		t.Helper()
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Status != http.StatusServiceUnavailable {
			t.Fatalf("count failure=%v, want 503", err)
		}
	}
	_, err = h.ListPersonalCollections(t.Context(), f.account, "owner")
	assert503(err)
	// A failed preflight cannot commit a metadata edit and then report failure.
	name := "Must not persist"
	_, err = h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: legacy.ID, Request: PersonalCollectionUpdateRequest{Name: &name}, V1Rules: true})
	assert503(err)
	stored, err := store.GetCollection(t.Context(), legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "Legacy" {
		t.Fatalf("failed v1 mutation persisted name %q", stored.Name)
	}
	ctx := WithNativeAPIV2(t.Context())
	listed, err := h.ListPersonalCollections(ctx, f.account, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Collections) != 1 || listed.Collections[0].ItemCountStatus != CollectionCountUnavailable || listed.Collections[0].ItemCount != 0 {
		t.Fatalf("legacy unavailable collection: %+v", listed.Collections)
	}
	library := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, catalog.NewItemRepository(f.pool), nil)
	library.Executor, library.UserCollectionPool = h.Executor, f.pool
	library.FolderRepo = catalog.NewFolderRepository(f.pool)
	library.GroupRepo = catalog.NewLibraryCollectionGroupRepository(f.pool)
	if _, err := library.GroupRepo.Create(ctx, catalog.CreateLibraryCollectionGroupInput{LibraryID: f.library, Name: "Personal", Kind: models.GroupKindUserCollections}); err != nil {
		t.Fatal(err)
	}
	userCards, err := library.LibraryUserCollections(ctx, f.library, f.account, "owner")
	if err != nil || len(userCards) != 1 || userCards[0].ItemCountStatus != CollectionCountUnavailable || userCards[0].ItemCount != 0 {
		t.Fatalf("library personal list: %+v, %v", userCards, err)
	}
	tab, err := library.LibraryCollectionsTab(ctx, f.library, f.account, "owner")
	if err != nil || len(tab.Groups) != 1 || len(tab.Groups[0].Collections) != 1 || tab.Groups[0].Collections[0].ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("library tab: %+v, %v", tab, err)
	}
	_, err = library.LibraryUserCollections(t.Context(), f.library, f.account, "owner")
	assert503(err)
	name = "Still editable"
	edited, err := h.UpdatePersonalCollection(ctx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: legacy.ID, Request: PersonalCollectionUpdateRequest{Name: &name}})
	if err != nil || edited.Name != name || edited.ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("v2 metadata edit: %+v, %v", edited, err)
	}
	// Known-ID repair works through frozen v1 even while its list fails.
	repaired, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: legacy.ID, Request: PersonalCollectionUpdateRequest{QueryDefinition: json.RawMessage(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":9999}]}]}`)}, V1Rules: true})
	if err != nil || repaired.ItemCountStatus != CollectionCountAvailable || repaired.ItemCount != 0 {
		t.Fatalf("v1 repair/available empty: %+v, %v", repaired, err)
	}
	// Reintroduce the legacy row through the fixture, then delete by known ID:
	// deletion never depends on successful counting.
	f.exec(t, `UPDATE user_personal_collections SET query_definition=$3::jsonb WHERE user_id=$1 AND id=$2`, f.account, legacy.ID, string(raw))
	if err := h.DeletePersonalCollection(t.Context(), f.account, "owner", legacy.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionCountLedgerAcrossOwnersDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(t.Context(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "other", Name: "Synthetic other"}); err != nil {
		t.Fatal(err)
	}
	var last string
	for i := range 129 {
		owner := "owner"
		if i >= 64 {
			owner = "other"
		}
		c, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: owner, Name: fmt.Sprintf("Synthetic %03d", i), CollectionType: "manual", IsShared: true})
		if err != nil {
			t.Fatal(err)
		}
		last = c.ID
	}
	if err := store.AddCollectionItem(t.Context(), last, f.ids[1], 0); err != nil {
		t.Fatal(err)
	}
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	h.CollectionOwners = &countingCollectionOwners{}
	ctx := WithNativeAPIV2(t.Context())
	listed, err := h.ListPersonalCollections(ctx, f.account, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Collections) != 129 {
		t.Fatalf("retained %d collections, want129", len(listed.Collections))
	}
	available, unavailable := 0, 0
	for _, c := range listed.Collections {
		switch c.ItemCountStatus {
		case CollectionCountAvailable:
			available++
		case CollectionCountUnavailable:
			unavailable++
			if c.ID != last {
				t.Fatalf("unexpected unavailable collection %s", c.ID)
			}
		default:
			t.Fatalf("missing status for %s", c.ID)
		}
	}
	if available != 128 || unavailable != 1 {
		t.Fatalf("counts across owners: available%d unavailable%d", available, unavailable)
	}
	// An unavailable list count never blocks reading stored membership.
	page, err := h.PersonalCollectionItemsPage(ctx, f.account, "owner", last, catalog.AccessFilter{}, userstore.CollectionItemsPageOptions{Limit: 2}, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].MediaItemID != f.ids[1] {
		t.Fatalf("membership remains usable: %+v, %v", page, err)
	}
}

func TestUnavailableCollectionCountSort(t *testing.T) {
	collections := []usercollections.ServerVisibleCollection{{ID: "unknown", ItemCount: 99, ItemCountStatus: CollectionCountUnavailable}, {ID: "empty", ItemCount: 0, ItemCountStatus: CollectionCountAvailable}, {ID: "populated", ItemCount: 2, ItemCountStatus: CollectionCountAvailable}}
	got := applyUserCollectionSort(collections, models.GroupSortMostItems)
	for i, want := range []string{"populated", "empty", "unknown"} {
		if got[i].ID != want {
			t.Fatalf("position%d=%s, want %s", i, got[i].ID, want)
		}
	}
}
