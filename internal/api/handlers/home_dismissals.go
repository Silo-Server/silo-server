package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type HomeDismissalHandler struct {
	storeProvider userstore.UserStoreProvider
	EventsHub     *evt.Hub

	seriesDrops       seriesDropStore
	droppedDispatcher LocalDroppedEventDispatcher
	// dropAccess checks that the viewer may see a series before it is
	// dropped, since a drop is sent on to the profile's watch providers.
	dropAccess interface {
		EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
	}
}

// seriesDropStore records dropped series. catalog.DroppedSeriesRepo satisfies it.
type seriesDropStore interface {
	ResolveDropSeries(ctx context.Context, itemID string) (string, bool, error)
	Drop(ctx context.Context, userID int, profileID, seriesID string) error
	Undrop(ctx context.Context, userID int, profileID, seriesID string) error
}

// LocalDroppedEventDispatcher sends a profile's dropped-series changes to its
// watch providers.
type LocalDroppedEventDispatcher interface {
	HandleLocalDroppedEvent(ctx context.Context, event watchsync.LocalDroppedEvent) error
}

type upsertHomeDismissalRequest struct {
	SeriesID          string `json:"series_id"`
	ProgressUpdatedAt string `json:"progress_updated_at"`
}

func NewHomeDismissalHandler(provider userstore.UserStoreProvider) *HomeDismissalHandler {
	return &HomeDismissalHandler{storeProvider: provider}
}

// SetSeriesDrops makes dismissing an episode or series drop the whole series.
// Without it every dismissal hides only the dismissed card. access, when set,
// refuses a drop of a series outside the viewer's library access.
func (h *HomeDismissalHandler) SetSeriesDrops(drops seriesDropStore, access interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}) {
	h.seriesDrops = drops
	h.dropAccess = access
}

// SetLocalDroppedEventDispatcher configures where dropped-series changes are
// sent for watch-provider sync.
func (h *HomeDismissalHandler) SetLocalDroppedEventDispatcher(dispatcher LocalDroppedEventDispatcher) {
	h.droppedDispatcher = dispatcher
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

	// The frozen v1 contract keeps per-card dismissals; only v2 drops shows.
	if err := h.dismissHomeItem(r.Context(), false, HomeDismissalCommand{
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
//
// Dismissing an episode or a series from either surface drops the whole
// series instead: every card of it leaves Next Up and Continue Watching until
// the profile watches it again or undoes the dismissal, and the drop is sent
// to the profile's watch providers.
func (h *HomeDismissalHandler) DismissHomeItem(ctx context.Context, cmd HomeDismissalCommand) error {
	return h.dismissHomeItem(ctx, true, cmd)
}

func (h *HomeDismissalHandler) dismissHomeItem(ctx context.Context, dropSeries bool, cmd HomeDismissalCommand) error {
	if err := cmd.validate(); err != nil {
		return err
	}
	seriesID, drop, err := h.dropSeriesFor(ctx, dropSeries, cmd.ItemID)
	if err != nil {
		return err
	}
	if drop {
		if h.dropAccess != nil {
			if err := h.dropAccess.EnsureAccessible(ctx, seriesID, AccessFilterFromContext(ctx, "")); err != nil {
				if errors.Is(err, catalog.ErrItemNotFound) {
					return apiError(http.StatusNotFound, "not_found", "Item not found")
				}
				return apiError(http.StatusInternalServerError, "internal_error", "Failed to check item access")
			}
		}
		if err := h.seriesDrops.Drop(ctx, cmd.UserID, cmd.ProfileID, seriesID); err != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to save dismissal")
		}
		publishUserStateEvent(ctx, h.EventsHub, cmd.UserID, cmd.ProfileID, cmd.ItemID, seriesID, "home_dismissal", userStateEventState{})
		h.dispatchDropped(ctx, cmd.UserID, cmd.ProfileID, seriesID)
		return nil
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

	if err := h.undismissHomeItem(r.Context(), false, userID, profileID, surface, itemID); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UndismissHomeItem removes the dismissal, if any, and publishes the
// user-state event. Removing an absent dismissal succeeds. For an episode or a
// series it also undrops the series.
func (h *HomeDismissalHandler) UndismissHomeItem(ctx context.Context, userID int, profileID, surface, itemID string) error {
	return h.undismissHomeItem(ctx, true, userID, profileID, surface, itemID)
}

func (h *HomeDismissalHandler) undismissHomeItem(ctx context.Context, dropSeries bool, userID int, profileID, surface, itemID string) error {
	seriesID, drop, err := h.dropSeriesFor(ctx, dropSeries, itemID)
	if err != nil {
		return err
	}
	if drop {
		if err := h.seriesDrops.Undrop(ctx, userID, profileID, seriesID); err != nil {
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete dismissal")
		}
	}
	// Per-item dismissals written before series drops existed may remain.
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := store.DeleteHomeDismissal(ctx, profileID, surface, itemID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete dismissal")
	}
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, itemID, seriesID, "home_dismissal", userStateEventState{})
	if drop {
		h.dispatchDropped(ctx, userID, profileID, seriesID)
	}
	return nil
}

// dropSeriesFor returns the series a dismissal of itemID drops, if any.
// dropSeries is false on the frozen v1 routes, which keep per-card dismissals.
func (h *HomeDismissalHandler) dropSeriesFor(ctx context.Context, dropSeries bool, itemID string) (string, bool, error) {
	if !dropSeries || h.seriesDrops == nil {
		return "", false, nil
	}
	seriesID, ok, err := h.seriesDrops.ResolveDropSeries(ctx, itemID)
	if err != nil {
		return "", false, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve dismissed item")
	}
	return seriesID, ok, nil
}

func (h *HomeDismissalHandler) dispatchDropped(ctx context.Context, userID int, profileID, seriesID string) {
	if h.droppedDispatcher == nil {
		return
	}
	_ = h.droppedDispatcher.HandleLocalDroppedEvent(ctx, watchsync.LocalDroppedEvent{
		UserID:    userID,
		ProfileID: profileID,
		SeriesIDs: []string{seriesID},
	})
}

func validHomeSurface(surface string) bool {
	return surface == userstore.HomeSurfaceContinueWatching || surface == userstore.HomeSurfaceNextUp
}
