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
	"github.com/Silo-Server/silo-server/internal/artworkkey"
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

type LocalListEventDispatcher interface {
	HandleLocalListEvent(ctx context.Context, event watchsync.LocalListEvent) error
}

// PersonalDataHandler handles favorites, watchlist, and history endpoints.
type PersonalDataHandler struct {
	storeProvider           userstore.UserStoreProvider
	itemRepo                personalDataItemRepository
	episodeRepo             *catalog.EpisodeRepository
	seasonRepo              *catalog.SeasonRepository
	detailSvc               *catalog.DetailService
	EventsHub               *evt.Hub
	localListDispatcher     LocalListEventDispatcher
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

func (h *PersonalDataHandler) SetLocalListEventDispatcher(dispatcher LocalListEventDispatcher) {
	h.localListDispatcher = dispatcher
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
	if !rejectInvalidImageSize(w, r) {
		return
	}
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

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items, HasMore: len(favorites) == limit})
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

	h.dispatchLocalListEvent(r.Context(), watchsync.ListKindFavorites, watchsync.ListChangeAdded, userID, profileID, itemID)
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

	h.dispatchLocalListEvent(r.Context(), watchsync.ListKindFavorites, watchsync.ListChangeRemoved, userID, profileID, itemID)
	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "favorite", userStateEventState{
		IsFavorite: boolPtr(false),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *PersonalDataHandler) dispatchLocalListEvent(ctx context.Context, list watchsync.ListKind, change watchsync.ListChange, userID int, profileID string, itemID string) {
	if h == nil || h.localListDispatcher == nil || h.itemRepo == nil {
		return
	}
	item, err := h.itemRepo.GetByID(ctx, itemID)
	if err != nil || item == nil {
		return
	}
	listItem := watchsync.LocalFavorite{
		MediaItemID: item.ContentID,
		Kind:        item.Type,
		Title:       item.Title,
		Year:        item.Year,
		IMDbID:      item.ImdbID,
		TMDBID:      item.TmdbID,
		TVDBID:      item.TvdbID,
		FavoritedAt: time.Now().UTC(),
	}
	_ = h.localListDispatcher.HandleLocalListEvent(ctx, watchsync.LocalListEvent{
		List:      list,
		Change:    change,
		UserID:    userID,
		ProfileID: profileID,
		Items:     []watchsync.LocalFavorite{listItem},
	})
}

// --- Watchlist ---

// HandleListWatchlist handles GET /watchlist.
func (h *PersonalDataHandler) HandleListWatchlist(w http.ResponseWriter, r *http.Request) {
	if !rejectInvalidImageSize(w, r) {
		return
	}
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
	// Capture has_more from the raw store page, before hidden-series filtering
	// shrinks it — a full page that filters down still has more to fetch.
	watchlistHasMore := len(entries) == limit
	entries, err = h.filterHiddenWatchlistSeries(r.Context(), store, profileID, entries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to filter watchlist")
		return
	}

	items, err := resolveItems(h, r, entries, func(e userstore.WatchlistEntry) string { return e.MediaItemID })
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve watchlist items")
		return
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items, HasMore: watchlistHasMore})
}

// filterHiddenWatchlistSeries drops fully-watched series from a watchlist page.
// The entries stay in the store (and on synced providers) so a newly added
// episode makes the series reappear; only the display surface hides them.
func (h *PersonalDataHandler) filterHiddenWatchlistSeries(ctx context.Context, store userstore.UserStore, profileID string, entries []userstore.WatchlistEntry) ([]userstore.WatchlistEntry, error) {
	if h.episodeRepo == nil || len(entries) == 0 {
		return entries, nil
	}
	entryIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.MediaItemID)
	}
	visibility := catalog.NewWatchlistVisibility(h.itemRepo, h.episodeRepo)
	hidden, err := visibility.HiddenSeriesIDs(ctx, store, profileID, entryIDs)
	if err != nil {
		return nil, err
	}
	if len(hidden) == 0 {
		return entries, nil
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if _, ok := hidden[entry.MediaItemID]; !ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
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

	h.dispatchLocalListEvent(r.Context(), watchsync.ListKindWatchlist, watchsync.ListChangeAdded, userID, profileID, itemID)
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

	h.dispatchLocalListEvent(r.Context(), watchsync.ListKindWatchlist, watchsync.ListChangeRemoved, userID, profileID, itemID)
	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, itemID, "", "watchlist", userStateEventState{
		InWatchlist: boolPtr(false),
	})
	w.WriteHeader(http.StatusNoContent)
}

// --- History ---

// HandleListHistory handles GET /history.
func (h *PersonalDataHandler) HandleListHistory(w http.ResponseWriter, r *http.Request) {
	if !rejectInvalidImageSize(w, r) {
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	limit, offset := parsePagination(r)

	entries, err := h.HistoryEntries(r.Context(), userID, profileID, limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	cards, err := h.HistoryCards(r.Context(), sectionViewerFromRequest(r), entries)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	items := make([]itemListResponse, 0, len(cards))
	for _, card := range cards {
		items = append(items, card.Item)
	}

	writeJSON(w, http.StatusOK, itemsListResponse{Items: items, HasMore: len(entries) == limit})
}

// History seams: the v1 handlers and the v2 history operations share them.
// Each returns an *APIError on failure.

// HistoryRemovalTarget is one thing to hide from history: an item, or a
// season/episode widened to its show.
type HistoryRemovalTarget = historyRemovalTargetRequest

// HistoryCardView is one history card: the catalog card rendered for the
// display item (an episode collapses to its series) and the most recent
// watch record behind it.
type HistoryCardView struct {
	Item  CollectionItemView
	Entry userstore.WatchHistoryEntry
}

// HistoryEntries pages the profile's visible history rows, most recent
// watch first.
func (h *PersonalDataHandler) HistoryEntries(ctx context.Context, userID int, profileID string, limit, offset int) ([]userstore.WatchHistoryEntry, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entries, err := store.ListHistory(ctx, profileID, limit, offset)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list history")
	}
	return entries, nil
}

// HistoryPage is the keyset form of HistoryEntries: at most limit visible
// history rows strictly after the key in (watched_at DESC, id DESC) order,
// nil starting at the most recent watch. The v2 listHistory operation pages
// with it so a watch recorded or hidden mid-pagination never repeats or
// skips a row.
func (h *PersonalDataHandler) HistoryPage(ctx context.Context, userID int, profileID string, after *userstore.HistoryKey, limit int) ([]userstore.WatchHistoryEntry, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entries, err := store.ListHistoryPage(ctx, profileID, after, limit)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list history")
	}
	return entries, nil
}

// HistoryCards renders the cards of a history page in entry order, one per
// display item, omitting items the viewer cannot see or that left the
// catalog.
func (h *PersonalDataHandler) HistoryCards(ctx context.Context, viewer SectionViewer, entries []userstore.WatchHistoryEntry) ([]HistoryCardView, error) {
	display, err := catalog.ResolveHistoryDisplayEntries(ctx, entries, h.episodeRepo)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve history items")
	}
	ids := make([]string, 0, len(display))
	for _, d := range display {
		ids = append(ids, d.DisplayID)
	}
	items, err := h.resolveItemCards(ctx, viewer, ids)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve history items")
	}
	byID := make(map[string]itemListResponse, len(items))
	for _, item := range items {
		byID[item.ContentID] = item
	}
	cards := make([]HistoryCardView, 0, len(items))
	for _, d := range display {
		item, ok := byID[d.DisplayID]
		if !ok {
			continue
		}
		cards = append(cards, HistoryCardView{Item: item, Entry: d.Entry})
	}
	return cards, nil
}

// RemoveHistory hides every watch of the targets from the profile's history
// and notifies the profile's sessions. Removing an already hidden item is a
// no-op, so a replay converges.
func (h *PersonalDataHandler) RemoveHistory(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter, targets []HistoryRemovalTarget) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	mediaItemSet := make(map[string]struct{})
	mediaItemIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		resolvedIDs, resolveErr := h.resolveHistoryRemovalMediaItemIDs(ctx, target, filter)
		if resolveErr != nil {
			if isNotFound(resolveErr) {
				return apiError(http.StatusNotFound, "not_found", "History target not found")
			}
			return fieldError("targets", resolveErr.Error())
		}
		for _, mediaItemID := range resolvedIDs {
			if _, ok := mediaItemSet[mediaItemID]; ok {
				continue
			}
			mediaItemSet[mediaItemID] = struct{}{}
			mediaItemIDs = append(mediaItemIDs, mediaItemID)
		}
	}

	if err := store.RemoveHistoryItems(ctx, profileID, mediaItemIDs, time.Now().UTC()); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove history")
	}

	triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
	for _, mediaItemID := range mediaItemIDs {
		publishUserStateEvent(
			ctx,
			h.EventsHub,
			userID,
			profileID,
			mediaItemID,
			"",
			"history",
			userStateEventState{},
		)
	}
	return nil
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

	if err := h.RemoveHistory(r.Context(), userID, profileID, requestAccessFilter(r), req.Targets); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

// itemsListResponse wraps a list of items for favorites/watchlist responses.
type itemsListResponse struct {
	Items []itemListResponse `json:"items"`
	// HasMore lets clients paginate these personal lists. Computed from the
	// raw store page size (== limit), not the resolved item count, so
	// catalog-missing / hidden entries don't make a full page look final.
	HasMore bool `json:"has_more"`
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
	return h.resolveItemCards(r.Context(), sectionViewerFromRequest(r), ids)
}

// resolveItemCards is resolveItemsByIDs off the request: the v1 handlers
// and the v2 history operation share it, so a card is rendered one way.
func (h *PersonalDataHandler) resolveItemCards(ctx context.Context, viewer SectionViewer, ids []string) ([]itemListResponse, error) {
	if len(ids) == 0 || h.itemRepo == nil {
		return []itemListResponse{}, nil
	}
	mediaItems, err := h.itemRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	// Index by content ID for order-preserving lookup.
	byID := make(map[string]*itemListResponse, len(mediaItems))
	filter := viewer.Access
	// Parsed once for the whole list; the calling handler has already rejected
	// an unrecognized value. Unset keeps the per-slot defaults below, which are
	// deliberately asymmetric (a featured poster beside a card backdrop); an
	// explicit size applies to every image in the response instead.
	size := viewer.ImageSize
	posterHint := requestVariantHint("featured", size)
	cardHint := requestVariantHint("card", size)
	accessibleItems := make([]*models.MediaItem, 0, len(mediaItems))
	for _, mi := range mediaItems {
		if err := h.itemRepo.EnsureAccessible(ctx, mi.ContentID, filter); err != nil {
			if errors.Is(err, catalog.ErrItemNotFound) {
				continue
			}
			return nil, err
		}
		accessibleItems = append(accessibleItems, mi)
	}

	userStates := map[string]*itemUserStateResponse{}
	if store, profileID, ok := h.userStoreForContext(ctx); ok {
		if resolvedStates, err := resolveItemUserStatesWithOptions(ctx, store, profileID, h.episodeRepo, accessibleItems, itemUserStateOptions{
			UserID:             apimw.GetUserID(ctx),
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
		resp.PosterURL = h.presignURLCtx(ctx, sizedPosterPath(mi.PosterPath, size), posterHint)
		resp.BackdropURL = h.presignURLCtx(ctx, sizedCardBackdropPath(mi.BackdropPath, size), cardHint)
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
			episodes, epErr := h.episodeRepo.GetByIDs(ctx, unresolvedIDs)
			if epErr == nil && len(episodes) > 0 {
				// Gather parent series for poster/metadata fallback.
				seriesIDs := make([]string, 0, len(episodes))
				for _, ep := range episodes {
					seriesIDs = append(seriesIDs, ep.SeriesID)
				}
				parentItems, _ := h.itemRepo.GetByIDs(ctx, seriesIDs)
				parentByID := make(map[string]*models.MediaItem, len(parentItems))
				for _, mi := range parentItems {
					parentByID[mi.ContentID] = mi
				}

				for _, ep := range episodes {
					// Verify the parent series is accessible.
					if err := h.itemRepo.EnsureAccessible(ctx, ep.SeriesID, filter); err != nil {
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
						resp.BackdropURL = h.presignURLCtx(ctx, sizedCardPath(ep.StillPath, artworkkey.ImageStill, size), cardHint)
						resp.BackdropThumbhash = ep.StillThumbhash
					} else if parent != nil {
						resp.BackdropURL = h.presignURLCtx(ctx, sizedCardBackdropPath(parent.BackdropPath, size), cardHint)
						resp.BackdropThumbhash = parent.BackdropThumbhash
					}
					if parent != nil {
						resp.PosterURL = h.presignURLCtx(ctx, sizedPosterPath(parent.PosterPath, size), posterHint)
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
			case "movie", "ebook":
				// Hiding an ebook gates ebook_reader_progress reads via
				// user_history_hidden_items (updated_at <= hidden_before)
				// without deleting the progress row, so the reader position
				// survives: hidden is not the same as unread.
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
	return h.userStoreForContext(r.Context())
}

func (h *PersonalDataHandler) userStoreForContext(ctx context.Context) (userstore.UserStore, string, bool) {
	if h.storeProvider == nil {
		return nil, "", false
	}
	userID := apimw.GetUserID(ctx)
	profileID := apimw.GetProfileID(ctx)
	if userID == 0 || profileID == "" {
		return nil, "", false
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
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
	return h.presignURLCtx(r.Context(), path, variant)
}

func (h *PersonalDataHandler) presignURLCtx(ctx context.Context, path string, variant string) string {
	if h.detailSvc != nil {
		return h.detailSvc.PresignURL(ctx, path, variant)
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
