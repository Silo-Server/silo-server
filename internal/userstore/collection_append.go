package userstore

import (
	"context"
	"errors"
)

var (
	ErrCollectionNotManual = errors.New("collection is not manual")
	// ErrCollectionAppendUnsupported is returned by a wrapping store whose
	// wrapped store can't append.
	ErrCollectionAppendUnsupported = errors.New("collection append is unsupported")
)

// CollectionItemAppender adds a title after a manual collection's stored tail,
// allocating its position in the same transaction as the insert. A store that
// implements it reports CollectionFeatures.ItemAppend.
type CollectionItemAppender interface {
	AppendCollectionItem(ctx context.Context, collectionID, mediaItemID string) error
}

// CanAppendCollectionItems reports whether store implements
// CollectionItemAppender, the check the append operation itself makes.
func CanAppendCollectionItems(store UserStore) bool {
	_, ok := store.(CollectionItemAppender)
	return ok
}
