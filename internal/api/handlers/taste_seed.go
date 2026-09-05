package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	tasteSeedDefaultLimit = 30
	tasteSeedMaxLimit     = 60
	tasteSeedMaxPicks     = 200
)

type tasteSeedItemsResponse struct {
	Items      []sectionItemResponse `json:"items"`
	NextOffset *int                  `json:"next_offset,omitempty"`
}

type tasteSeedSubmitRequest struct {
	ItemIDs []string `json:"item_ids"`
}

type tasteSeedSubmitResponse struct {
	Added int `json:"added"`
}

// HandleTasteSeedItems handles GET /recommendations/taste-seed/items.
//
// Returns a paginated, hydrated list of posters used for the new-user
// taste-seeding picker. Blends server-watched popularity with rating reliability
// and recency so fresh servers (no watch history yet) still surface recognizable
// content. The user_state field carries the existing is_favorite flag, so the UI
// can pre-select items the profile already favorited.
func (h *RecommendationsHandler) HandleTasteSeedItems(w http.ResponseWriter, r *http.Request) {
	limit := parseTasteSeedLimit(r)
	offset := parseTasteSeedOffset(r)
	items, candidates, err := h.TasteSeedItems(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), requestAccessFilter(r), limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	resp := tasteSeedItemsResponse{Items: items}
	if candidates == limit {
		next := offset + limit
		resp.NextOffset = &next
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleTasteSeed handles POST /recommendations/taste-seed.
//
// Accepts a list of item IDs the user picked in the taste-seeding UI and
// records each as a favorite for the active profile, then queues a
// taste-profile refresh.
func (h *RecommendationsHandler) HandleTasteSeed(w http.ResponseWriter, r *http.Request) {
	var req tasteSeedSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.ItemIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one item_id is required")
		return
	}
	if len(req.ItemIDs) > tasteSeedMaxPicks {
		writeError(w, http.StatusBadRequest, "bad_request", "Too many items in a single request")
		return
	}

	added, err := h.SubmitTasteSeed(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), req.ItemIDs)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tasteSeedSubmitResponse{Added: added})
}

func (h *RecommendationsHandler) resolveTasteSeedUserStates(ctx context.Context, userID int, profileID string, mediaItems []*models.MediaItem) map[string]*itemUserStateResponse {
	if h.storeProvider == nil || len(mediaItems) == 0 {
		return nil
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return nil
	}
	states, err := resolveItemUserStatesWithOptions(ctx, store, profileID, h.EpisodeRepo, mediaItems, itemUserStateOptions{
		UserID:             userID,
		EbookProgressStore: h.EbookProgress,
	})
	if err != nil {
		return nil
	}
	return states
}

// tasteSeedSectionItem builds the trimmed section item used by the seed grid.
// We only populate poster fields — the picker doesn't need overlays, ratings,
// or progress.
func (h *RecommendationsHandler) tasteSeedSectionItem(ctx context.Context, mi *models.MediaItem, stateMap map[string]*itemUserStateResponse) sectionItemResponse {
	item := sectionItemResponse{
		ContentID:       mi.ContentID,
		Type:            mi.Type,
		Title:           mi.Title,
		Year:            mi.Year,
		Genres:          mi.Genres,
		Status:          mi.Status,
		PosterThumbhash: mi.PosterThumbhash,
	}
	if item.Genres == nil {
		item.Genres = []string{}
	}
	if item.Keywords == nil {
		item.Keywords = []string{}
	}
	if h.DetailSvc != nil {
		item.PosterURL = h.DetailSvc.PresignURL(ctx, cardThumbnailPath(mi.PosterPath), "card")
	}
	if stateMap != nil {
		item.UserState = stateMap[mi.ContentID]
	}
	return item
}

func parseTasteSeedLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			if parsed > tasteSeedMaxLimit {
				return tasteSeedMaxLimit
			}
			return parsed
		}
	}
	return tasteSeedDefaultLimit
}

func parseTasteSeedOffset(r *http.Request) int {
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}
