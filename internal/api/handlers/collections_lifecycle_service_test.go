package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type lifecycleStore struct {
	userstore.UserStore
	collection userstore.Collection
	mutations  int
	update     userstore.UpdateCollectionInput
	deleteErr  error
}

func (s *lifecycleStore) GetCollection(context.Context, string) (*userstore.Collection, error) {
	return &s.collection, nil
}
func (s *lifecycleStore) DeleteCollection(context.Context, string) error {
	s.mutations++
	return s.deleteErr
}
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
	if input.PosterURL != nil {
		s.collection.PosterURL = *input.PosterURL
	}
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
			_, err := h.GetPersonalCollection(t.Context(), 1, profile, "c")
			if (err == nil) != tc.wantRead {
				t.Errorf("get error = %v", err)
			}
		})
	}
}

type failingPrefixStore struct{ *blobstoretest.Memory }

func (failingPrefixStore) DeletePrefix(context.Context, string) (int, error) {
	return 0, errors.New("storage unavailable")
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
