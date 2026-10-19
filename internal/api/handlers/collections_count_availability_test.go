package handlers

import (
	"encoding/json"
	"errors"
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

func TestUnavailableCollectionCountSort(t *testing.T) {
	collections := []usercollections.ServerVisibleCollection{{ID: "unknown", ItemCount: 99, ItemCountStatus: CollectionCountUnavailable}, {ID: "empty", ItemCount: 0, ItemCountStatus: CollectionCountAvailable}, {ID: "populated", ItemCount: 2, ItemCountStatus: CollectionCountAvailable}}
	got := applyUserCollectionSort(collections, models.GroupSortMostItems)
	for i, want := range []string{"populated", "empty", "unknown"} {
		if got[i].ID != want {
			t.Fatalf("position%d=%s, want %s", i, got[i].ID, want)
		}
	}
}
