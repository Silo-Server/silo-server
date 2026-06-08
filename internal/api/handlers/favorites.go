package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type personalDataItemRepository interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

type LocalFavoriteEventDispatcher interface {
	HandleLocalFavoriteEvent(ctx context.Context, event watchsync.LocalFavoriteEvent) error
}

// PersonalDataHandler handles favorites, watchlist, and history endpoints.
type PersonalDataHandler struct {
	storeProvider           userstore.UserStoreProvider
	itemRepo                personalDataItemRepository
	episodeRepo             *catalog.EpisodeRepository
	seasonRepo              *catalog.SeasonRepository
	detailSvc               *catalog.DetailService
	EventsHub               *evt.Hub
	localFavoriteDispatcher LocalFavoriteEventDispatcher
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
	ebookProgressStore      EbookReaderProgressLister
}

// NewPersonalDataHandler creates a new PersonalDataHandler.
func NewPersonalDataHandler(provider userstore.UserStoreProvider, itemRepo personalDataItemRepository) *PersonalDataHandler {
	return &PersonalDataHandler{
		storeProvider: provider,
		itemRepo:      itemRepo,
	}
}

// SetDetailService configures the detail service for image URL resolution.
func (h *PersonalDataHandler) SetDetailService(svc *catalog.DetailService) {
	h.detailSvc = svc
}

func (h *PersonalDataHandler) SetEbookReaderProgressStore(store EbookReaderProgressLister) {
	h.ebookProgressStore = store
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *PersonalDataHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *PersonalDataHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

func (h *PersonalDataHandler) SetEpisodeRepo(repo *catalog.EpisodeRepository) {
	h.episodeRepo = repo
}

func (h *PersonalDataHandler) SetSeasonRepo(repo *catalog.SeasonRepository) {
	h.seasonRepo = repo
}

func (h *PersonalDataHandler) SetLocalFavoriteEventDispatcher(dispatcher LocalFavoriteEventDispatcher) {
	h.localFavoriteDispatcher = dispatcher
}

// --- Response types ---

type favoriteResponse struct {
	MediaItemID string `json:"media_item_id"`
	AddedAt     string `json:"added_at"`
}

type favoriteListResponse struct {
	Favorites []favoriteResponse `json:"favorites"`
}

type watchlistEntryResponse struct {
	MediaItemID string `json:"media_item_id"`
	AddedAt     string `json:"added_at"`
}

type watchlistListResponse struct {
	Watchlist []watchlistEntryResponse `json:"watchlist"`
}

type historyEntryResponse struct {
	ID              string  `json:"id"`
	MediaItemID     string  `json:"media_item_id"`
	WatchedAt       string  `json:"watched_at"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
}

type historyListResponse struct {
	History []historyEntryResponse `json:"history"`
}

type historyRemovalTargetRequest struct {
	ContentID string `json:"content_id"`
	Scope     string `json:"scope"`
}

type removeHistoryRequest struct {
	Targets []historyRemovalTargetRequest `json:"targets"`
}

const (
	historyRemovalScopeItem = "item"
	historyRemovalScopeShow = "show"
)

// --- Favorites ---

// HandleListFavorites handles GET /favorites.
func (h *PersonalDataHandler) HandleListFavorites(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	limit, offset := parsePagination(r)

	favorites, err := store.ListFavorites(r.Context(), profileID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list favorites")
		return
	}

	items, err := resolveItems(h, r, favorites, func(f userstore.Favorite) string { return f.MediaItemID })
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve favorite items")
		return
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items})
}

// HandleCheckFavorite handles GET /favorites/{item_id}.
// Returns 204 if the item is a favorite, 404 if not.
func (h *PersonalDataHandler) HandleCheckFavorite(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := h.ensureAccessibleItem(r, itemID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	ok, err := store.IsFavorite(r.Context(), profileID, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check favorite")
		return
	}

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAddFavorite handles PUT /favorites/{item_id}.
func (h *PersonalDataHandler) HandleAddFavorite(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := h.ensureAccessibleItem(r, itemID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	if err := store.AddFavorite(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to add favorite")
		return
	}

	h.dispatchLocalFavoriteEvent(r.Context(), watchsync.LocalFavoriteEventAdded, userID, profileID, itemID)
	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "favorite", userStateEventState{
		IsFavorite: boolPtr(true),
	})
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveFavorite handles DELETE /favorites/{item_id}.
func (h *PersonalDataHandler) HandleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.RemoveFavorite(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove favorite")
		return
	}

	h.dispatchLocalFavoriteEvent(r.Context(), watchsync.LocalFavoriteEventRemoved, userID, profileID, itemID)
	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "favorite", userStateEventState{
		IsFavorite: boolPtr(false),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *PersonalDataHandler) dispatchLocalFavoriteEvent(ctx context.Context, kind watchsync.LocalFavoriteEventKind, userID int, profileID string, itemID string) {
	if h == nil || h.localFavoriteDispatcher == nil || h.itemRepo == nil {
		return
	}
	item, err := h.itemRepo.GetByID(ctx, itemID)
	if err != nil || item == nil {
		return
	}
	favorite := watchsync.LocalFavorite{
		MediaItemID: item.ContentID,
		Kind:        item.Type,
		Title:       item.Title,
		Year:        item.Year,
		IMDbID:      item.ImdbID,
		TMDBID:      item.TmdbID,
		TVDBID:      item.TvdbID,
		FavoritedAt: time.Now().UTC(),
	}
	_ = h.localFavoriteDispatcher.HandleLocalFavoriteEvent(ctx, watchsync.LocalFavoriteEvent{
		Kind:      kind,
		UserID:    userID,
		ProfileID: profileID,
		Favorites: []watchsync.LocalFavorite{favorite},
	})
}

// --- Watchlist ---

// HandleListWatchlist handles GET /watchlist.
func (h *PersonalDataHandler) HandleListWatchlist(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	limit, offset := parsePagination(r)

	entries, err := store.ListWatchlist(r.Context(), profileID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list watchlist")
		return
	}

	items, err := resolveItems(h, r, entries, func(e userstore.WatchlistEntry) string { return e.MediaItemID })
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve watchlist items")
		return
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items})
}

// HandleCheckWatchlist handles GET /watchlist/{item_id}.
// Returns 204 if the item is on the watchlist, 404 if not.
func (h *PersonalDataHandler) HandleCheckWatchlist(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := h.ensureAccessibleItem(r, itemID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	ok, err := store.InWatchlist(r.Context(), profileID, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check watchlist")
		return
	}

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleAddToWatchlist handles PUT /watchlist/{item_id}.
func (h *PersonalDataHandler) HandleAddToWatchlist(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := h.ensureAccessibleItem(r, itemID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	if err := store.AddToWatchlist(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to add to watchlist")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "watchlist", userStateEventState{
		InWatchlist: boolPtr(true),
	})
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveFromWatchlist handles DELETE /watchlist/{item_id}.
func (h *PersonalDataHandler) HandleRemoveFromWatchlist(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.RemoveFromWatchlist(r.Context(), profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove from watchlist")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "watchlist", userStateEventState{
		InWatchlist: boolPtr(false),
	})
	w.WriteHeader(http.StatusNoContent)
}

// --- History ---

// HandleListHistory handles GET /history.
func (h *PersonalDataHandler) HandleListHistory(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	limit, offset := parsePagination(r)

	entries, err := store.ListHistory(r.Context(), profileID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list history")
		return
	}

	ids, err := catalog.ResolveHistoryDisplayIDs(r.Context(), entries, h.episodeRepo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve history items")
		return
	}

	items, err := resolveItemsByIDs(h, r, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve history items")
		return
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items})
}

// HandleRemoveHistory handles POST /history/remove.
func (h *PersonalDataHandler) HandleRemoveHistory(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req removeHistoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.Targets) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one history target is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	filter := requestAccessFilter(r)
	mediaItemSet := make(map[string]struct{})
	mediaItemIDs := make([]string, 0, len(req.Targets))
	for _, target := range req.Targets {
		resolvedIDs, resolveErr := h.resolveHistoryRemovalMediaItemIDs(r.Context(), target, filter)
		if resolveErr != nil {
			switch {
			case isNotFound(resolveErr):
				writeError(w, http.StatusNotFound, "not_found", "History target not found")
			default:
				writeError(w, http.StatusBadRequest, "bad_request", resolveErr.Error())
			}
			return
		}
		for _, mediaItemID := range resolvedIDs {
			if _, ok := mediaItemSet[mediaItemID]; ok {
				continue
			}
			mediaItemSet[mediaItemID] = struct{}{}
			mediaItemIDs = append(mediaItemIDs, mediaItemID)
		}
	}

	if err := store.RemoveHistoryItems(r.Context(), profileID, mediaItemIDs, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove history")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	for _, mediaItemID := range mediaItemIDs {
		publishUserStateEvent(
			r.Context(),
			h.EventsHub,
			userID,
			profileID,
			mediaItemID,
			"",
			"history",
			userStateEventState{},
		)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

// itemsListResponse wraps a list of items for favorites/watchlist responses.
type itemsListResponse struct {
	Items []itemListResponse `json:"items"`
}

// resolveItems fetches full media item data for a list of entries.
// It preserves the order of the input slice and silently omits items not found in the catalog.
func resolveItems[T any](h *PersonalDataHandler, r *http.Request, entries []T, getID func(T) string) ([]itemListResponse, error) {
	if len(entries) == 0 || h.itemRepo == nil {
		return []itemListResponse{}, nil
	}

	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = getID(e)
	}
	return resolveItemsByIDs(h, r, ids)
}

func resolveItemsByIDs(h *PersonalDataHandler, r *http.Request, ids []string) ([]itemListResponse, error) {
	if len(ids) == 0 || h.itemRepo == nil {
		return []itemListResponse{}, nil
	}
	mediaItems, err := h.itemRepo.GetByIDs(r.Context(), ids)
	if err != nil {
		return nil, err
	}

	// Index by content ID for order-preserving lookup.
	byID := make(map[string]*itemListResponse, len(mediaItems))
	filter := requestAccessFilter(r)
	accessibleItems := make([]*models.MediaItem, 0, len(mediaItems))
	for _, mi := range mediaItems {
		if err := h.itemRepo.EnsureAccessible(r.Context(), mi.ContentID, filter); err != nil {
			if errors.Is(err, catalog.ErrItemNotFound) {
				continue
			}
			return nil, err
		}
		accessibleItems = append(accessibleItems, mi)
	}

	userStates := map[string]*itemUserStateResponse{}
	if store, profileID, ok := h.userStoreForRequest(r); ok {
		if resolvedStates, err := resolveItemUserStatesWithOptions(r.Context(), store, profileID, h.episodeRepo, accessibleItems, itemUserStateOptions{
			UserID:             apimw.GetUserID(r.Context()),
			EbookProgressStore: h.ebookProgressStore,
		}); err == nil {
			userStates = resolvedStates
		}
	}

	for _, mi := range accessibleItems {
		resp := itemListResponse{
			ContentID:         mi.ContentID,
			Type:              mi.Type,
			Title:             mi.Title,
			Year:              mi.Year,
			Genres:            mi.Genres,
			ContentRating:     mi.ContentRating,
			RatingIMDB:        mi.RatingIMDB,
			Overview:          mi.Overview,
			PosterThumbhash:   mi.PosterThumbhash,
			BackdropThumbhash: mi.BackdropThumbhash,
			UserState:         userStates[mi.ContentID],
		}
		resp.PosterURL = h.presignURL(r, featuredPosterPath(mi.PosterPath), "featured")
		resp.BackdropURL = h.presignURL(r, cardThumbnailPath(mi.BackdropPath), "card")
		byID[mi.ContentID] = &resp
	}

	// Resolve any remaining IDs as episodes.
	if h.episodeRepo != nil {
		var unresolvedIDs []string
		for _, id := range ids {
			if _, ok := byID[id]; !ok {
				unresolvedIDs = append(unresolvedIDs, id)
			}
		}
		if len(unresolvedIDs) > 0 {
			episodes, epErr := h.episodeRepo.GetByIDs(r.Context(), unresolvedIDs)
			if epErr == nil && len(episodes) > 0 {
				// Gather parent series for poster/metadata fallback.
				seriesIDs := make([]string, 0, len(episodes))
				for _, ep := range episodes {
					seriesIDs = append(seriesIDs, ep.SeriesID)
				}
				parentItems, _ := h.itemRepo.GetByIDs(r.Context(), seriesIDs)
				parentByID := make(map[string]*models.MediaItem, len(parentItems))
				for _, mi := range parentItems {
					parentByID[mi.ContentID] = mi
				}

				for _, ep := range episodes {
					// Verify the parent series is accessible.
					if err := h.itemRepo.EnsureAccessible(r.Context(), ep.SeriesID, filter); err != nil {
						continue
					}
					parent := parentByID[ep.SeriesID]
					resp := itemListResponse{
						ContentID:  ep.ContentID,
						Type:       "episode",
						Title:      ep.Title,
						RatingIMDB: ep.RatingIMDB,
						Overview:   ep.Overview,
					}
					// Use episode still as backdrop, fall back to parent series images.
					if ep.StillPath != "" {
						resp.BackdropURL = h.presignURL(r, cardThumbnailPath(ep.StillPath), "card")
						resp.BackdropThumbhash = ep.StillThumbhash
					} else if parent != nil {
						resp.BackdropURL = h.presignURL(r, cardThumbnailPath(parent.BackdropPath), "card")
						resp.BackdropThumbhash = parent.BackdropThumbhash
					}
					if parent != nil {
						resp.PosterURL = h.presignURL(r, featuredPosterPath(parent.PosterPath), "featured")
						resp.PosterThumbhash = parent.PosterThumbhash
						resp.Year = parent.Year
						resp.Genres = parent.Genres
						resp.ContentRating = parent.ContentRating
					}
					byID[ep.ContentID] = &resp
				}
			}
		}
	}

	// Preserve input order.
	result := make([]itemListResponse, 0, len(ids))
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			result = append(result, *item)
		}
	}

	return result, nil
}

func (h *PersonalDataHandler) resolveHistoryRemovalMediaItemIDs(
	ctx context.Context,
	target historyRemovalTargetRequest,
	filter catalog.AccessFilter,
) ([]string, error) {
	contentID := strings.TrimSpace(target.ContentID)
	scope := strings.TrimSpace(target.Scope)
	if contentID == "" {
		return nil, errors.New("content_id is required")
	}
	switch scope {
	case "", historyRemovalScopeItem:
		scope = historyRemovalScopeItem
	case historyRemovalScopeShow:
	default:
		return nil, errors.New("scope must be \"item\" or \"show\"")
	}

	if h.itemRepo != nil {
		item, err := h.itemRepo.GetByID(ctx, contentID)
		switch {
		case err == nil && item != nil:
			if err := h.itemRepo.EnsureAccessible(ctx, item.ContentID, filter); err != nil {
				return nil, err
			}
			switch item.Type {
			case "movie":
				return []string{item.ContentID}, nil
			case "series":
				return h.seriesEpisodeIDs(ctx, item.ContentID)
			}
		case err != nil && !errors.Is(err, catalog.ErrItemNotFound):
			return nil, err
		}
	}

	if h.seasonRepo != nil {
		season, err := h.seasonRepo.GetByID(ctx, contentID)
		switch {
		case err == nil && season != nil:
			if err := h.itemRepo.EnsureAccessible(ctx, season.SeriesID, filter); err != nil {
				return nil, err
			}
			if scope == historyRemovalScopeShow {
				return h.seriesEpisodeIDs(ctx, season.SeriesID)
			}
			return h.seasonEpisodeIDs(ctx, season.ContentID)
		case err != nil && !errors.Is(err, catalog.ErrSeasonNotFound):
			return nil, err
		}
	}

	if h.episodeRepo == nil {
		return nil, catalog.ErrItemNotFound
	}

	episode, err := h.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		return nil, err
	}
	if err := h.itemRepo.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
		return nil, err
	}
	if scope == historyRemovalScopeShow {
		return h.seriesEpisodeIDs(ctx, episode.SeriesID)
	}
	return []string{episode.ContentID}, nil
}

func (h *PersonalDataHandler) seriesEpisodeIDs(ctx context.Context, seriesID string) ([]string, error) {
	if h.episodeRepo == nil {
		return nil, catalog.ErrItemNotFound
	}
	episodes, err := h.episodeRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	return historyEpisodeIDs(episodes), nil
}

func (h *PersonalDataHandler) seasonEpisodeIDs(ctx context.Context, seasonID string) ([]string, error) {
	if h.episodeRepo == nil {
		return nil, catalog.ErrItemNotFound
	}
	episodes, err := h.episodeRepo.ListBySeasonID(ctx, seasonID)
	if err != nil {
		return nil, err
	}
	return historyEpisodeIDs(episodes), nil
}

func historyEpisodeIDs(episodes []*models.Episode) []string {
	ids := make([]string, 0, len(episodes))
	for _, episode := range episodes {
		if episode == nil || strings.TrimSpace(episode.ContentID) == "" {
			continue
		}
		ids = append(ids, episode.ContentID)
	}
	return ids
}

func (h *PersonalDataHandler) userStoreForRequest(r *http.Request) (userstore.UserStore, string, bool) {
	if h.storeProvider == nil {
		return nil, "", false
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		return nil, "", false
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		return nil, "", false
	}
	return store, profileID, true
}

func (h *PersonalDataHandler) ensureAccessibleItem(r *http.Request, itemID string) error {
	if h.itemRepo == nil {
		return nil
	}
	err := h.itemRepo.EnsureAccessible(r.Context(), itemID, requestAccessFilter(r))
	if err == nil {
		return nil
	}
	// If not found in media_items, check if it's an episode and verify its parent series is accessible.
	if errors.Is(err, catalog.ErrItemNotFound) && h.episodeRepo != nil {
		ep, epErr := h.episodeRepo.GetByID(r.Context(), itemID)
		if epErr == nil {
			return h.itemRepo.EnsureAccessible(r.Context(), ep.SeriesID, requestAccessFilter(r))
		}
	}
	return err
}

// presignURL resolves an image path to a usable URL, delegating to the
// DetailService which handles plugin-prefixed paths, HTTP pass-through,
// and legacy S3 presigning.
func (h *PersonalDataHandler) presignURL(r *http.Request, path string, variant string) string {
	if h.detailSvc != nil {
		return h.detailSvc.PresignURL(r.Context(), path, variant)
	}
	return ""
}

// parsePagination extracts limit and offset from query parameters with defaults.
func parsePagination(r *http.Request) (int, int) {
	q := r.URL.Query()

	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	return limit, offset
}
