package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionMutationItemReader interface {
	GetByIDsWithAccess(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, error)
	GetVisibleSeasonsWithAccess(context.Context, []string, catalog.AccessFilter) ([]catalog.VisibleSeasonMember, error)
}

// A guessed catalog identifier must not create membership for an item outside
// the selected viewer's library or rating scope. Both native transports use
// the scope populated by their authentication/profile middleware. A stored
// season is allowed when its series is visible; seasons need the PostgreSQL
// user store, since the SQLite store cannot read them back.
func (h *CollectionHandler) requireVisibleCollectionItem(ctx context.Context, store userstore.UserStore, itemID string) error {
	reader := h.ItemReader
	if reader == nil && h.Executor != nil && h.Executor.Pool != nil {
		reader = catalog.NewItemRepository(h.Executor.Pool)
	}
	if reader == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Catalog access is unavailable")
	}
	filter := AccessFilterFromContext(ctx, "")
	items, err := reader.GetByIDsWithAccess(ctx, []string{itemID}, filter)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check collection item access")
	}
	for _, item := range items {
		if item != nil && item.ContentID == itemID {
			return nil
		}
	}
	seasons, err := reader.GetVisibleSeasonsWithAccess(ctx, []string{itemID}, filter)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check collection item access")
	}
	for _, season := range seasons {
		if season.SeasonID != itemID {
			continue
		}
		if !userstore.HasCatalogSQLState(store) {
			return apiError(http.StatusNotImplemented, "capability_unsupported", "This account's collection storage cannot hold seasons")
		}
		return nil
	}
	return apiError(http.StatusNotFound, "not_found", "Item not found")
}
