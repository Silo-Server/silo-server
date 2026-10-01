package notifications

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *interestTrackingStore) mutationStore() (userstore.CollectionMutationStore, error) {
	store, ok := s.UserStore.(userstore.CollectionMutationStore)
	if !ok {
		return nil, userstore.ErrCollectionPagingUnsupported
	}
	return store, nil
}
func (s *interestTrackingStore) DeleteCollectionIfRevision(ctx context.Context, id string, expected int64) error {
	store, err := s.mutationStore()
	if err != nil {
		return err
	}
	return store.DeleteCollectionIfRevision(ctx, id, expected)
}
func (s *interestTrackingStore) ReorderCollectionItemsIfRevision(ctx context.Context, id string, ids []string, expected int64) error {
	store, err := s.mutationStore()
	if err != nil {
		return err
	}
	return store.ReorderCollectionItemsIfRevision(ctx, id, ids, expected)
}
func (s *interestTrackingStore) CollectionOrderRevision(ctx context.Context) (int64, error) {
	store, err := s.mutationStore()
	if err != nil {
		return 0, err
	}
	return store.CollectionOrderRevision(ctx)
}
func (s *interestTrackingStore) ReorderCollectionsIfRevision(ctx context.Context, profile string, group *string, ids []string, expected int64) error {
	store, err := s.mutationStore()
	if err != nil {
		return err
	}
	return store.ReorderCollectionsIfRevision(ctx, profile, group, ids, expected)
}
func (s *interestTrackingStore) UpdateCollectionGroupIfRevision(ctx context.Context, id string, name, slug *string, mode *userstore.GroupSortMode, expected int64) (*userstore.CollectionGroup, error) {
	store, err := s.mutationStore()
	if err != nil {
		return nil, err
	}
	return store.UpdateCollectionGroupIfRevision(ctx, id, name, slug, mode, expected)
}
func (s *interestTrackingStore) DeleteCollectionGroupIfRevision(ctx context.Context, id string, expected int64) error {
	store, err := s.mutationStore()
	if err != nil {
		return err
	}
	return store.DeleteCollectionGroupIfRevision(ctx, id, expected)
}
func (s *interestTrackingStore) ReorderCollectionGroupsIfRevision(ctx context.Context, ids []string, expected int64) error {
	store, err := s.mutationStore()
	if err != nil {
		return err
	}
	return store.ReorderCollectionGroupsIfRevision(ctx, ids, expected)
}
