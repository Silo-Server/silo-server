package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
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
	if h.recsRepo == nil || h.Fetcher == nil {
		writeJSON(w, http.StatusOK, tasteSeedItemsResponse{Items: []sectionItemResponse{}})
		return
	}

	limit := parseTasteSeedLimit(r)
	offset := parseTasteSeedOffset(r)

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	// Fetch a page-sized window of candidate IDs ordered by engagement + rating.
	candidateIDs, err := h.recsRepo.GetTasteSeedCandidates(r.Context(), limit, offset)
	if err != nil {
		slog.Error("TasteSeedItems: candidate query failed", "user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch taste seed candidates")
		return
	}

	if len(candidateIDs) == 0 {
		writeJSON(w, http.StatusOK, tasteSeedItemsResponse{Items: []sectionItemResponse{}})
		return
	}

	// Hydrate items, applying the user's library/content-rating access filter.
	mediaItems, err := h.Fetcher.FetchItemsByContentIDs(r.Context(), candidateIDs, filter)
	if err != nil {
		slog.Error("TasteSeedItems: hydrate failed", "user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to hydrate taste seed items")
		return
	}

	// Preserve the candidate ordering returned by the repo, since
	// FetchItemsByContentIDs does not guarantee input order.
	itemMap := make(map[string]*models.MediaItem, len(mediaItems))
	for _, mi := range mediaItems {
		itemMap[mi.ContentID] = mi
	}

	stateMap := h.resolveTasteSeedUserStates(r.Context(), userID, profileID, mediaItems)

	items := make([]sectionItemResponse, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		mi, ok := itemMap[id]
		if !ok || mi == nil {
			continue
		}
		items = append(items, h.tasteSeedSectionItem(r.Context(), mi, stateMap))
	}

	resp := tasteSeedItemsResponse{Items: items}
	// Pagination is on the underlying SQL candidate stream (offset/limit on
	// GetTasteSeedCandidates), so we gate on candidate page fullness, not on
	// post-hydration visible count. Comparing on `items` would incorrectly
	// terminate pagination whenever access filtering trims the visible page —
	// even though more candidate rows exist. The trade-off is that pathologically
	// filtered tails may produce one extra empty fetch before next_offset goes
	// nil; the infinite-query consumer handles that gracefully.
	if len(candidateIDs) == limit {
		next := offset + limit
		resp.NextOffset = &next
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleTasteSeed handles POST /recommendations/taste-seed.
//
// Adds each of the provided content IDs to the active profile's favorites
// (idempotent — re-adds are safe). After bulk-add, asynchronously requests a
// taste profile refresh so recommendations re-rank using the new signals.
// Already-favorited items are silently skipped.
func (h *RecommendationsHandler) HandleTasteSeed(w http.ResponseWriter, r *http.Request) {
	if h.storeProvider == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "User store unavailable")
		return
	}

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

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		slog.Error("TasteSeed: failed to load user store", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load user store")
		return
	}

	added := 0
	for _, id := range req.ItemIDs {
		if id == "" {
			continue
		}
		if err := store.AddFavorite(r.Context(), profileID, id); err != nil {
			// Don't fail the whole request on a single item error — log and
			// continue so the user's other picks still seed their profile.
			slog.Warn("TasteSeed: failed to add favorite", "user_id", userID, "profile_id", profileID, "item_id", id, "error", err)
			continue
		}
		added++
	}

	// Trigger an async taste profile refresh so the next discover/for-you
	// fetch sees the new signals. This is fire-and-forget by design — the
	// worker handles staleness.
	if added > 0 {
		var staler ProfileStaler
		if h.recsRepo != nil {
			staler = h.recsRepo
		}
		triggerProfileRefresh(r.Context(), staler, h.RecWorker, userID, profileID)
	}

	writeJSON(w, http.StatusOK, tasteSeedSubmitResponse{Added: added})
}

// resolveTasteSeedUserStates returns the per-item user state map (favorite,
// watchlist, played) so the UI can pre-mark items the profile already
// favorited. Returns nil on any error — the UI falls back to "not favorited"
// in that case.
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
