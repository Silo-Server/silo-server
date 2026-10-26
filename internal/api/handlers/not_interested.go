package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Not interested is a profile's mark on a movie or series it does not want
// recommended. The mark keeps the title out of the profile's recommendation
// and discovery rows and ranks similar titles lower there; library browsing,
// search, item detail, playback and the profile's own lists are unchanged.
// Only the v2 API serves it.

// ListNotInterestedPage answers at most limit of the profile's marks ordered
// by (added_at DESC, media_item_id DESC) strictly after the key (nil = from
// the newest), and the cards of the marked titles the viewer may see, in the
// same order.
func (h *PersonalDataHandler) ListNotInterestedPage(ctx context.Context, viewer PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, []CollectionItemView, error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entries, err := store.ListNotInterestedPage(ctx, viewer.ProfileID, after, limit)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list Not interested titles")
	}
	items, err := resolveItems(h, ctx, viewer, entries, func(e userstore.NotInterestedEntry) string { return e.MediaItemID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve Not interested titles")
	}
	return entries, items, nil
}

// GetNotInterested answers the profile's mark on an item the viewer may see.
// found is false when the item is not marked; an item the viewer may not see
// is a 404 error.
func (h *PersonalDataHandler) GetNotInterested(ctx context.Context, viewer PersonalListViewer, itemID string) (entry userstore.NotInterestedEntry, found bool, err error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return userstore.NotInterestedEntry{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return userstore.NotInterestedEntry{}, false, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	mark, err := store.GetNotInterested(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return userstore.NotInterestedEntry{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check Not interested")
	}
	if mark == nil {
		return userstore.NotInterestedEntry{}, false, nil
	}
	return *mark, true, nil
}

// MarkNotInterested marks a movie or series the viewer may see. Episodes,
// seasons and other kinds are refused: recommendations offer movies and
// series, and a series mark covers its episodes. Marking a marked title
// succeeds and notifies nobody, so a retry repeats no side effect.
func (h *PersonalDataHandler) MarkNotInterested(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	if h.itemRepo != nil {
		item, err := h.itemRepo.GetByID(ctx, itemID)
		if err != nil || item == nil {
			return apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		if item.Type != itemTypeMovie && item.Type != itemTypeSeries {
			return apiError(http.StatusUnprocessableEntity, "validation_failed", "Only a movie or series can be marked Not interested")
		}
	}
	marked, err := store.MarkNotInterested(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to mark Not interested")
	}
	if marked {
		h.notifyNotInterestedChange(ctx, viewer, itemID, true)
	}
	return nil
}

// UnmarkNotInterested removes the profile's mark. Removing an absent mark
// succeeds and notifies nobody. No access check runs: the item may have left
// the viewer's scope since it was marked, and the profile must still be able
// to clear it.
func (h *PersonalDataHandler) UnmarkNotInterested(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	unmarked, err := store.UnmarkNotInterested(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear Not interested")
	}
	if unmarked {
		h.notifyNotInterestedChange(ctx, viewer, itemID, false)
	}
	return nil
}

// notifyNotInterestedChange tells the profile's recommendations, which
// rebuild its rows, and its clients about a committed mark or unmark. Like
// notifyListChange it outlives the request.
func (h *PersonalDataHandler) notifyNotInterestedChange(ctx context.Context, viewer PersonalListViewer, itemID string, marked bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listChangeNoticeTimeout)
	defer cancel()
	notifySignalsChanged(ctx, h.signalsNotifier, viewer.UserID, viewer.ProfileID)
	publishUserStateEvent(ctx, h.EventsHub, viewer.UserID, viewer.ProfileID, itemID, "", "not_interested", userStateEventState{NotInterested: &marked})
}
