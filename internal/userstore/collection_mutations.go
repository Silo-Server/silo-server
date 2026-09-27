package userstore

import (
	"context"
	"errors"
)

var ErrCollectionRevisionMismatch = errors.New("collection mutation revision does not match")

// CollectionMutationStore compares a supplied witness inside the same
// transaction that changes the collection. Order/group witnesses cover the
// account's collection and group rows; item mutations use CollectionRevision.
type CollectionMutationStore interface {
	DeleteCollectionIfRevision(context.Context, string, int64) error
	ReorderCollectionItemsIfRevision(context.Context, string, []string, int64) error
	CollectionOrderRevision(context.Context) (int64, error)
	ReorderCollectionsIfRevision(context.Context, string, *string, []string, int64) error
	UpdateCollectionGroupIfRevision(context.Context, string, *string, *string, *GroupSortMode, int64) (*CollectionGroup, error)
	DeleteCollectionGroupIfRevision(context.Context, string, int64) error
	ReorderCollectionGroupsIfRevision(context.Context, []string, int64) error
}
