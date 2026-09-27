package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type collectionMutationItemReader interface {
	GetByIDsWithAccess(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, error)
}

// A guessed catalog identifier must not create membership for an item outside
// the selected viewer's library or rating scope. Both native transports use
// the scope populated by their authentication/profile middleware.
func (h *CollectionHandler) requireVisibleCollectionItem(ctx context.Context, itemID string) error {
	reader := h.ItemReader
	if reader == nil && h.Executor != nil && h.Executor.Pool != nil {
		reader = catalog.NewItemRepository(h.Executor.Pool)
	}
	if reader == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Catalog access is unavailable")
	}
	items, err := reader.GetByIDsWithAccess(ctx, []string{itemID}, AccessFilterFromContext(ctx, ""))
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check collection item access")
	}
	for _, item := range items {
		if item != nil && item.ContentID == itemID {
			return nil
		}
	}
	return apiError(http.StatusNotFound, "not_found", "Item not found")
}
