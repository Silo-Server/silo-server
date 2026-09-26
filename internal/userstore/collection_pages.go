package userstore

import (
	"context"
	"errors"
)

// ErrCollectionNotFound means no collection exists in the requested account.
var ErrCollectionNotFound = errors.New("collection not found")

// ErrCollectionChanged means that a continuation belongs to an older membership
// or definition. Clients must restart the listing instead of silently skipping rows.
var ErrCollectionChanged = errors.New("collection changed during pagination")
var ErrCollectionPagingUnsupported = errors.New("collection paging is unsupported")

type CollectionItemPosition struct {
	Position    int
	MediaItemID string
}

type CollectionItemsPageOptions struct {
	Limit int
	After *CollectionItemPosition
	// Revision is zero only on the first page.
	Revision int64
}

func (o CollectionItemsPageOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 500 {
		return errors.New("collection page limit must be between 1 and 500")
	}
	if o.Revision < 0 || (o.After != nil && (o.Revision == 0 || o.After.MediaItemID == "")) {
		return errors.New("invalid collection continuation")
	}
	return nil
}

type CollectionItemsPage struct {
	Items    []CollectionItem
	Revision int64
	HasMore  bool
}

// CollectionItemsPager reads a bounded membership window and checks its durable
// revision in the same database snapshot. Callers still enforce viewer access.
type CollectionItemsPager interface {
	CollectionRevision(context.Context, string) (int64, error)
	ListCollectionItemsPage(context.Context, string, CollectionItemsPageOptions) (CollectionItemsPage, error)
}

// CollectionFeatures describes functionality actually implemented by a store.
// Basic manual collection operations and paging remain available in both stores.
type CollectionFeatures struct {
	Groups      bool
	Imports     bool
	Artwork     bool
	ItemReorder bool
}

type CollectionFeatureProvider interface {
	CollectionFeatures() CollectionFeatures
}
