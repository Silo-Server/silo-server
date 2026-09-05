package handlers

import (
	"context"
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

// HomeDismissalCommand is a validated dismissal: the profile, the home
// surface, the item, and the surface-specific anchor (the progress stamp a
// Continue Watching dismissal holds for, or the series a Next Up dismissal
// belongs to).
type HomeDismissalCommand struct {
	UserID            int
	ProfileID         string
	Surface           string
	ItemID            string
	SeriesID          string
	ProgressUpdatedAt string
}

// validate applies the per-surface requirements the v1 handler enforces.
func (c HomeDismissalCommand) validate() error {
	switch c.Surface {
	case userstore.HomeSurfaceContinueWatching:
		if c.ProgressUpdatedAt == "" {
			return fieldError("progress_updated_at", "progress_updated_at is required")
		}
	case userstore.HomeSurfaceNextUp:
		if c.SeriesID == "" {
			return fieldError("series_id", "series_id is required")
		}
	}
	return nil
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

	if err := h.DismissHomeItem(r.Context(), HomeDismissalCommand{
		UserID: userID, ProfileID: profileID, Surface: surface, ItemID: itemID,
		SeriesID: req.SeriesID, ProgressUpdatedAt: req.ProgressUpdatedAt,
	}); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DismissHomeItem records the dismissal and publishes the user-state event.
// It is an idempotent upsert: repeating it refreshes the dismissal stamp.
func (h *HomeDismissalHandler) DismissHomeItem(ctx context.Context, cmd HomeDismissalCommand) error {
	if err := cmd.validate(); err != nil {
		return err
	}
	dismissal := userstore.HomeItemDismissal{
		ProfileID:   cmd.ProfileID,
		Surface:     cmd.Surface,
		MediaItemID: cmd.ItemID,
		DismissedAt: time.Now().UTC().Format(time.RFC3339),
	}
	switch cmd.Surface {
	case userstore.HomeSurfaceContinueWatching:
		progressUpdatedAt := cmd.ProgressUpdatedAt
		dismissal.ProgressUpdatedAt = &progressUpdatedAt
	case userstore.HomeSurfaceNextUp:
		seriesID := cmd.SeriesID
		dismissal.SeriesID = &seriesID
	}

	store, err := h.storeProvider.ForUser(ctx, cmd.UserID)
	if err != nil || store == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := store.UpsertHomeDismissal(ctx, dismissal); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to save dismissal")
	}
	publishUserStateEvent(ctx, h.EventsHub, cmd.UserID, cmd.ProfileID, cmd.ItemID, cmd.SeriesID, "home_dismissal", userStateEventState{})
	return nil
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

	if err := h.UndismissHomeItem(r.Context(), userID, profileID, surface, itemID); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UndismissHomeItem removes the dismissal, if any, and publishes the
// user-state event. Removing an absent dismissal succeeds.
func (h *HomeDismissalHandler) UndismissHomeItem(ctx context.Context, userID int, profileID, surface, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := store.DeleteHomeDismissal(ctx, profileID, surface, itemID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete dismissal")
	}
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, itemID, "", "home_dismissal", userStateEventState{})
	return nil
}

func validHomeSurface(surface string) bool {
	return surface == userstore.HomeSurfaceContinueWatching || surface == userstore.HomeSurfaceNextUp
}
