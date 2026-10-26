package userstore

import "context"

// CollectionMembershipReader answers which collections hold a title in one
// read, so a picker can mark them without listing every collection's items.
// Callers still check that the viewer may access the title.
type CollectionMembershipReader interface {
	// ManualCollectionsHolding returns the ids of creatorProfileID's own
	// manual collections that hold mediaItemID, in no particular order.
	ManualCollectionsHolding(ctx context.Context, creatorProfileID, mediaItemID string) ([]string, error)
}
