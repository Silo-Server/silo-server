package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
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

// Every profile on the login reads a shared collection; only its owner
// changes it (403 for the others). A private collection does not exist for
// anyone but its owner (404).
func TestPersonalCollectionLifecycleProfileAccess(t *testing.T) {
	for _, tc := range []struct {
		profile  string
		shared   bool
		wantRead bool
		want     int
	}{
		{profile: "owner", shared: false, wantRead: true},
		{profile: "owner", shared: true, wantRead: true},
		{profile: "viewer", shared: true, wantRead: true, want: 403},
		{profile: "viewer", shared: false, want: 404},
		{profile: "", shared: true, want: 404},
	} {
		t.Run(fmt.Sprintf("%s/shared=%t", tc.profile, tc.shared), func(t *testing.T) {
			profile := tc.profile
			store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", IsShared: tc.shared, CollectionType: "manual"}}
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
				"artwork": func() error { return h.DeletePersonalCollectionImage(t.Context(), 1, profile, "c", "poster") },
			} {
				err := operation()
				if tc.want == 0 {
					if err != nil && name != "artwork" {
						t.Fatalf("%s: %v", name, err)
					}
					continue
				}
				e, ok := errors.AsType[*APIError](err)
				if !ok || e.Status != tc.want {
					t.Errorf("%s error = %v; want %d", name, err, tc.want)
				}
			}
			if tc.want != 0 && store.mutations != 0 {
				t.Fatalf("unauthorized mutations: %d", store.mutations)
			}
			_, err := h.ListPersonalCollectionItems(t.Context(), 1, profile, "c")
			if (err == nil) != tc.wantRead {
				t.Errorf("list error = %v", err)
			}
			_, err = h.GetPersonalCollection(t.Context(), 1, profile, "c")
			if (err == nil) != tc.wantRead {
				t.Errorf("get error = %v", err)
			}
		})
	}
}

func TestPersonalCollectionUpdatePreservesNullableGroupAndImportConfig(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: "mdblist", SourceConfig: `{"url":"https://mdblist.com/lists/user/list","limit":10,"library_ids":[7]}`}}
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

// groupLessStore reports the features of a store without collection groups,
// as both user stores do since groups were retired.
type groupLessStore struct{ *lifecycleStore }

func (groupLessStore) CollectionFeatures() userstore.CollectionFeatures {
	return userstore.CollectionFeatures{Imports: true, Artwork: true, ItemReorder: true, Description: true}
}

type groupLessProvider struct {
	userstore.UserStoreProvider
	store groupLessStore
}

func (p groupLessProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func TestPersonalCollectionUpdateWithoutGroupsIgnoresNullGroup(t *testing.T) {
	for _, body := range []string{`{"name":"Renamed","group_id":null}`, `{"name":"Renamed","group_id":"  "}`} {
		inner := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: "manual"}}
		h := NewCollectionHandler(groupLessProvider{store: groupLessStore{inner}})
		var req PersonalCollectionUpdateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if _, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: req}); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if inner.update.Name == nil || *inner.update.Name != "Renamed" || inner.update.GroupID != nil {
			t.Fatalf("%s: update = %#v, want the rename without a group change", body, inner.update)
		}
	}

	inner := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", CollectionType: "manual"}}
	h := NewCollectionHandler(groupLessProvider{store: groupLessStore{inner}})
	var req PersonalCollectionUpdateRequest
	if err := json.Unmarshal([]byte(`{"name":"Renamed","group_id":"g1"}`), &req); err != nil {
		t.Fatal(err)
	}
	_, err := h.UpdatePersonalCollection(t.Context(), PersonalCollectionUpdateCommand{UserID: 1, ProfileID: "owner", CollectionID: "c", Request: req})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusNotImplemented {
		t.Fatalf("named group error = %#v, want 501", err)
	}
}

func TestPersonalLegacyTraktSourceCannotBeEdited(t *testing.T) {
	store := &lifecycleStore{collection: userstore.Collection{
		ID: "c", CreatorProfileID: "owner",
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

// Sync answers like every other mutation: a collection the profile cannot
// see does not exist for it (404), and a shared collection it does not own
// is refused (403).
func TestSyncPersonalCollectionNotFoundVersusForbidden(t *testing.T) {
	for _, tc := range []struct {
		shared bool
		want   int
	}{{shared: false, want: 404}, {shared: true, want: 403}} {
		store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", IsShared: tc.shared, CollectionType: "mdblist"}}
		h := NewUserCollectionImportHandler(lifecycleProvider{store: store}, &usercollections.Service{}, nil, nil, nil, nil)
		_, err := h.SyncPersonalCollection(t.Context(), 1, "viewer", "c")
		if e, ok := errors.AsType[*APIError](err); !ok || e.Status != tc.want {
			t.Errorf("shared=%t: sync error = %v; want %d", tc.shared, err, tc.want)
		}
	}
}
