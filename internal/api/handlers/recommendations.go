package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type recommendationsEngine interface {
	SimilarItems(ctx context.Context, itemID string, limit int) ([]recommendations.ScoredItem, error)
	BecauseYouWatched(ctx context.Context, userID int, profileID string, sourceItemID string, limit int) ([]recommendations.ScoredItem, error)
	GetTasteProfileSummary(ctx context.Context, userID int, profileID string) (*recommendations.TasteProfileSummary, error)
}

type recommendationsReader interface {
	GetForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
	GetForYouRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error)
	GetSimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error)
	GetDiscoverRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error)
	GetSection(ctx context.Context, userID int, profileID, kind, key string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
	GetWatchTonight(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (recommendations.WatchTonightResult, error)
}

// RecommendationsHandler handles recommendation API endpoints.
type RecommendationsHandler struct {
	engine              recommendationsEngine
	reader              recommendationsReader
	storeProvider       userstore.UserStoreProvider
	ratingsRepo         *catalog.RatingsRepo
	recsRepo            *recommendations.Repo
	enabled             bool
	Fetcher             discoverFetcher
	DetailSvc           discoverPresigner
	CalendarRepo        calendarRepository
	EpisodeRepo         *catalog.EpisodeRepository
	WatchTonightFetcher watchTonightSectionFetcher
	CastFetcher         cardsCastFetcher
	EbookProgress       EbookReaderProgressLister
	// RecWorker enqueues asynchronous taste-profile refreshes after writes
	// (taste seeding). Optional — when nil, refresh is simply skipped.
	RecWorker ProfileRefreshRequester
	nowFn     func() time.Time
}

type discoverFetcher interface {
	FetchItemsByContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) ([]*models.MediaItem, error)
	FetchEpisodesByContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error)
	ListOverlaySummaries(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*models.OverlaySummary, error)
}

type discoverPresigner interface {
	PresignURL(ctx context.Context, path string, variant string) string
}

// NewRecommendationsHandler creates a new RecommendationsHandler.
func NewRecommendationsHandler(engine recommendationsEngine, reader recommendationsReader, storeProvider userstore.UserStoreProvider, ratingsRepo *catalog.RatingsRepo, recsRepo *recommendations.Repo, enabled bool) *RecommendationsHandler {
	return &RecommendationsHandler{
		engine:        engine,
		reader:        reader,
		storeProvider: storeProvider,
		ratingsRepo:   ratingsRepo,
		recsRepo:      recsRepo,
		enabled:       enabled,
		nowFn:         time.Now,
	}
}

// --- Response types ---

type scoredItemsResponse struct {
	Items []recommendations.ScoredItem `json:"items"`
}

type forYouMainResponse struct {
	Row *recommendations.ForYouRow `json:"row"`
}

// HandleSimilar handles GET /recommendations/similar/{item_id}.
func (h *RecommendationsHandler) HandleSimilar(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		writeJSON(w, http.StatusOK, scoredItemsResponse{Items: []recommendations.ScoredItem{}})
		return
	}

	itemID := chi.URLParam(r, "item_id")
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	limit, _ := parsePagination(r)
	if limit > 50 {
		limit = 50
	}
	if limit <= 0 {
		limit = 20
	}

	items, err := h.engine.SimilarItems(r.Context(), itemID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch similar items")
		return
	}

	if items == nil {
		items = []recommendations.ScoredItem{}
	}

	writeJSON(w, http.StatusOK, scoredItemsResponse{Items: items})
}

// HandleForYouMain handles GET /recommendations/for-you/main.
func (h *RecommendationsHandler) HandleForYouMain(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSON(w, http.StatusOK, forYouMainResponse{Row: nil})
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	limit, _ := parsePagination(r)
	if limit <= 0 {
		limit = 20
	}

	row, err := h.reader.GetForYouMain(r.Context(), userID, profileID, limit, filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "ForYouMain failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}

	writeJSON(w, http.StatusOK, forYouMainResponse{Row: row})
}

// HandleForYouRows handles GET /recommendations/for-you/rows.
func (h *RecommendationsHandler) HandleForYouRows(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSON(w, http.StatusOK, recommendations.ForYouResponse{Rows: []recommendations.ForYouRow{}})
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	limit, _ := parsePagination(r)
	if limit <= 0 {
		limit = 20
	}

	rows, err := h.reader.GetForYouRows(r.Context(), userID, profileID, limit, filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "ForYouRows failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}

	if rows == nil {
		rows = []recommendations.ForYouRow{}
	}

	writeJSON(w, http.StatusOK, recommendations.ForYouResponse{Rows: rows})
}

// HandleBecauseWatched handles GET /recommendations/because-watched/{item_id}.
func (h *RecommendationsHandler) HandleBecauseWatched(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	itemID := chi.URLParam(r, "item_id")
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	limit, _ := parsePagination(r)
	if limit <= 0 {
		limit = 20
	}

	items, err := h.engine.BecauseYouWatched(r.Context(), userID, profileID, itemID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}

	items = h.filterRecommendations(r, userID, profileID, items)

	if items == nil {
		items = []recommendations.ScoredItem{}
	}

	items = h.excludeWatchedRecommendations(r, userID, profileID, items)

	writeJSON(w, http.StatusOK, scoredItemsResponse{Items: items})
}

// HandleSimilarUsers handles GET /recommendations/similar-users.
func (h *RecommendationsHandler) HandleSimilarUsers(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSON(w, http.StatusOK, scoredItemsResponse{Items: []recommendations.ScoredItem{}})
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	limit, _ := parsePagination(r)
	if limit <= 0 {
		limit = 20
	}

	items, err := h.reader.GetSimilarUsersLiked(r.Context(), userID, profileID, limit, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}

	if items == nil {
		items = []recommendations.ScoredItem{}
	}

	writeJSON(w, http.StatusOK, scoredItemsResponse{Items: items})
}

// HandleTasteProfile handles GET /recommendations/taste-profile.
func (h *RecommendationsHandler) HandleTasteProfile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	summary, err := h.engine.GetTasteProfileSummary(r.Context(), userID, profileID)
	if err != nil || summary == nil {
		writeJSON(w, http.StatusOK, recommendations.TasteProfileSummary{
			TopGenres:         []string{},
			FavoriteDirectors: []string{},
			SignalCounts:      map[string]int{},
		})
		return
	}

	writeJSON(w, http.StatusOK, summary)
}

// HandlePopular handles GET /recommendations/popular?days=30&limit=20.
func (h *RecommendationsHandler) HandlePopular(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	items, err := h.recsRepo.GetPopularItems(r.Context(), days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch popular items")
		return
	}
	if items == nil {
		items = []recommendations.ScoredItem{}
	}

	items = h.excludeWatchedRecommendations(r, apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), items)

	writeJSON(w, http.StatusOK, scoredItemsResponse{Items: items})
}

// HandleRecentlyAdded handles GET /recommendations/recently-added?days=14&limit=20.
func (h *RecommendationsHandler) HandleRecentlyAdded(w http.ResponseWriter, r *http.Request) {
	days := 14
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	items, err := h.recsRepo.GetRecentlyAddedItems(r.Context(), days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recently added items")
		return
	}
	if items == nil {
		items = []recommendations.ScoredItem{}
	}

	items = h.excludeWatchedRecommendations(r, apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), items)

	writeJSON(w, http.StatusOK, scoredItemsResponse{Items: items})
}

// filterRecommendations removes watched items and low-rated items from the list.
func (h *RecommendationsHandler) filterRecommendations(r *http.Request, userID int, profileID string, items []recommendations.ScoredItem) []recommendations.ScoredItem {
	items = h.excludeWatchedRecommendations(r, userID, profileID, items)
	return h.excludeLowRatedRecommendations(r, userID, profileID, items)
}

func (h *RecommendationsHandler) excludeWatchedRecommendations(r *http.Request, userID int, profileID string, items []recommendations.ScoredItem) []recommendations.ScoredItem {
	if len(items) == 0 {
		return items
	}
	watchedSet, err := h.watchedItemIDSet(r.Context(), userID, profileID)
	if err != nil || len(watchedSet) == 0 {
		return items
	}

	filtered := make([]recommendations.ScoredItem, 0, len(items))
	for _, item := range items {
		if _, ok := watchedSet[item.MediaItemID]; ok {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (h *RecommendationsHandler) watchedItemIDSet(ctx context.Context, userID int, profileID string) (map[string]struct{}, error) {
	if h.recsRepo == nil {
		return map[string]struct{}{}, nil
	}

	if h.storeProvider != nil {
		store, err := h.storeProvider.ForUser(ctx, userID)
		if err == nil && store != nil {
			set, err := h.recsRepo.GetWatchedItemIDSetFromStore(ctx, store, profileID)
			if err == nil {
				return set, nil
			}
		}
	}

	return h.recsRepo.GetWatchedItemIDSet(ctx, userID, profileID)
}

func (h *RecommendationsHandler) excludeLowRatedRecommendations(r *http.Request, userID int, profileID string, items []recommendations.ScoredItem) []recommendations.ScoredItem {
	if len(items) == 0 || h.ratingsRepo == nil {
		return items
	}

	itemIDs := make([]string, len(items))
	for i, item := range items {
		itemIDs[i] = item.MediaItemID
	}

	ratings, err := h.ratingsRepo.ListForItems(r.Context(), userID, profileID, itemIDs)
	if err != nil {
		return items
	}

	filtered := make([]recommendations.ScoredItem, 0, len(items))
	for _, item := range items {
		if rating, ok := ratings[item.MediaItemID]; ok && rating <= 2 {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// --- Discover endpoint ---

type discoverRowResponse struct {
	Type        string                `json:"type"`
	Label       string                `json:"label"`
	SectionKind string                `json:"section_kind,omitempty"`
	SectionKey  string                `json:"section_key,omitempty"`
	Items       []sectionItemResponse `json:"items"`
}

type discoverResponse struct {
	Rows []discoverRowResponse `json:"rows"`
}

type sectionDetailResponse struct {
	Kind  string                `json:"kind"`
	Key   string                `json:"key,omitempty"`
	Type  string                `json:"type"`
	Label string                `json:"label"`
	Items []sectionItemResponse `json:"items"`
}

const (
	discoverForYouMaxItems     = 28
	discoverUpcomingWindowDays = 14
	discoverForYouLabel        = "For You"
	sectionDetailDefaultLimit  = recommendations.CacheCandidateLimit
)

type discoverRowModel struct {
	Type           string
	Label          string
	ClusterIndex   int
	Items          []recommendations.ScoredItem
	UpcomingEvents map[string]upcomingEventResponse
}

type upcomingDiscoverCandidate struct {
	DisplayID      string
	Event          upcomingEventResponse
	AirDateTime    time.Time
	BaseIndex      int
	EffectiveIndex int
	InWatchlist    bool
	IsFavorite     bool
}

type combinedForYouItem struct {
	Item           recommendations.ScoredItem
	BaseIndex      int
	EffectiveIndex int
	HasUpcoming    bool
	UpcomingEvent  *upcomingEventResponse
	AirDateTime    time.Time
}

// HandleDiscover handles GET /recommendations/discover.
// Returns all recommendation rows with fully enriched item metadata for the
// carousel-based discover page.
func (h *RecommendationsHandler) HandleDiscover(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.Fetcher == nil {
		writeJSON(w, http.StatusOK, discoverResponse{Rows: []discoverRowResponse{}})
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	rows, err := h.reader.GetDiscoverRows(r.Context(), userID, profileID, 20, filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "Discover failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}

	discoverRows := discoverRowModelsFromRecommendations(rows)

	discoverRows, upcomingErr := h.blendUpcomingIntoDiscoverRows(
		r.Context(),
		userID,
		profileID,
		filter,
		discoverRows,
		rows,
	)
	if upcomingErr != nil {
		slog.WarnContext(r.Context(),
			"Discover: schedule-aware blending unavailable; falling back to legacy rows", "component", "api",
			"user_id",
			userID,
			"profile_id",
			profileID,
			"error",
			upcomingErr,
		)
	}

	if len(discoverRows) == 0 {
		writeJSON(w, http.StatusOK, discoverResponse{Rows: []discoverRowResponse{}})
		return
	}

	// Collect unique item IDs across all rows for batched enrichment.
	seen := make(map[string]struct{})
	var allIDs []string
	for _, row := range discoverRows {
		for _, item := range row.Items {
			if _, ok := seen[item.MediaItemID]; ok {
				continue
			}
			seen[item.MediaItemID] = struct{}{}
			allIDs = append(allIDs, item.MediaItemID)
		}
	}

	enrichment, err := h.loadItemEnrichment(r.Context(), userID, profileID, filter, allIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch item details")
		return
	}

	resp := discoverResponse{Rows: make([]discoverRowResponse, 0, len(discoverRows))}
	for _, row := range discoverRows {
		kind, key := discoverRowSectionKey(row.Type, row.Label, row.ClusterIndex)
		respRow := discoverRowResponse{
			Type:        row.Type,
			Label:       row.Label,
			SectionKind: kind,
			SectionKey:  key,
			Items:       h.buildSectionItems(r.Context(), row.Items, enrichment, row.UpcomingEvents),
		}
		if len(respRow.Items) > 0 {
			resp.Rows = append(resp.Rows, respRow)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleSection handles GET /recommendations/section/{kind} and
// /recommendations/section/{kind}/{key}. It returns the full contents of a
// single recommendation row so the UI can render a dedicated "see all" page.
func (h *RecommendationsHandler) HandleSection(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.Fetcher == nil {
		writeError(w, http.StatusNotFound, "not_found", "Section not found")
		return
	}

	kind := chi.URLParam(r, "kind")
	key := chi.URLParam(r, "key")
	if kind == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Section kind is required")
		return
	}

	limit := sectionDetailDefaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > recommendations.CacheCandidateLimit {
		limit = recommendations.CacheCandidateLimit
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	row, err := h.reader.GetSection(r.Context(), userID, profileID, kind, key, limit, filter)
	if err != nil {
		slog.ErrorContext(r.Context(),
			"Section failed", "component", "api",
			"user_id", userID,
			"profile_id", profileID,
			"kind", kind,
			"key", key,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch section")
		return
	}
	if row == nil {
		writeJSON(w, http.StatusOK, sectionDetailResponse{
			Kind:  kind,
			Key:   key,
			Items: []sectionItemResponse{},
		})
		return
	}

	ids := make([]string, 0, len(row.Items))
	seen := make(map[string]struct{}, len(row.Items))
	for _, item := range row.Items {
		if _, ok := seen[item.MediaItemID]; ok {
			continue
		}
		seen[item.MediaItemID] = struct{}{}
		ids = append(ids, item.MediaItemID)
	}

	enrichment, err := h.loadItemEnrichment(r.Context(), userID, profileID, filter, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch item details")
		return
	}

	writeJSON(w, http.StatusOK, sectionDetailResponse{
		Kind:  kind,
		Key:   key,
		Type:  row.Type,
		Label: row.Label,
		Items: h.buildSectionItems(r.Context(), row.Items, enrichment, nil),
	})
}

// itemEnrichment caches the data needed to convert ScoredItem records into
// sectionItemResponse for a single request.
type itemEnrichment struct {
	items    map[string]*models.MediaItem
	overlays map[string]*models.OverlaySummary
	states   map[string]*itemUserStateResponse
}

func (h *RecommendationsHandler) loadItemEnrichment(
	ctx context.Context,
	userID int,
	profileID string,
	filter catalog.AccessFilter,
	ids []string,
) (*itemEnrichment, error) {
	out := &itemEnrichment{
		items: map[string]*models.MediaItem{},
	}
	if h.Fetcher == nil || len(ids) == 0 {
		return out, nil
	}

	mediaItems, err := h.Fetcher.FetchItemsByContentIDs(ctx, ids, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Recommendations: fetch items failed", "component", "api", "error", err)
		return nil, err
	}
	for _, mi := range mediaItems {
		out.items[mi.ContentID] = mi
	}

	overlays, overlayErr := h.Fetcher.ListOverlaySummaries(ctx, ids, filter)
	if overlayErr != nil {
		slog.ErrorContext(ctx, "Recommendations: overlay summaries failed", "component", "api", "error", overlayErr)
	} else {
		out.overlays = overlays
	}

	if h.storeProvider != nil {
		store, storeErr := h.storeProvider.ForUser(ctx, userID)
		if storeErr == nil && store != nil {
			states, stateErr := resolveItemUserStatesWithOptions(ctx, store, profileID, h.EpisodeRepo, mediaItems, itemUserStateOptions{
				UserID:             userID,
				EbookProgressStore: h.EbookProgress,
			})
			if stateErr == nil {
				out.states = states
			}
		}
	}

	return out, nil
}

func (h *RecommendationsHandler) buildSectionItems(
	ctx context.Context,
	scoredItems []recommendations.ScoredItem,
	enrichment *itemEnrichment,
	upcomingEvents map[string]upcomingEventResponse,
) []sectionItemResponse {
	out := make([]sectionItemResponse, 0, len(scoredItems))
	for _, scored := range scoredItems {
		mi, ok := enrichment.items[scored.MediaItemID]
		if !ok || mi == nil {
			continue
		}
		item := sectionItemResponse{
			ContentID:         mi.ContentID,
			Type:              mi.Type,
			Title:             mi.Title,
			Year:              mi.Year,
			Genres:            mi.Genres,
			Keywords:          mi.Keywords,
			Status:            mi.Status,
			RatingIMDB:        mi.RatingIMDB,
			RatingTMDB:        mi.RatingTMDB,
			RatingRTCritic:    mi.RatingRTCritic,
			RatingRTAudience:  mi.RatingRTAudience,
			OriginalLanguage:  mi.OriginalLanguage,
			Overview:          mi.Overview,
			PosterThumbhash:   mi.PosterThumbhash,
			BackdropThumbhash: mi.BackdropThumbhash,
		}
		if item.Genres == nil {
			item.Genres = []string{}
		}
		if item.Keywords == nil {
			item.Keywords = []string{}
		}
		if h.DetailSvc != nil {
			item.PosterURL = h.DetailSvc.PresignURL(ctx, featuredPosterPath(mi.PosterPath), "featured")
			item.BackdropURL = h.DetailSvc.PresignURL(ctx, featuredBackdropPath(mi.BackdropPath), "featured")
			item.LogoURL = h.DetailSvc.PresignURL(ctx, mi.LogoPath, "featured")
		}
		if enrichment.overlays != nil {
			item.OverlaySummary = enrichment.overlays[mi.ContentID]
		}
		if enrichment.states != nil {
			item.UserState = enrichment.states[mi.ContentID]
		}
		if upcomingEvents != nil {
			if upcomingEvent, ok := upcomingEvents[mi.ContentID]; ok {
				upcomingEventCopy := upcomingEvent
				item.UpcomingEvent = &upcomingEventCopy
			}
		}
		out = append(out, item)
	}
	return out
}

func discoverRowModelsFromRecommendations(rows []recommendations.ForYouRow) []discoverRowModel {
	if len(rows) == 0 {
		return nil
	}

	models := make([]discoverRowModel, 0, len(rows))
	for _, row := range rows {
		models = append(models, discoverRowModel{
			Type:         row.Type,
			Label:        row.Label,
			ClusterIndex: row.ClusterIndex,
			Items:        append([]recommendations.ScoredItem(nil), row.Items...),
		})
	}
	return models
}

// discoverRowSectionKey derives the URL kind/key pair used by the recommendation
// section detail page from a discover row's type/label/cluster_index. Returns
// empty strings when the row has no dedicated detail page (so the frontend
// renders the title without a link).
func discoverRowSectionKey(rowType, label string, clusterIndex int) (string, string) {
	switch rowType {
	case "cluster":
		// "For You" main row uses Type="cluster" with Label="For You" and no
		// cluster index assigned. Specific cluster rows have label "Because
		// you enjoy ...".
		if label == discoverForYouLabel {
			return recommendations.SectionKindForYouMain, ""
		}
		return recommendations.SectionKindCluster, strconv.Itoa(clusterIndex)
	case "similar_users_liked":
		return recommendations.SectionKindSimilarUsers, ""
	case recommendations.RecTypePopular:
		return recommendations.SectionKindPopular, ""
	case recommendations.RecTypeRecentlyAdded:
		return recommendations.SectionKindRecentlyAdded, ""
	case recommendations.RecTypeTopRated:
		return recommendations.SectionKindTopRated, ""
	case "genre_sampler":
		// Cold-start labels rows "Top X"; the warm-discover path labels them
		// "Popular in X". The genre name is always the label suffix.
		for _, prefix := range []string{"Popular in ", "Top "} {
			if name, ok := strings.CutPrefix(label, prefix); ok && name != "" {
				return recommendations.SectionKindGenre, name
			}
		}
	}
	return "", ""
}

func (h *RecommendationsHandler) blendUpcomingIntoDiscoverRows(
	ctx context.Context,
	userID int,
	profileID string,
	filter catalog.AccessFilter,
	discoverRows []discoverRowModel,
	baseRows []recommendations.ForYouRow,
) ([]discoverRowModel, error) {
	if h.CalendarRepo == nil {
		return discoverRows, nil
	}

	mainRowIndex := indexOfDiscoverForYouRow(discoverRows)
	if mainRowIndex < 0 {
		return discoverRows, nil
	}

	now := time.Now
	if h.nowFn != nil {
		now = h.nowFn
	}
	nowUTC := now().UTC()
	start := discoverUTCDate(nowUTC)
	end := start.AddDate(0, 0, discoverUpcomingWindowDays-1)

	events, err := h.CalendarRepo.ListEvents(ctx, catalog.CalendarFilter{
		Start:              start,
		End:                end,
		AllowedLibraryIDs:  filter.AllowedLibraryIDs,
		DisabledLibraryIDs: filter.DisabledLibraryIDs,
		MaxContentRating:   filter.MaxContentRating,
	})
	if err != nil {
		return discoverRows, err
	}
	if len(events) == 0 {
		return discoverRows, nil
	}

	premiereCandidates := categorizeUpcomingCandidates(events)
	if len(premiereCandidates) == 0 {
		return discoverRows, nil
	}

	displayIDs := collectUpcomingDisplayIDsFromCandidates(premiereCandidates)
	rankMap, firstSeenItems := buildDiscoverRankingData(baseRows)

	watchlistMap := map[string]bool{}
	favoritesMap := map[string]bool{}
	if h.storeProvider != nil && len(displayIDs) > 0 {
		store, err := h.storeProvider.ForUser(ctx, userID)
		if err != nil {
			return discoverRows, err
		}
		if store != nil {
			favoritesMap, err = store.ListFavoritesByMediaItems(ctx, profileID, displayIDs)
			if err != nil {
				return discoverRows, err
			}
			watchlistMap, err = store.ListWatchlistByMediaItems(ctx, profileID, displayIDs)
			if err != nil {
				return discoverRows, err
			}
		}
	}

	watchedSet, err := h.watchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return discoverRows, err
	}

	lowRatings := map[string]int{}
	if h.ratingsRepo != nil && len(displayIDs) > 0 {
		lowRatings, err = h.ratingsRepo.ListForItems(ctx, userID, profileID, displayIDs)
		if err != nil {
			return discoverRows, err
		}
	}

	discoverRows[mainRowIndex] = mergePremieresIntoDiscoverRow(
		discoverRows[mainRowIndex],
		rankMap,
		firstSeenItems,
		premiereCandidates,
		watchedSet,
		lowRatings,
		watchlistMap,
		favoritesMap,
		nowUTC,
	)

	return discoverRows, nil
}

func mergePremieresIntoDiscoverRow(
	row discoverRowModel,
	rankMap map[string]int,
	firstSeenItems map[string]recommendations.ScoredItem,
	candidates []upcomingDiscoverCandidate,
	watchedSet map[string]struct{},
	lowRatings map[string]int,
	watchlistMap map[string]bool,
	favoritesMap map[string]bool,
	now time.Time,
) discoverRowModel {
	if len(candidates) == 0 {
		return row
	}

	rankedPremieres := rankUpcomingCandidates(
		candidates,
		rankMap,
		watchedSet,
		lowRatings,
		watchlistMap,
		favoritesMap,
		now,
	)
	if len(rankedPremieres) == 0 {
		return row
	}

	combined := buildCombinedForYouItems(row.Items, rankMap)
	for _, candidate := range rankedPremieres {
		scoredItem, ok := firstSeenItems[candidate.DisplayID]
		if !ok {
			scoredItem = recommendations.ScoredItem{MediaItemID: candidate.DisplayID}
		}

		if existing, ok := combined[candidate.DisplayID]; ok {
			existing.Item = mergeScoredItem(scoredItem, existing.Item)
			existing.BaseIndex = candidate.BaseIndex
			existing.EffectiveIndex = candidate.EffectiveIndex
			existing.HasUpcoming = true
			existing.UpcomingEvent = &candidate.Event
			existing.AirDateTime = candidate.AirDateTime
			continue
		}

		combined[candidate.DisplayID] = &combinedForYouItem{
			Item:           scoredItem,
			BaseIndex:      candidate.BaseIndex,
			EffectiveIndex: candidate.EffectiveIndex,
			HasUpcoming:    true,
			UpcomingEvent:  &candidate.Event,
			AirDateTime:    candidate.AirDateTime,
		}
	}

	sorted := sortCombinedForYouItems(combined)
	if len(sorted) > discoverForYouMaxItems {
		sorted = sorted[:discoverForYouMaxItems]
	}

	row.Items = make([]recommendations.ScoredItem, 0, len(sorted))
	row.UpcomingEvents = make(map[string]upcomingEventResponse)
	for _, item := range sorted {
		row.Items = append(row.Items, item.Item)
		if item.HasUpcoming && item.UpcomingEvent != nil {
			row.UpcomingEvents[item.Item.MediaItemID] = *item.UpcomingEvent
		}
	}
	if len(row.UpcomingEvents) == 0 {
		row.UpcomingEvents = nil
	}

	return row
}

func categorizeUpcomingCandidates(
	events []catalog.CalendarEvent,
) []upcomingDiscoverCandidate {
	premieres := make(map[string]upcomingDiscoverCandidate)

	for _, event := range events {
		candidate, ok := buildUpcomingCandidate(event)
		if !ok {
			continue
		}
		if isUpcomingPremiere(event, candidate.Event.Badges) {
			insertUpcomingCandidate(premieres, candidate)
		}
	}

	return upcomingCandidatesFromMap(premieres)
}

func buildUpcomingCandidate(event catalog.CalendarEvent) (upcomingDiscoverCandidate, bool) {
	displayID := event.ContentID
	if event.Type != "movie" {
		if event.SeriesID == nil || *event.SeriesID == "" {
			return upcomingDiscoverCandidate{}, false
		}
		displayID = *event.SeriesID
	}

	return upcomingDiscoverCandidate{
		DisplayID:   displayID,
		AirDateTime: calendarEventAirDateTime(event),
		Event: upcomingEventResponse{
			Type:          event.Type,
			AirDate:       event.AirDate.Format("2006-01-02"),
			AirTime:       event.AirTime,
			EpisodeTitle:  event.EpisodeTitle,
			SeasonNumber:  event.SeasonNumber,
			EpisodeNumber: event.EpisodeNumber,
			Badges:        buildBadges(event),
		},
	}, true
}

func insertUpcomingCandidate(
	target map[string]upcomingDiscoverCandidate,
	candidate upcomingDiscoverCandidate,
) {
	existing, ok := target[candidate.DisplayID]
	if !ok || candidate.AirDateTime.Before(existing.AirDateTime) {
		target[candidate.DisplayID] = candidate
	}
}

func upcomingCandidatesFromMap(
	items map[string]upcomingDiscoverCandidate,
) []upcomingDiscoverCandidate {
	if len(items) == 0 {
		return nil
	}

	candidates := make([]upcomingDiscoverCandidate, 0, len(items))
	for _, candidate := range items {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].AirDateTime.Equal(candidates[j].AirDateTime) {
			return candidates[i].AirDateTime.Before(candidates[j].AirDateTime)
		}
		return candidates[i].DisplayID < candidates[j].DisplayID
	})
	return candidates
}

func rankUpcomingCandidates(
	candidates []upcomingDiscoverCandidate,
	rankMap map[string]int,
	watchedSet map[string]struct{},
	lowRatings map[string]int,
	watchlistMap map[string]bool,
	favoritesMap map[string]bool,
	now time.Time,
) []upcomingDiscoverCandidate {
	if len(candidates) == 0 {
		return nil
	}

	ranked := make([]upcomingDiscoverCandidate, 0, len(candidates))
	savedOnly := make([]upcomingDiscoverCandidate, 0, len(candidates))

	for _, candidate := range candidates {
		if _, watched := watchedSet[candidate.DisplayID]; watched {
			continue
		}
		if rating, rated := lowRatings[candidate.DisplayID]; rated && rating <= 2 {
			continue
		}

		candidate.InWatchlist = watchlistMap[candidate.DisplayID]
		candidate.IsFavorite = favoritesMap[candidate.DisplayID]

		if baseIndex, ok := rankMap[candidate.DisplayID]; ok {
			candidate.BaseIndex = baseIndex
			ranked = append(ranked, candidate)
			continue
		}
		if candidate.InWatchlist || candidate.IsFavorite {
			savedOnly = append(savedOnly, candidate)
		}
	}

	sort.SliceStable(savedOnly, func(i, j int) bool {
		if !savedOnly[i].AirDateTime.Equal(savedOnly[j].AirDateTime) {
			return savedOnly[i].AirDateTime.Before(savedOnly[j].AirDateTime)
		}
		return savedOnly[i].DisplayID < savedOnly[j].DisplayID
	})
	for i := range savedOnly {
		savedOnly[i].BaseIndex = len(rankMap) + i
		ranked = append(ranked, savedOnly[i])
	}

	for i := range ranked {
		ranked[i].EffectiveIndex = effectiveUpcomingIndex(
			ranked[i].BaseIndex,
			ranked[i].InWatchlist,
			ranked[i].IsFavorite,
			ranked[i].AirDateTime,
			now,
		)
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].EffectiveIndex != ranked[j].EffectiveIndex {
			return ranked[i].EffectiveIndex < ranked[j].EffectiveIndex
		}
		if !ranked[i].AirDateTime.Equal(ranked[j].AirDateTime) {
			return ranked[i].AirDateTime.Before(ranked[j].AirDateTime)
		}
		if ranked[i].BaseIndex != ranked[j].BaseIndex {
			return ranked[i].BaseIndex < ranked[j].BaseIndex
		}
		return ranked[i].DisplayID < ranked[j].DisplayID
	})

	return ranked
}

func buildDiscoverRankingData(rows []recommendations.ForYouRow) (map[string]int, map[string]recommendations.ScoredItem) {
	rankMap := make(map[string]int)
	firstSeenItems := make(map[string]recommendations.ScoredItem)
	nextIndex := 0
	for _, row := range rows {
		for _, item := range row.Items {
			if _, ok := rankMap[item.MediaItemID]; ok {
				continue
			}
			rankMap[item.MediaItemID] = nextIndex
			firstSeenItems[item.MediaItemID] = item
			nextIndex++
		}
	}
	return rankMap, firstSeenItems
}

func effectiveUpcomingIndex(
	baseIndex int,
	inWatchlist bool,
	isFavorite bool,
	airDateTime time.Time,
	now time.Time,
) int {
	effectiveIndex := baseIndex
	if inWatchlist {
		effectiveIndex -= 6
	}
	if isFavorite {
		effectiveIndex -= 3
	}

	timeUntilAiring := airDateTime.Sub(now)
	if timeUntilAiring <= 48*time.Hour {
		effectiveIndex -= 2
	} else if timeUntilAiring <= 7*24*time.Hour {
		effectiveIndex -= 1
	}

	return effectiveIndex
}

func collectUpcomingDisplayIDsFromCandidates(candidates []upcomingDiscoverCandidate) []string {
	if len(candidates) == 0 {
		return nil
	}

	displayIDs := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate.DisplayID]; ok {
			continue
		}
		seen[candidate.DisplayID] = struct{}{}
		displayIDs = append(displayIDs, candidate.DisplayID)
	}

	return displayIDs
}

func isUpcomingPremiere(event catalog.CalendarEvent, badges []string) bool {
	if event.Type == "movie" {
		return true
	}
	return hasUpcomingBadge(badges, "series_premiere") || hasUpcomingBadge(badges, "season_premiere")
}

func indexOfDiscoverForYouRow(rows []discoverRowModel) int {
	for i := range rows {
		if rows[i].Label == discoverForYouLabel {
			return i
		}
	}
	return -1
}

func buildCombinedForYouItems(
	items []recommendations.ScoredItem,
	rankMap map[string]int,
) map[string]*combinedForYouItem {
	combined := make(map[string]*combinedForYouItem, len(items))
	nextIndex := len(rankMap)
	for _, item := range items {
		baseIndex, ok := rankMap[item.MediaItemID]
		if !ok {
			baseIndex = nextIndex
			nextIndex++
		}
		combined[item.MediaItemID] = &combinedForYouItem{
			Item:           item,
			BaseIndex:      baseIndex,
			EffectiveIndex: baseIndex,
		}
	}
	return combined
}

func mergeScoredItem(
	primary recommendations.ScoredItem,
	fallback recommendations.ScoredItem,
) recommendations.ScoredItem {
	if primary.Score == 0 {
		primary.Score = fallback.Score
	}
	if primary.Reason == "" {
		primary.Reason = fallback.Reason
	}
	if primary.ReasonDetail == "" {
		primary.ReasonDetail = fallback.ReasonDetail
	}
	return primary
}

func sortCombinedForYouItems(combined map[string]*combinedForYouItem) []combinedForYouItem {
	items := make([]combinedForYouItem, 0, len(combined))
	for _, item := range combined {
		items = append(items, *item)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].EffectiveIndex != items[j].EffectiveIndex {
			return items[i].EffectiveIndex < items[j].EffectiveIndex
		}
		if items[i].HasUpcoming != items[j].HasUpcoming {
			return items[i].HasUpcoming
		}
		if items[i].HasUpcoming && items[j].HasUpcoming && !items[i].AirDateTime.Equal(items[j].AirDateTime) {
			return items[i].AirDateTime.Before(items[j].AirDateTime)
		}
		if items[i].BaseIndex != items[j].BaseIndex {
			return items[i].BaseIndex < items[j].BaseIndex
		}
		return items[i].Item.MediaItemID < items[j].Item.MediaItemID
	})

	return items
}

func hasUpcomingBadge(badges []string, target string) bool {
	for _, badge := range badges {
		if badge == target {
			return true
		}
	}
	return false
}

func calendarEventAirDateTime(event catalog.CalendarEvent) time.Time {
	base := discoverUTCDate(event.AirDate.UTC())
	if event.AirTime == nil || *event.AirTime == "" {
		return base
	}

	layouts := []string{"15:04:05", "15:04"}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, *event.AirTime)
		if err == nil {
			return time.Date(
				base.Year(),
				base.Month(),
				base.Day(),
				parsed.Hour(),
				parsed.Minute(),
				parsed.Second(),
				0,
				time.UTC,
			)
		}
	}

	return base
}

func discoverUTCDate(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
