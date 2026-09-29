package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type orderEditorStore struct {
	userstore.UserStore
	collections []userstore.Collection
}

func (s *orderEditorStore) ListCollections(context.Context, string) ([]userstore.Collection, error) {
	return s.collections, nil
}
func (s *orderEditorStore) ListCollectionGroups(context.Context) ([]userstore.CollectionGroup, error) {
	return nil, nil
}
func (s *orderEditorStore) CollectionOrderRevision(context.Context) (int64, error) { return 3, nil }

type orderEditorProvider struct {
	userstore.UserStoreProvider
	store *orderEditorStore
}

func (p orderEditorProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func TestPersonalCollectionOrderEditorListsOnlyOwnCollections(t *testing.T) {
	group := "g"
	// ListCollections returns every collection the profile can see, so the
	// same rows serve both profiles here.
	store := &orderEditorStore{collections: []userstore.Collection{
		{ID: "shared", CreatorProfileID: "other", AllowedProfileIDs: []string{"other", "owner"}},
		{ID: "own-1", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"}},
		{ID: "own-grouped", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"}, GroupID: &group},
		{ID: "own-2", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner", "other"}},
	}}
	h := NewCollectionHandler(orderEditorProvider{store: store})
	for _, tc := range []struct {
		profile string
		group   *string
		want    []string
	}{
		{profile: "owner", want: []string{"own-1", "own-2"}},
		{profile: "owner", group: &group, want: []string{"own-grouped"}},
		{profile: "other", want: []string{"shared"}},
		{profile: "other", group: &group, want: []string{}},
	} {
		view, err := h.PersonalCollectionOrderEditor(t.Context(), 1, tc.profile, tc.group)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view.OrderedIDs, tc.want) || view.Revision != 3 {
			t.Fatalf("%s group %v: got %v at revision %d, want %v", tc.profile, tc.group, view.OrderedIDs, view.Revision, tc.want)
		}
	}
}
