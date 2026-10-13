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
	reader, err := h.itemReader()
	if err != nil {
		return err
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

// itemReader reads catalog items under an access filter: the injected reader,
// else the catalog behind the executor.
func (h *CollectionHandler) itemReader() (collectionMutationItemReader, error) {
	if h.ItemReader != nil {
		return h.ItemReader, nil
	}
	if h.Executor != nil && h.Executor.Pool != nil {
		return catalog.NewItemRepository(h.Executor.Pool), nil
	}
	return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Catalog access is unavailable")
}
