package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type HomeDismissalHandler struct {
	storeProvider userstore.UserStoreProvider
	EventsHub     *evt.Hub
}

type upsertHomeDismissalRequest struct {
	SeriesID          string `json:"series_id"`
	ProgressUpdatedAt string `json:"progress_updated_at"`
}

func NewHomeDismissalHandler(provider userstore.UserStoreProvider) *HomeDismissalHandler {
	return &HomeDismissalHandler{storeProvider: provider}
}

func (h *HomeDismissalHandler) HandleUpsertDismissal(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	surface := chi.URLParam(r, "surface")
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if !validHomeSurface(surface) {
		writeError(w, http.StatusBadRequest, "bad_request", "Surface is invalid")
		return
	}

	var req upsertHomeDismissalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	dismissal := userstore.HomeItemDismissal{
		ProfileID:   profileID,
		Surface:     surface,
		MediaItemID: itemID,
		DismissedAt: time.Now().UTC().Format(time.RFC3339),
	}

	switch surface {
	case userstore.HomeSurfaceContinueWatching:
		if req.ProgressUpdatedAt == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "progress_updated_at is required")
			return
		}
		dismissal.ProgressUpdatedAt = &req.ProgressUpdatedAt
	case userstore.HomeSurfaceNextUp:
		if req.SeriesID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "series_id is required")
			return
		}
		dismissal.SeriesID = &req.SeriesID
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := store.UpsertHomeDismissal(r.Context(), dismissal); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save dismissal")
		return
	}
	publishUserStateEvent(
		r.Context(),
		h.EventsHub,
		userID,
		profileID,
		itemID,
		req.SeriesID,
		"home_dismissal",
		userStateEventState{},
	)

	w.WriteHeader(http.StatusNoContent)
}

func (h *HomeDismissalHandler) HandleDeleteDismissal(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	surface := chi.URLParam(r, "surface")
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if !validHomeSurface(surface) {
		writeError(w, http.StatusBadRequest, "bad_request", "Surface is invalid")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := store.DeleteHomeDismissal(r.Context(), profileID, surface, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete dismissal")
		return
	}
	publishUserStateEvent(
		r.Context(),
		h.EventsHub,
		userID,
		profileID,
		itemID,
		"",
		"home_dismissal",
		userStateEventState{},
	)

	w.WriteHeader(http.StatusNoContent)
}

func validHomeSurface(surface string) bool {
	return surface == userstore.HomeSurfaceContinueWatching || surface == userstore.HomeSurfaceNextUp
}
