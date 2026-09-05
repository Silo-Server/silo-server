package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ratingsRepository defines the data access interface for user ratings.
type ratingsRepository interface {
	Set(ctx context.Context, userID int, profileID, mediaItemID string, rating int) error
	Get(ctx context.Context, userID int, profileID, mediaItemID string) (*catalog.UserRating, error)
	Delete(ctx context.Context, userID int, profileID, mediaItemID string) error
	List(ctx context.Context, userID int, profileID string, limit, offset int) ([]catalog.UserRating, error)
}

// RatingsHandler handles user rating operations.
type RatingsHandler struct {
	ratingsRepo             ratingsRepository
	itemRepo                personalDataItemRepository
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
}

// NewRatingsHandler creates a new RatingsHandler.
func NewRatingsHandler(ratingsRepo ratingsRepository, itemRepo personalDataItemRepository) *RatingsHandler {
	return &RatingsHandler{ratingsRepo: ratingsRepo, itemRepo: itemRepo}
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *RatingsHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *RatingsHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

func (h *RatingsHandler) markStale(ctx context.Context, userID int, profileID string) {
	triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
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

// HandleSetRating handles PUT /ratings/{item_id}.
// Accepts {"rating": N} where N is 1-5. Returns 204 on success.
func (h *RatingsHandler) HandleSetRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req setRatingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Rating < 1 || req.Rating > 5 {
		writeError(w, http.StatusBadRequest, "bad_request", "Rating must be between 1 and 5")
		return
	}

	if err := h.itemRepo.EnsureAccessible(r.Context(), itemID, requestAccessFilter(r)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	if err := h.ratingsRepo.Set(r.Context(), userID, profileID, itemID, req.Rating); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set rating")
		return
	}

	h.markStale(r.Context(), userID, profileID)
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteRating handles DELETE /ratings/{item_id}.
// Returns 204 on success.
func (h *RatingsHandler) HandleDeleteRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.DeleteRating(r.Context(), userID, profileID, itemID); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteRating removes the profile's rating of an item. Deleting a rating
// that does not exist succeeds, so a retried delete converges.
func (h *RatingsHandler) DeleteRating(ctx context.Context, userID int, profileID, itemID string) error {
	if err := h.ratingsRepo.Delete(ctx, userID, profileID, itemID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete rating")
	}
	h.markStale(ctx, userID, profileID)
	return nil
}

// HandleGetRating handles GET /ratings/{item_id}.
// Returns the rating or 404 if not found.
func (h *RatingsHandler) HandleGetRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	rating, err := h.ratingsRepo.Get(r.Context(), userID, profileID, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get rating")
		return
	}

	if rating == nil {
		writeError(w, http.StatusNotFound, "not_found", "Rating not found")
		return
	}

	writeJSON(w, http.StatusOK, ratingResponse{
		Rating:  rating.Rating,
		RatedAt: rating.RatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// ListRatings answers the store page [offset, offset+limit) of the profile's
// ratings, newest first.
func (h *RatingsHandler) ListRatings(ctx context.Context, userID int, profileID string, limit, offset int) ([]catalog.UserRating, error) {
	ratings, err := h.ratingsRepo.List(ctx, userID, profileID, limit, offset)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list ratings")
	}
	return ratings, nil
}

// HandleListRatings handles GET /ratings/.
// Returns paginated ratings for the current user+profile.
func (h *RatingsHandler) HandleListRatings(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	limit, offset := parsePagination(r)

	ratings, err := h.ListRatings(r.Context(), userID, profileID, limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	items := make([]ratingListItem, 0, len(ratings))
	for _, ur := range ratings {
		items = append(items, ratingListItem{
			MediaItemID: ur.MediaItemID,
			Rating:      ur.Rating,
			RatedAt:     ur.RatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, ratingListResponse{Ratings: items})
}
