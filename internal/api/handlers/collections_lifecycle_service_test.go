package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type lifecycleStore struct {
	userstore.UserStore
	collection userstore.Collection
	mutations  int
	update     userstore.UpdateCollectionInput
}

func (s *lifecycleStore) GetCollection(context.Context, string) (*userstore.Collection, error) {
	return &s.collection, nil
}
func (s *lifecycleStore) DeleteCollection(context.Context, string) error { s.mutations++; return nil }
func (s *lifecycleStore) AddCollectionItem(context.Context, string, string, int) error {
	s.mutations++
	return nil
}
func (s *lifecycleStore) RemoveCollectionItem(context.Context, string, string) error {
	s.mutations++
	return nil
}
func (s *lifecycleStore) ReorderCollectionItems(context.Context, string, []string) error {
	s.mutations++
	return nil
}
func (s *lifecycleStore) UpdateCollection(_ context.Context, input userstore.UpdateCollectionInput) error {
	s.mutations++
	s.update = input
	return nil
}
func (s *lifecycleStore) ListCollectionItems(context.Context, string) ([]userstore.CollectionItem, error) {
	return []userstore.CollectionItem{}, nil
}

type lifecycleProvider struct {
	userstore.UserStoreProvider
	store *lifecycleStore
}

func (p lifecycleProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func TestPersonalCollectionLifecycleProfileAccess(t *testing.T) {
	for _, profile := range []string{"owner", "viewer", "hidden", ""} {
		t.Run(profile, func(t *testing.T) {
			store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner", "viewer"}, CollectionType: "manual"}}
			h := NewCollectionHandler(lifecycleProvider{store: store})
			h.ItemReader = &collectionGuardReader{items: []*models.MediaItem{{ContentID: "item"}}}
			for name, operation := range map[string]func() error{
				"delete":  func() error { return h.DeletePersonalCollection(t.Context(), 1, profile, "c") },
				"add":     func() error { return h.AddPersonalCollectionItem(t.Context(), 1, profile, "c", "item", 0) },
				"remove":  func() error { return h.RemovePersonalCollectionItem(t.Context(), 1, profile, "c", "item") },
				"reorder": func() error { return h.ReorderPersonalCollectionItems(t.Context(), 1, profile, "c", []string{"item"}) },
				"update": func() error {
					_, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: profile, CollectionID: "c"})
					return err
				},
			} {
				err := operation()
				if profile == "owner" {
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					continue
				}
				want := 404
				if profile == "viewer" {
					want = 403
				}
				e, ok := errors.AsType[*APIError](err)
				if !ok || e.Status != want {
					t.Errorf("%s error = %v; want %d", name, err, want)
				}
			}
			if profile != "owner" && store.mutations != 0 {
				t.Fatalf("unauthorized mutations: %d", store.mutations)
			}
			_, err := h.ListPersonalCollectionItems(t.Context(), 1, profile, "c")
			if (err == nil) != (profile == "owner" || profile == "viewer") {
				t.Errorf("list error = %v", err)
			}
			_, err = h.GetPersonalCollection(t.Context(), 1, profile, "c")
			if (err == nil) != (profile == "owner" || profile == "viewer") {
				t.Errorf("get error = %v", err)
			}
			if profile != "owner" {
				if err := h.DeletePersonalCollectionImage(t.Context(), 1, profile, "c", "poster"); err == nil {
					t.Fatal("unauthorized artwork deletion accepted")
				}
			}
		})
	}
}

func TestPersonalCollectionUpdatePreservesNullableGroupAndImportConfig(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"}, CollectionType: "mdblist", SourceConfig: `{"url":"https://mdblist.com/lists/user/list","limit":10,"library_ids":[7]}`}}
	h := NewCollectionHandler(lifecycleProvider{store: store})
	var req PersonalCollectionUpdateRequest
	if err := json.Unmarshal([]byte(`{"group_id":null,"max_items":0}`), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: req}); err != nil {
		t.Fatal(err)
	}
	if store.update.GroupID == nil || *store.update.GroupID != nil {
		t.Fatalf("group null not preserved: %#v", store.update.GroupID)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(*store.update.SourceConfigPatch), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 1 || cfg["limit"] != nil {
		t.Fatalf("patch must only clear limit: %#v", cfg)
	}
}

func TestPersonalLegacyTraktSourceCannotBeEdited(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{
		ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"},
		CollectionType: "trakt", SourceConfig: `{"url":"https://trakt.tv/users/example/lists/list"}`,
	}}
	h := NewCollectionHandler(lifecycleProvider{store: store})
	maxItems := 25
	_, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{
		UserID: 1, ProfileID: "owner", CollectionID: "c",
		Request: PersonalCollectionUpdateRequest{MaxItems: &maxItems},
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "legacy_source_immutable" {
		t.Fatalf("error = %#v, want legacy_source_immutable", err)
	}
	if store.mutations != 0 {
		t.Fatalf("source edit caused %d mutations", store.mutations)
	}
}
