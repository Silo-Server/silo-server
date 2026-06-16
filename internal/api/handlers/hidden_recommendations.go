package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// --- Hidden recommendations ("not interested") ---

// hiddenStoreForRequest resolves the user store and asserts it supports hidden
// recommendations. It writes an error response and returns ok=false on failure.
func (h *PersonalDataHandler) hiddenStoreForRequest(w http.ResponseWriter, r *http.Request) (userstore.HiddenRecommendationStore, bool) {
	userID := apimw.GetUserID(r.Context())
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, false
	}
	hidden, ok := store.(userstore.HiddenRecommendationStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_implemented", "Hidden recommendations are not supported by this server")
		return nil, false
	}
	return hidden, true
}

// HandleListHidden handles GET /hidden.
func (h *PersonalDataHandler) HandleListHidden(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())

	hidden, ok := h.hiddenStoreForRequest(w, r)
	if !ok {
		return
	}

	limit, offset := parsePagination(r)

	entries, err := hidden.ListHiddenRecommendations(r.Context(), profileID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list hidden items")
		return
	}

	items, err := resolveItems(h, r, entries, func(e userstore.HiddenRecommendation) string { return e.MediaItemID })
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve hidden items")
		return
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items})
}

// HandleCheckHidden handles GET /hidden/{item_id}.
func (h *PersonalDataHandler) HandleCheckHidden(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	hidden, ok := h.hiddenStoreForRequest(w, r)
	if !ok {
		return
	}

	isHidden, err := hidden.IsHiddenRecommendation(r.Context(), profileID, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check hidden item")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"hidden": isHidden})
}

// HandleAddHidden handles PUT /hidden/{item_id}.
func (h *PersonalDataHandler) HandleAddHidden(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	hidden, ok := h.hiddenStoreForRequest(w, r)
	if !ok {
		return
	}
	if err := h.ensureAccessibleItem(r, itemID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	if err := hidden.AddHiddenRecommendation(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to hide item")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "hidden", userStateEventState{
		IsHidden: boolPtr(true),
	})
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveHidden handles DELETE /hidden/{item_id}.
func (h *PersonalDataHandler) HandleRemoveHidden(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	hidden, ok := h.hiddenStoreForRequest(w, r)
	if !ok {
		return
	}

	if err := hidden.RemoveHiddenRecommendation(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to un-hide item")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "hidden", userStateEventState{
		IsHidden: boolPtr(false),
	})
	w.WriteHeader(http.StatusNoContent)
}
