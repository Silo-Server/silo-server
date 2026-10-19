package handlers

import "context"

type collectionItemAppender interface {
	AppendCollectionItem(ctx context.Context, collectionID, mediaItemID string) error
}

// AppendPersonalCollectionItem uses the same owner and catalog access checks as
// positional adds; the store allocates the position from actual membership.
func (h *CollectionHandler) AppendPersonalCollectionItem(ctx context.Context, userID int, profileID, collectionID, itemID string) error {
	return h.addPersonalCollectionItem(ctx, userID, profileID, collectionID, itemID, 0, true)
}

func (h *LibraryCollectionHandler) AppendAdminCollectionItem(ctx context.Context, collectionID, itemID string) error {
	return h.addAdminCollectionItem(ctx, collectionID, itemID, 0, true)
}
