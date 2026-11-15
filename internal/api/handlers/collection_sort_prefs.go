package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionPreferenceLibraryReader interface {
	GetByID(ctx context.Context, id string) (*models.LibraryCollection, error)
}

// CollectionSortPreferenceRequest saves the sort a viewer chose while browsing
// a collection or personal list. An empty Field pins the viewer to source order.
// DELETE removes the preference and restores the source's default behavior.
type CollectionSortPreferenceRequest struct {
	CollectionKind string `json:"collection_kind"`
	CollectionID   string `json:"collection_id"`
	Field          string `json:"field"`
	Order          string `json:"order"`
}

type CollectionSortPreferenceResponse struct {
	CollectionKind string `json:"collection_kind"`
	CollectionID   string `json:"collection_id"`
	Field          string `json:"field"`
	Order          string `json:"order"`
}

// NormalizeCollectionSortConfig is catalog.NormalizeCollectionSortConfig.
func NormalizeCollectionSortConfig(raw json.RawMessage, allowPersonalized bool) (string, error) {
	return catalog.NormalizeCollectionSortConfig(raw, allowPersonalized)
}

// sortPreferenceKinds is every collection_kind the sort-preference endpoints
// accept. The collection capabilities response advertises it so a client can
// feature-detect the personal-list kinds instead of discovering that an older
// server rejects them by receiving a 400.
var sortPreferenceKinds = []string{
	userstore.CollectionKindLibrary,
	userstore.CollectionKindUser,
	userstore.CollectionKindWatchlist,
	userstore.CollectionKindFavorites,
}

func (h *CollectionHandler) SetPersonalCollectionSortPreference(ctx context.Context, userID int, profileID string, access catalog.AccessFilter, req CollectionSortPreferenceRequest) (CollectionSortPreferenceResponse, error) {
	kind, collectionID, ok := catalog.NormalizeSortPreferenceRef(req.CollectionKind, req.CollectionID)
	if !ok {
		return CollectionSortPreferenceResponse{}, apiError(http.StatusBadRequest, "bad_request", "collection_kind must be 'library', 'user', 'watchlist', or 'favorites'; collection_id is required for collection kinds")
	}

	field, order, valid := catalog.NormalizeSortPreference(kind, req.Field, req.Order)
	if !valid {
		return CollectionSortPreferenceResponse{}, apiError(http.StatusBadRequest, "bad_request", "Unsupported sort field or order for this collection")
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return CollectionSortPreferenceResponse{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if !h.canSaveCollectionSortPreferenceContext(ctx, profileID, access, store, kind, collectionID) {
		// Use one not-found response for absent and inaccessible collections so
		// the preference endpoint cannot be used to enumerate hidden IDs.
		return CollectionSortPreferenceResponse{}, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}

	if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
		ProfileID:      profileID,
		CollectionKind: kind,
		CollectionID:   collectionID,
		SortField:      field,
		SortOrder:      order,
	}); err != nil {
		return CollectionSortPreferenceResponse{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to save sort preference")
	}

	return CollectionSortPreferenceResponse{
		CollectionKind: kind,
		CollectionID:   collectionID,
		Field:          field,
		Order:          order,
	}, nil
}

func (h *CollectionHandler) canSaveCollectionSortPreferenceContext(ctx context.Context, profileID string, access catalog.AccessFilter, store userstore.UserStore, kind, collectionID string) bool {
	switch kind {
	case userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites:
		return true
	case userstore.CollectionKindLibrary:
		if h.LibraryCollections == nil {
			return false
		}
		collection, err := h.LibraryCollections.GetByID(ctx, collectionID)
		return err == nil && catalog.CanAccessLibraryCollection(collection, access)
	case userstore.CollectionKindUser:
		collection, err := store.GetCollection(ctx, collectionID)
		return err == nil && catalog.ProfileCanAccessCollection(collection, profileID)
	default:
		return false
	}
}

func (h *CollectionHandler) ClearPersonalCollectionSortPreference(ctx context.Context, userID int, profileID, rawKind, rawID string) error {
	kind, collectionID, ok := catalog.NormalizeSortPreferenceRef(rawKind, rawID)
	if !ok {
		return apiError(http.StatusBadRequest, "bad_request", "collection_kind must be 'library', 'user', 'watchlist', or 'favorites'; collection_id is required for collection kinds")
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	if err := store.ClearCollectionSortPreference(ctx, profileID, kind, collectionID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear sort preference")
	}

	return nil
}
