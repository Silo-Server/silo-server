package notifications

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *interestTrackingStore) ListCollectionItemsPage(ctx context.Context, collectionID string, opts userstore.CollectionItemsPageOptions) (userstore.CollectionItemsPage, error) {
	pager, ok := s.UserStore.(userstore.CollectionItemsPager)
	if !ok {
		return userstore.CollectionItemsPage{}, userstore.ErrCollectionPagingUnsupported
	}
	return pager.ListCollectionItemsPage(ctx, collectionID, opts)
}

func (s *interestTrackingStore) CollectionFeatures() userstore.CollectionFeatures {
	if provider, ok := s.UserStore.(userstore.CollectionFeatureProvider); ok {
		return provider.CollectionFeatures()
	}
	return userstore.CollectionFeatures{}
}

func (s *interestTrackingStore) CollectionRevision(ctx context.Context, collectionID string) (int64, error) {
	pager, ok := s.UserStore.(userstore.CollectionItemsPager)
	if !ok {
		return 0, userstore.ErrCollectionPagingUnsupported
	}
	return pager.CollectionRevision(ctx, collectionID)
}
