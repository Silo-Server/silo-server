package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionPreferenceLibraryReader interface {
	GetByID(ctx context.Context, id string) (*models.LibraryCollection, error)
}

// collectionSortPreferenceRequest saves the sort a viewer chose while browsing
// a collection or personal list. An empty Field pins the viewer to source order.
// DELETE removes the preference and restores the source's default behavior.
type collectionSortPreferenceRequest struct {
	CollectionKind string `json:"collection_kind"`
	CollectionID   string `json:"collection_id"`
	Field          string `json:"field"`
	Order          string `json:"order"`
}

type collectionSortPreferenceResponse struct {
	CollectionKind string `json:"collection_kind"`
	CollectionID   string `json:"collection_id"`
	Field          string `json:"field"`
	Order          string `json:"order"`
}

// NormalizeCollectionSortConfig validates the default sort a collection's
// creator configured and returns its canonical sort_config JSON. Shared by the
// personal and library collection handlers so both id spaces accept exactly the
// same vocabulary.
//
// allowPersonalized must be false for library collection defaults because
// those defaults are shared across profiles. Viewer overrides are validated
// separately and may use profile-scoped fields for either collection kind.
func NormalizeCollectionSortConfig(raw json.RawMessage, allowPersonalized bool) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == jsonNullLiteral || trimmed == "{}" {
		return "{}", nil
	}
	var cfg struct {
		Field string `json:"field"`
		Order string `json:"order"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", errInvalidSortConfig
	}
	if strings.TrimSpace(cfg.Field) == "" {
		// sort_config predates default sorting and may contain independent
		// collection modes such as {"mode":"manual_pins"}. Preserve those
		// configurations byte-for-byte instead of erasing them merely because
		// they do not contain a sort field.
		return string(raw), nil
	}
	qs, ok := catalog.NormalizeCollectionSort(cfg.Field, cfg.Order, allowPersonalized)
	if !ok {
		return "", errInvalidSortConfig
	}
	return catalog.EncodeCollectionDefaultSort(qs, true)
}

var errInvalidSortConfig = &sortConfigError{}

type sortConfigError struct{}

func (*sortConfigError) Error() string {
	return "sort_config must be {} or {\"field\":\"<supported sort>\",\"order\":\"asc|desc\"}"
}

// HandleSetCollectionSortPreference handles PUT /collections/sort-preference.
func (h *CollectionHandler) HandleSetCollectionSortPreference(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req collectionSortPreferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	kind, collectionID, ok := normalizeCollectionRef(req.CollectionKind, req.CollectionID)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "collection_kind must be 'library', 'user', 'watchlist', or 'favorites'; collection_id is required for collection kinds")
		return
	}

	field, order, valid := normalizeSortPreference(kind, req.Field, req.Order)
	if !valid {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported sort field or order for this collection")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if !h.canSaveCollectionSortPreference(r, store, kind, collectionID) {
		// Use one not-found response for absent and inaccessible collections so
		// the preference endpoint cannot be used to enumerate hidden IDs.
		writeError(w, http.StatusNotFound, "not_found", "Collection not found")
		return
	}

	if err := store.SetCollectionSortPreference(r.Context(), userstore.CollectionSortPreference{
		ProfileID:      profileID,
		CollectionKind: kind,
		CollectionID:   collectionID,
		SortField:      field,
		SortOrder:      order,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save sort preference")
		return
	}

	writeJSON(w, http.StatusOK, collectionSortPreferenceResponse{
		CollectionKind: kind,
		CollectionID:   collectionID,
		Field:          field,
		Order:          order,
	})
}

func (h *CollectionHandler) canSaveCollectionSortPreference(
	r *http.Request,
	store userstore.UserStore,
	kind, collectionID string,
) bool {
	switch kind {
	case userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites:
		return true
	case userstore.CollectionKindLibrary:
		if h.LibraryCollections == nil {
			return false
		}
		collection, err := h.LibraryCollections.GetByID(r.Context(), collectionID)
		return err == nil && catalog.CanAccessLibraryCollection(collection, requestAccessFilter(r))
	case userstore.CollectionKindUser:
		collection, err := store.GetCollection(r.Context(), collectionID)
		return err == nil && catalog.ProfileCanAccessCollection(collection, apimw.GetProfileID(r.Context()))
	default:
		return false
	}
}

// HandleClearCollectionSortPreference handles DELETE /collections/sort-preference,
// returning the collection or personal list to its default behavior.
func (h *CollectionHandler) HandleClearCollectionSortPreference(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	values := r.URL.Query()
	kind, collectionID, ok := normalizeCollectionRef(values.Get("collection_kind"), values.Get("collection_id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "collection_kind must be 'library', 'user', 'watchlist', or 'favorites'; collection_id is required for collection kinds")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.ClearCollectionSortPreference(r.Context(), profileID, kind, collectionID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear sort preference")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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

func normalizeCollectionRef(rawKind, rawID string) (string, string, bool) {
	kind := strings.ToLower(strings.TrimSpace(rawKind))
	if isPersonalSortPreferenceKind(kind) {
		return kind, userstore.PersonalSortPreferenceCollectionID, true
	}
	if kind != userstore.CollectionKindLibrary && kind != userstore.CollectionKindUser {
		return "", "", false
	}
	collectionID := strings.TrimSpace(rawID)
	if collectionID == "" {
		return "", "", false
	}
	return kind, collectionID, true
}

func isPersonalSortPreferenceKind(kind string) bool {
	return kind == userstore.CollectionKindWatchlist || kind == userstore.CollectionKindFavorites
}

func normalizeSortPreference(kind, rawField, rawOrder string) (string, string, bool) {
	field := strings.ToLower(strings.TrimSpace(rawField))
	if field == "" {
		return "", "", true
	}
	if isPersonalSortPreferenceKind(kind) {
		qs, ok := catalog.NormalizePersonalSourceSort(field, rawOrder)
		return qs.Field, qs.Order, ok
	}
	qs, ok := catalog.NormalizeCollectionSort(field, rawOrder, true)
	return qs.Field, qs.Order, ok
}
