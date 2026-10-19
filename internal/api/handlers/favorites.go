package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchlist"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type personalDataItemRepository interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
	EnsureAccessibleIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]bool, error)
}

type LocalListEventDispatcher interface {
	HandleLocalListEvent(ctx context.Context, event watchsync.LocalListEvent) error
}

// PersonalDataHandler handles favorites, watchlist, and history endpoints.
type PersonalDataHandler struct {
	storeProvider       userstore.UserStoreProvider
	itemRepo            personalDataItemRepository
	episodeRepo         *catalog.EpisodeRepository
	seasonRepo          *catalog.SeasonRepository
	detailSvc           *catalog.DetailService
	EventsHub           *evt.Hub
	localListDispatcher LocalListEventDispatcher
	signalsNotifier     SignalsChangedNotifier
	ebookProgressStore  EbookReaderProgressLister
	watchlistTitles     *watchlist.Titles
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

// SetSignalsChangedNotifier configures where changes to a profile's
// recommendation signals are reported. Without it they are not reported.
func (h *PersonalDataHandler) SetSignalsChangedNotifier(notifier SignalsChangedNotifier) {
	h.signalsNotifier = notifier
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
	limit, offset := parsePagination(r)
	favorites, items, err := h.ListFavorites(r.Context(), personalListViewerFromRequest(r), limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsListResponse{Items: items, HasMore: len(favorites) == limit})
}

// PersonalListViewer is the identity a personal-list seam acts as: the
// account and profile the list belongs to, the viewer access filter that
// hides items the profile may not see, and the artwork size of the cards.
type PersonalListViewer struct {
	UserID    int
	ProfileID string
	Access    catalog.AccessFilter
	ImageSize imagesize.Size
	// ImageWidths are per-slot target widths overriding ImageSize; only the
	// native v2 API carries them.
	ImageWidths imagesize.Widths
}

// Artwork is the viewer's artwork preference.
func (v PersonalListViewer) Artwork() imagesize.Request {
	return imagesize.Request{Size: v.ImageSize, Widths: v.ImageWidths}
}

func personalListViewerFromRequest(r *http.Request) PersonalListViewer {
	return PersonalListViewer{
		UserID:    apimw.GetUserID(r.Context()),
		ProfileID: apimw.GetProfileID(r.Context()),
		Access:    requestAccessFilter(r),
		ImageSize: requestImageSize(r),
	}
}

// ListFavorites answers the store page [offset, offset+limit) of the
// profile's favorites, newest first, and the cards of those entries in the
// same order. Entries the catalog no longer has or the viewer may not see
// have no card, so the raw entries, not the cards, decide whether another
// page follows.
func (h *PersonalDataHandler) ListFavorites(ctx context.Context, viewer PersonalListViewer, limit, offset int) ([]userstore.Favorite, []CollectionItemView, error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	favorites, err := store.ListFavorites(ctx, viewer.ProfileID, limit, offset)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list favorites")
	}
	items, err := resolveItems(h, ctx, viewer, favorites, func(f userstore.Favorite) string { return f.MediaItemID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve favorite items")
	}
	return favorites, items, nil
}

// ListFavoritesPage is the keyset form of ListFavorites the v2 listing
// uses: at most limit entries ordered by (added_at DESC, media_item_id DESC)
// strictly after the key (nil = from the newest row), and the cards of those
// entries in the same order, resolved exactly as ListFavorites resolves them.
func (h *PersonalDataHandler) ListFavoritesPage(ctx context.Context, viewer PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.Favorite, []CollectionItemView, error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	favorites, err := store.ListFavoritesPage(ctx, viewer.ProfileID, after, limit)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list favorites")
	}
	items, err := resolveItems(h, ctx, viewer, favorites, func(f userstore.Favorite) string { return f.MediaItemID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve favorite items")
	}
	return favorites, items, nil
}

// GetFavorite answers the profile's favorite entry for an item the viewer
// may see. found is false when the item is not a favorite; an item the
// viewer may not see is a 404 error, as v1 answers it.
func (h *PersonalDataHandler) GetFavorite(ctx context.Context, viewer PersonalListViewer, itemID string) (entry userstore.Favorite, found bool, err error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return userstore.Favorite{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return userstore.Favorite{}, false, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	fav, err := store.GetFavorite(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return userstore.Favorite{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check favorite")
	}
	if fav == nil {
		return userstore.Favorite{}, false, nil
	}
	return *fav, true, nil
}

// AddFavorite adds an item the viewer may see to the profile's favorites.
// Listeners hear only about an add that inserted the row: adding an item
// that is already a favorite succeeds and notifies nobody, so a retried add
// repeats no side effect.
func (h *PersonalDataHandler) AddFavorite(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	added, err := store.AddFavorite(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to add favorite")
	}
	if added {
		h.notifyListChange(ctx, watchsync.ListKindFavorites, watchsync.ListChangeAdded, viewer, itemID)
	}
	return nil
}

// RemoveFavorite removes an item from the profile's favorites. Removing an
// item that is not a favorite succeeds and notifies nobody, so a retried
// remove converges and repeats no side effect. No access check runs: the
// item may have left the viewer's scope since it was added, and the profile
// must still be able to drop it.
func (h *PersonalDataHandler) RemoveFavorite(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	removed, err := store.RemoveFavorite(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove favorite")
	}
	if removed {
		h.notifyListChange(ctx, watchsync.ListKindFavorites, watchsync.ListChangeRemoved, viewer, itemID)
	}
	return nil
}

// HandleCheckFavorite handles GET /favorites/{item_id}.
// Returns 204 if the item is a favorite, 404 if not.
func (h *PersonalDataHandler) HandleCheckFavorite(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	_, ok, err := h.GetFavorite(r.Context(), personalListViewerFromRequest(r), itemID)
	if err != nil {
		writeAPIError(w, err)
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
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.AddFavorite(r.Context(), personalListViewerFromRequest(r), itemID); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveFavorite handles DELETE /favorites/{item_id}.
func (h *PersonalDataHandler) HandleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.RemoveFavorite(r.Context(), personalListViewerFromRequest(r), itemID); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listChangeNoticeTimeout bounds the notices for one committed favorites or
// watchlist change, which no longer end with the request.
const listChangeNoticeTimeout = 30 * time.Second

// notifyListChange tells every listener about a favorites or watchlist row a
// write inserted or deleted: connected watch providers, the profile's
// recommendations, and the profile's clients. The row is already committed,
// so the notices run on a context the request's cancellation does not reach.
// A client that disconnects now would retry, and its retry changes no row and
// sends nothing.
func (h *PersonalDataHandler) notifyListChange(ctx context.Context, list watchsync.ListKind, change watchsync.ListChange, viewer PersonalListViewer, itemID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listChangeNoticeTimeout)
	defer cancel()
	h.dispatchLocalListEvent(ctx, list, change, viewer.UserID, viewer.ProfileID, itemID)
	notifySignalsChanged(ctx, h.signalsNotifier, viewer.UserID, viewer.ProfileID)
	member := change == watchsync.ListChangeAdded
	state, stateChange := userStateEventState{IsFavorite: &member}, "favorite"
	if list == watchsync.ListKindWatchlist {
		state, stateChange = userStateEventState{InWatchlist: &member}, "watchlist"
	}
	publishUserStateEvent(ctx, h.EventsHub, viewer.UserID, viewer.ProfileID, itemID, "", stateChange, state)
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
	limit, offset := parsePagination(r)
	entries, items, err := h.ListWatchlist(r.Context(), personalListViewerFromRequest(r), limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	// has_more comes from the raw store page, before hidden-series filtering
	// shrinks it — a full page that filters down still has more to fetch.
	writeJSON(w, http.StatusOK, itemsListResponse{Items: items, HasMore: len(entries) == limit})
}

// ListWatchlist answers the store page [offset, offset+limit) of the
// profile's watchlist, newest first, and the cards of those entries in the
// same order. Fully-watched series, entries the catalog no longer has, and
// entries the viewer may not see have no card, so the raw entries, not the
// cards, decide whether another page follows.
func (h *PersonalDataHandler) ListWatchlist(ctx context.Context, viewer PersonalListViewer, limit, offset int) ([]userstore.WatchlistEntry, []CollectionItemView, error) {
	// Promotion scans the whole profile, so a paged read runs it once, on the
	// first page, rather than once per page.
	if offset == 0 {
		h.promoteWatchlist(ctx, viewer)
	}
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entries, err := store.ListWatchlist(ctx, viewer.ProfileID, limit, offset)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list watchlist")
	}
	visible, err := h.filterHiddenWatchlistSeries(ctx, store, viewer.ProfileID, append([]userstore.WatchlistEntry(nil), entries...))
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to filter watchlist")
	}
	items, err := resolveItems(h, ctx, viewer, visible, func(e userstore.WatchlistEntry) string { return e.MediaItemID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve watchlist items")
	}
	return entries, items, nil
}

// ListWatchlistPage is the keyset form of ListWatchlist the v2 listing
// uses: at most limit entries ordered by (added_at DESC, media_item_id DESC)
// strictly after the key (nil = from the newest row), and the cards of the
// entries the viewer may see (fully-watched series hidden) in the same order.
func (h *PersonalDataHandler) ListWatchlistPage(ctx context.Context, viewer PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.WatchlistEntry, []CollectionItemView, error) {
	// Once per traversal, on the first page; see ListWatchlist.
	if after == nil {
		h.promoteWatchlist(ctx, viewer)
	}
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entries, err := store.ListWatchlistPage(ctx, viewer.ProfileID, after, limit)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list watchlist")
	}
	visible, err := h.filterHiddenWatchlistSeries(ctx, store, viewer.ProfileID, append([]userstore.WatchlistEntry(nil), entries...))
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to filter watchlist")
	}
	items, err := resolveItems(h, ctx, viewer, visible, func(e userstore.WatchlistEntry) string { return e.MediaItemID })
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve watchlist items")
	}
	return entries, items, nil
}

// VisibleItemIDs reports which of the given catalog items the viewer may see.
func (h *PersonalDataHandler) VisibleItemIDs(ctx context.Context, viewer PersonalListViewer, ids []string) (map[string]bool, error) {
	visible, err := h.itemRepo.EnsureAccessibleIDs(ctx, ids, viewer.Access)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to check item access")
	}
	return visible, nil
}

// ResolveCards answers the cards of the given catalog items the viewer may
// see, in the order given. Items the catalog no longer has or the viewer may
// not see have no card.
func (h *PersonalDataHandler) ResolveCards(ctx context.Context, viewer PersonalListViewer, ids []string) ([]CollectionItemView, error) {
	items, err := resolveItemsByIDs(h, ctx, viewer, ids)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve items")
	}
	return items, nil
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

// GetWatchlistEntry answers the profile's watchlist entry for an item the
// viewer may see. found is false when the item is not on the watchlist; an
// item the viewer may not see is a 404 error, as v1 answers it.
func (h *PersonalDataHandler) GetWatchlistEntry(ctx context.Context, viewer PersonalListViewer, itemID string) (entry userstore.WatchlistEntry, found bool, err error) {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return userstore.WatchlistEntry{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return userstore.WatchlistEntry{}, false, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	h.promoteWatchlistItem(ctx, viewer, itemID)
	e, err := store.GetWatchlistEntry(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return userstore.WatchlistEntry{}, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist")
	}
	if e == nil {
		return userstore.WatchlistEntry{}, false, nil
	}
	return *e, true, nil
}

// AddToWatchlist adds an item the viewer may see to the profile's
// watchlist. Listeners hear only about an add that inserted the row: adding
// an item that is already on it succeeds and notifies nobody, so a retried
// add repeats no side effect.
func (h *PersonalDataHandler) AddToWatchlist(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := h.ensureAccessibleItem(ctx, itemID, viewer.Access); err != nil {
		return apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	added, err := store.AddToWatchlist(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to add to watchlist")
	}
	if added {
		h.notifyListChange(ctx, watchsync.ListKindWatchlist, watchsync.ListChangeAdded, viewer, itemID)
	}
	return nil
}

// RemoveFromWatchlist removes an item from the profile's watchlist.
// Removing an item that is not on it succeeds and notifies nobody, so a
// retried remove converges and repeats no side effect. No access check
// runs: the item may have left the viewer's scope since it was added, and
// the profile must still be able to drop it.
func (h *PersonalDataHandler) RemoveFromWatchlist(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	removed, err := store.RemoveFromWatchlist(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove from watchlist")
	}
	if removed {
		h.notifyListChange(ctx, watchsync.ListKindWatchlist, watchsync.ListChangeRemoved, viewer, itemID)
	}
	return nil
}

// HandleCheckWatchlist handles GET /watchlist/{item_id}.
// Returns 204 if the item is on the watchlist, 404 if not.
func (h *PersonalDataHandler) HandleCheckWatchlist(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	_, ok, err := h.GetWatchlistEntry(r.Context(), personalListViewerFromRequest(r), itemID)
	if err != nil {
		writeAPIError(w, err)
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
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.AddToWatchlist(r.Context(), personalListViewerFromRequest(r), itemID); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveFromWatchlist handles DELETE /watchlist/{item_id}.
func (h *PersonalDataHandler) HandleRemoveFromWatchlist(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.RemoveFromWatchlist(r.Context(), personalListViewerFromRequest(r), itemID); err != nil {
		writeAPIError(w, err)
		return
	}
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
	return h.historyDisplayCards(ctx, viewer, display)
}

// HistoryPageCards keeps only the newest visible watch of each display card.
// Witnesses are selected across the whole group, not just the bounded raw
// page, so older repeats on later pages cannot emit that card again.
func (h *PersonalDataHandler) HistoryPageCards(ctx context.Context, viewer SectionViewer, entries []userstore.WatchHistoryEntry) ([]HistoryCardView, error) {
	display, err := catalog.ResolveHistoryDisplayEntries(ctx, entries, h.episodeRepo)
	if err != nil {
		return nil, err
	}
	groups, err := catalog.HistoryDisplayGroups(ctx, display, h.episodeRepo)
	if err != nil {
		return nil, err
	}
	store, err := h.storeProvider.ForUser(ctx, viewer.Access.UserID)
	if err != nil {
		return nil, err
	}
	witnesses, err := store.LatestHistoryIDs(ctx, viewer.Access.ProfileID, groups)
	if err != nil {
		return nil, err
	}
	latest := display[:0]
	for _, d := range display {
		if witnesses[d.DisplayID] == d.Entry.ID {
			latest = append(latest, d)
		}
	}
	return h.historyDisplayCards(ctx, viewer, latest)
}

func (h *PersonalDataHandler) historyDisplayCards(ctx context.Context, viewer SectionViewer, display []catalog.HistoryDisplayEntry) ([]HistoryCardView, error) {
	ids := make([]string, 0, len(display))
	for _, d := range display {
		ids = append(ids, d.DisplayID)
	}
	items, err := resolveItemsByIDs(h, ctx, PersonalListViewer{UserID: apimw.GetUserID(ctx), ProfileID: apimw.GetProfileID(ctx), Access: viewer.Access, ImageSize: viewer.ImageSize, ImageWidths: viewer.ImageWidths}, ids)
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
	addMediaItems := func(ids []string) {
		for _, mediaItemID := range ids {
			if _, ok := mediaItemSet[mediaItemID]; ok {
				continue
			}
			mediaItemSet[mediaItemID] = struct{}{}
			mediaItemIDs = append(mediaItemIDs, mediaItemID)
		}
	}
	var filelessIDs []string
	for _, target := range targets {
		resolvedIDs, fileless, resolveErr := h.resolveHistoryRemovalMediaItemIDs(ctx, target, filter)
		if resolveErr != nil {
			if isNotFound(resolveErr) {
				return apiError(http.StatusNotFound, "not_found", "History target not found")
			}
			return fieldError("targets", resolveErr.Error())
		}
		addMediaItems(resolvedIDs)
		filelessIDs = append(filelessIDs, fileless...)
	}
	watchedFileless, err := watchstate.WatchedItemIDs(ctx, store, profileID, filelessIDs)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove history")
	}
	addMediaItems(watchedFileless)

	if err := store.RemoveHistoryItems(ctx, profileID, mediaItemIDs, time.Now().UTC()); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove history")
	}

	notifySignalsChanged(ctx, h.signalsNotifier, userID, profileID)
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
func resolveItems[T any](h *PersonalDataHandler, ctx context.Context, viewer PersonalListViewer, entries []T, getID func(T) string) ([]itemListResponse, error) {
	if len(entries) == 0 || h.itemRepo == nil {
		return []itemListResponse{}, nil
	}

	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = getID(e)
	}
	return resolveItemsByIDs(h, ctx, viewer, ids)
}

func resolveItemsByIDs(h *PersonalDataHandler, ctx context.Context, viewer PersonalListViewer, ids []string) ([]itemListResponse, error) {
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
	art := viewer.Artwork()
	posterHint := requestVariantHint("featured", art.For(imagesize.SlotPoster))
	cardHint := requestVariantHint("card", art.For(imagesize.SlotBackdrop))
	itemIDs := make([]string, len(mediaItems))
	for i, mi := range mediaItems {
		itemIDs[i] = mi.ContentID
	}
	accessible, err := h.itemRepo.EnsureAccessibleIDs(ctx, itemIDs, filter)
	if err != nil {
		return nil, err
	}
	accessibleItems := make([]*models.MediaItem, 0, len(mediaItems))
	for _, mi := range mediaItems {
		if accessible[mi.ContentID] {
			accessibleItems = append(accessibleItems, mi)
		}
	}

	userStates := map[string]*itemUserStateResponse{}
	if store, profileID, ok := h.userStoreFor(ctx, viewer.UserID, viewer.ProfileID); ok {
		if resolvedStates, err := resolveItemUserStatesWithOptions(ctx, store, profileID, h.episodeRepo, accessibleItems, itemUserStateOptions{
			UserID:             viewer.UserID,
			EbookProgressStore: h.ebookProgressStore,
		}); err == nil {
			userStates = resolvedStates
		}
	}

	// Card images resolve together once every card is built, in one batch per
	// hint, rather than one lookup each.
	var images listCardImages
	for _, mi := range accessibleItems {
		resp := itemListResponse{
			ContentID:         mi.ContentID,
			Type:              mi.Type,
			Title:             mi.Title,
			Year:              mi.Year,
			Genres:            mi.Genres,
			ContentRating:     mi.ContentRating,
			AdvisoryAge:       mi.AdvisoryAge,
			AdvisorySource:    mi.AdvisorySource,
			RatingIMDB:        mi.RatingIMDB,
			Overview:          mi.Overview,
			PosterThumbhash:   mi.PosterThumbhash,
			BackdropThumbhash: mi.BackdropThumbhash,
			UserState:         userStates[mi.ContentID],
		}
		images.add(&resp.PosterURL, sizedPosterPath(mi.PosterPath, art.For(imagesize.SlotPoster)), posterHint)
		images.add(&resp.BackdropURL, sizedCardBackdropPath(mi.BackdropPath, art.For(imagesize.SlotBackdrop)), cardHint)
		byID[mi.ContentID] = &resp
	}
	logoPaths := localizedLogoPaths(ctx, h.detailSvc, accessibleItems, filter)

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
				parentLogoPaths := localizedLogoPaths(ctx, h.detailSvc, parentItems, filter)

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
						images.add(&resp.BackdropURL, sizedCardPath(ep.StillPath, artworkkey.ImageStill, art.For(imagesize.SlotBackdrop)), cardHint)
						resp.BackdropThumbhash = ep.StillThumbhash
					} else if parent != nil {
						images.add(&resp.BackdropURL, sizedCardBackdropPath(parent.BackdropPath, art.For(imagesize.SlotBackdrop)), cardHint)
						resp.BackdropThumbhash = parent.BackdropThumbhash
					}
					if parent != nil {
						images.add(&resp.PosterURL, sizedPosterPath(parent.PosterPath, art.For(imagesize.SlotPoster)), posterHint)
						resp.PosterThumbhash = parent.PosterThumbhash
						resp.Year = parent.Year
						resp.Genres = parent.Genres
						resp.ContentRating = parent.ContentRating
						if path := parentLogoPaths[parent.ContentID]; path != "" {
							logoPaths[ep.ContentID] = path
						}
					}
					byID[ep.ContentID] = &resp
				}
			}
		}
	}

	images.resolve(ctx, h.detailSvc)
	for contentID, url := range signListingLogos(ctx, h.detailSvc, logoPaths, art.For(imagesize.SlotLogo)) {
		if item, ok := byID[contentID]; ok {
			item.LogoURL = url
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

// resolveHistoryRemovalMediaItemIDs returns the media item IDs a target
// hides and, for a series or season, its file-less episodes as candidates
// (see seriesEpisodeIDs).
func (h *PersonalDataHandler) resolveHistoryRemovalMediaItemIDs(
	ctx context.Context,
	target historyRemovalTargetRequest,
	filter catalog.AccessFilter,
) ([]string, []string, error) {
	contentID := strings.TrimSpace(target.ContentID)
	scope := strings.TrimSpace(target.Scope)
	if contentID == "" {
		return nil, nil, errors.New("content_id is required")
	}
	switch scope {
	case "", historyRemovalScopeItem:
		scope = historyRemovalScopeItem
	case historyRemovalScopeShow:
	default:
		return nil, nil, errors.New("scope must be \"item\" or \"show\"")
	}

	if h.itemRepo != nil {
		item, err := h.itemRepo.GetByID(ctx, contentID)
		switch {
		case err == nil && item != nil:
			if err := h.itemRepo.EnsureAccessible(ctx, item.ContentID, filter); err != nil {
				return nil, nil, err
			}
			switch item.Type {
			case "movie", "ebook":
				// Hiding an ebook gates ebook_reader_progress reads via
				// user_history_hidden_items (updated_at <= hidden_before)
				// without deleting the progress row, so the reader position
				// survives: hidden is not the same as unread.
				return []string{item.ContentID}, nil, nil
			case "series":
				return h.seriesEpisodeIDs(ctx, item.ContentID)
			}
		case err != nil && !errors.Is(err, catalog.ErrItemNotFound):
			return nil, nil, err
		}
	}

	if h.seasonRepo != nil {
		season, err := h.seasonRepo.GetByID(ctx, contentID)
		switch {
		case err == nil && season != nil:
			if err := h.itemRepo.EnsureAccessible(ctx, season.SeriesID, filter); err != nil {
				return nil, nil, err
			}
			if scope == historyRemovalScopeShow {
				return h.seriesEpisodeIDs(ctx, season.SeriesID)
			}
			return h.seasonEpisodeIDs(ctx, season.ContentID)
		case err != nil && !errors.Is(err, catalog.ErrSeasonNotFound):
			return nil, nil, err
		}
	}

	if h.episodeRepo == nil {
		return nil, nil, catalog.ErrItemNotFound
	}

	episode, err := h.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		return nil, nil, err
	}
	if err := h.itemRepo.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
		return nil, nil, err
	}
	if scope == historyRemovalScopeShow {
		return h.seriesEpisodeIDs(ctx, episode.SeriesID)
	}
	return []string{episode.ContentID}, nil, nil
}

// seriesEpisodeIDs returns the series' episodes that have a library file,
// and separately the ones that don't. History shows a watch of either kind
// on the series card, so removal must reach both; RemoveHistory keeps only
// the file-less episodes the profile has watched, since a long-running show
// can have thousands of them.
func (h *PersonalDataHandler) seriesEpisodeIDs(ctx context.Context, seriesID string) ([]string, []string, error) {
	if h.episodeRepo == nil {
		return nil, nil, catalog.ErrItemNotFound
	}
	return h.episodeRepo.PartitionIDsBySeries(ctx, seriesID)
}

// seasonEpisodeIDs is seriesEpisodeIDs for one season.
func (h *PersonalDataHandler) seasonEpisodeIDs(ctx context.Context, seasonID string) ([]string, []string, error) {
	if h.episodeRepo == nil {
		return nil, nil, catalog.ErrItemNotFound
	}
	return h.episodeRepo.PartitionIDsBySeason(ctx, seasonID)
}

func (h *PersonalDataHandler) userStoreFor(ctx context.Context, userID int, profileID string) (userstore.UserStore, string, bool) {
	if h.storeProvider == nil || userID == 0 || profileID == "" {
		return nil, "", false
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return nil, "", false
	}
	return store, profileID, true
}

func (h *PersonalDataHandler) ensureAccessibleItem(ctx context.Context, itemID string, access catalog.AccessFilter) error {
	if h.itemRepo == nil {
		return nil
	}
	err := h.itemRepo.EnsureAccessible(ctx, itemID, access)
	if err == nil {
		return nil
	}
	// If not found in media_items, check if it's an episode and verify its parent series is accessible.
	if errors.Is(err, catalog.ErrItemNotFound) && h.episodeRepo != nil {
		ep, epErr := h.episodeRepo.GetByID(ctx, itemID)
		if epErr == nil {
			return h.itemRepo.EnsureAccessible(ctx, ep.SeriesID, access)
		}
	}
	return err
}

// listCardImages collects a listing's card image fields for batch resolution.
type listCardImages struct {
	pending []listCardImage
}

type listCardImage struct {
	dst        *string
	path, hint string
}

// add queues path to fill *dst, resolved at hint. Empty paths and the "-"
// placeholder leave *dst empty.
func (l *listCardImages) add(dst *string, path, hint string) {
	if path == "" || path == "-" {
		return
	}
	l.pending = append(l.pending, listCardImage{dst: dst, path: path, hint: hint})
}

// resolve fills every queued field, one batch per hint.
func (l *listCardImages) resolve(ctx context.Context, svc *catalog.DetailService) {
	if svc == nil || len(l.pending) == 0 {
		return
	}
	byHint := make(map[string][]string)
	type pathAtHint struct{ path, hint string }
	seen := make(map[pathAtHint]struct{}, len(l.pending))
	for _, image := range l.pending {
		key := pathAtHint{path: image.path, hint: image.hint}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		byHint[image.hint] = append(byHint[image.hint], image.path)
	}
	resolved := make(map[string]map[string]catalog.ResolvedImageURL, len(byHint))
	for _, hint := range slices.Sorted(maps.Keys(byHint)) {
		resolved[hint] = svc.PresignURLsWithExpiry(ctx, byHint[hint], hint)
	}
	for _, image := range l.pending {
		*image.dst = resolved[image.hint][image.path].URL
	}
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
