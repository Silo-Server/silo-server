package metadata

import (
	"context"
	"fmt"
	"log/slog"
)

// metadataRelinkReconciler is asserted on libraryRepo; the concrete
// *catalog.LibraryItemRepository satisfies it.
type metadataRelinkReconciler interface {
	ReconcileRelinkedItems(ctx context.Context, folderID int, contentIDs []string) (int, int, []string, error)
}

// reconcileRelinkedItems removes the folder memberships that a series-root
// relink left stale on the items its files used to belong to. Scans reconcile
// only content their own files link to, so without this a replaced item stays
// listed in the library with no files until the next full library scan. Items
// left with no files at all are deleted; their stored artwork is reclaimed by
// the artwork storage sweep, as for other items metadata removes.
func (s *MetadataService) reconcileRelinkedItems(ctx context.Context, folderID int, contentIDs []string) error {
	if s == nil || len(contentIDs) == 0 {
		return nil
	}
	reconciler, ok := s.libraryRepo.(metadataRelinkReconciler)
	if !ok {
		return nil
	}
	removed, deleted, _, err := reconciler.ReconcileRelinkedItems(ctx, folderID, contentIDs)
	if err != nil {
		return fmt.Errorf("reconciling relinked items: %w", err)
	}
	if removed > 0 || deleted > 0 {
		slog.InfoContext(ctx, "metadata: reconciled items left by a series root relink", "component", "metadata",
			"folder_id", folderID,
			"memberships_removed", removed,
			"items_deleted", deleted,
		)
	}
	return nil
}
