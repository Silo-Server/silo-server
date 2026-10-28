package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// ratingsRepository defines the data access interface for user ratings.
type ratingsRepository interface {
	Set(ctx context.Context, userID int, profileID, mediaItemID string, rating int) error
	Get(ctx context.Context, userID int, profileID, mediaItemID string) (*catalog.UserRating, error)
	Delete(ctx context.Context, userID int, profileID, mediaItemID string) error
	List(ctx context.Context, userID int, profileID string, limit, offset int) ([]catalog.UserRating, error)
	ListPage(ctx context.Context, userID int, profileID string, after *catalog.RatingKey, limit int) ([]catalog.UserRating, error)
}

// LocalRatingEventDispatcher sends a profile's rating changes to its watch
// providers.
type LocalRatingEventDispatcher interface {
	HandleLocalRatingEvent(ctx context.Context, event watchsync.LocalRatingEvent) error
}

// RatingsHandler handles user rating operations.
type RatingsHandler struct {
	ratingsRepo      ratingsRepository
	itemRepo         personalDataItemRepository
	signalsNotifier  SignalsChangedNotifier
	ratingDispatcher LocalRatingEventDispatcher
}

// NewRatingsHandler creates a new RatingsHandler.
func NewRatingsHandler(ratingsRepo ratingsRepository, itemRepo personalDataItemRepository) *RatingsHandler {
	return &RatingsHandler{ratingsRepo: ratingsRepo, itemRepo: itemRepo}
}

// SetSignalsChangedNotifier configures where changes to a profile's
// recommendation signals are reported. Without it they are not reported.
func (h *RatingsHandler) SetSignalsChangedNotifier(notifier SignalsChangedNotifier) {
	h.signalsNotifier = notifier
}

// SetLocalRatingEventDispatcher configures where rating changes are sent for
// watch-provider sync.
func (h *RatingsHandler) SetLocalRatingEventDispatcher(dispatcher LocalRatingEventDispatcher) {
	h.ratingDispatcher = dispatcher
}

func (h *RatingsHandler) dispatchRatingChange(ctx context.Context, userID int, profileID, itemID string) {
	if h.ratingDispatcher == nil {
		return
	}
	_ = h.ratingDispatcher.HandleLocalRatingEvent(ctx, watchsync.LocalRatingEvent{
		UserID:       userID,
		ProfileID:    profileID,
		MediaItemIDs: []string{itemID},
	})
}

// --- Response types ---

type ratingResponse struct {
	Rating  int    `json:"rating"`
	RatedAt string `json:"rated_at"`
}

type ratingListItem struct {
	MediaItemID string `json:"media_item_id"`
	Rating      int    `json:"rating"`
	RatedAt     string `json:"rated_at"`
}

type ratingListResponse struct {
	Ratings []ratingListItem `json:"ratings"`
}

// --- Request types ---

type setRatingRequest struct {
	Rating int `json:"rating"`
}

// SetRating sets the profile's rating of an item the viewer may see. The
// caller validates the range; an item outside the viewer's access is 404.
// Setting the same rating twice converges, so a retried set is safe.
func (h *RatingsHandler) SetRating(ctx context.Context, userID int, profileID, itemID string, access catalog.AccessFilter, rating int) error {
	if err := h.itemRepo.EnsureAccessible(ctx, itemID, access); err != nil {
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	if err := h.ratingsRepo.Set(ctx, userID, profileID, itemID, rating); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to set rating")
	}
	notifySignalsChanged(ctx, h.signalsNotifier, userID, profileID)
	h.dispatchRatingChange(ctx, userID, profileID, itemID)
	return nil
}

// DeleteRating removes the profile's rating of an item. Deleting a rating
// that does not exist succeeds, so a retried delete converges.
func (h *RatingsHandler) DeleteRating(ctx context.Context, userID int, profileID, itemID string) error {
	if err := h.ratingsRepo.Delete(ctx, userID, profileID, itemID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete rating")
	}
	notifySignalsChanged(ctx, h.signalsNotifier, userID, profileID)
	h.dispatchRatingChange(ctx, userID, profileID, itemID)
	return nil
}

// GetRating answers the profile's rating of an item; found is false when
// the profile has not rated it. No access check runs, matching v1: a rating
// is the profile's own data.
func (h *RatingsHandler) GetRating(ctx context.Context, userID int, profileID, itemID string) (rating catalog.UserRating, found bool, err error) {
	r, err := h.ratingsRepo.Get(ctx, userID, profileID, itemID)
	if err != nil {
		return catalog.UserRating{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to get rating")
	}
	if r == nil {
		return catalog.UserRating{}, false, nil
	}
	return *r, true, nil
}

// ListRatingsPage is the keyset form of ListRatings the v2 listing uses: at
// most limit rows ordered by (rated_at DESC, media_item_id DESC) strictly
// after the key (nil = from the most recently rated row).
func (h *RatingsHandler) ListRatingsPage(ctx context.Context, userID int, profileID string, after *catalog.RatingKey, limit int) ([]catalog.UserRating, error) {
	ratings, err := h.ratingsRepo.ListPage(ctx, userID, profileID, after, limit)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list ratings")
	}
	return ratings, nil
}
