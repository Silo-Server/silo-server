package catalog

import "context"

// AppendItemIfAbsent allocates the tail position under the shared parent lock,
// retaining the native add operation's type, scope and duplicate checks.
func (r *LibraryCollectionRepository) AppendItemIfAbsent(ctx context.Context, collectionID, mediaItemID string) error {
	return r.addItemIfAbsent(ctx, collectionID, mediaItemID, 0, true)
}
