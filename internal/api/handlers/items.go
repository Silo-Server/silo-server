package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"log/slog"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// EpisodeFileProvider retrieves media files linked to an episode.
type EpisodeFileProvider interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
	ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaFile, error)
}

type batchEpisodeFileProvider interface {
	ListByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error)
}

type MetadataRefreshRequester interface {
	RequestStaleMetadataRefresh(ctx context.Context, targetType, contentID string) error
}

type LocalWatchEventDispatcher interface {
	HandleLocalWatchEvent(ctx context.Context, event watchsync.LocalWatchEvent) error
}

type EbookReaderProgressLister interface {
	ListByContentIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]EbookReaderProgress, error)
}

// ItemsHandler handles browse, search, item detail, and series endpoints.
type ItemsHandler struct {
	browseRepo               *catalog.BrowseRepository
	itemRepo                 *catalog.ItemRepository
	episodeRepo              *catalog.EpisodeRepository
	seasonRepo               *catalog.SeasonRepository
	ratingsRepo              ratingsRepository
	catalogResolver          *catalog.CatalogResolver
	fileRepo                 EpisodeFileProvider
	detailSvc                *catalog.DetailService
	storeProvider            userstore.UserStoreProvider
	watchState               *watchstate.Service
	profileStaler            ProfileStaler
	profileRefreshRequester  ProfileRefreshRequester
	metadataRefreshRequester MetadataRefreshRequester
	localWatchDispatcher     LocalWatchEventDispatcher
	ebookProgressStore       EbookReaderProgressLister
	EventsHub                *evt.Hub
	UserRepo                 *auth.UserRepository
}

// NewItemsHandler creates a new ItemsHandler.
func NewItemsHandler(
	browseRepo *catalog.BrowseRepository,
	itemRepo *catalog.ItemRepository,
	episodeRepo *catalog.EpisodeRepository,
	seasonRepo *catalog.SeasonRepository,
	ratingsRepo ratingsRepository,
	fileRepo EpisodeFileProvider,
	storeProvider userstore.UserStoreProvider,
	detailSvc *catalog.DetailService,
	providerIDRepo *catalog.ProviderIDRepository,
) *ItemsHandler {
	return &ItemsHandler{
		browseRepo:  browseRepo,
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		seasonRepo:  seasonRepo,
		ratingsRepo: ratingsRepo,
		catalogResolver: catalog.NewCatalogResolver(browseRepo, itemRepo).
			WithEpisodeRepository(episodeRepo).
			WithUserStoreProvider(storeProvider),
		fileRepo:      fileRepo,
		storeProvider: storeProvider,
		watchState: watchstate.NewService(storeProvider).WithStableIdentityResolver(
			watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo),
		),
		detailSvc: detailSvc,
	}
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *ItemsHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *ItemsHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

func (h *ItemsHandler) SetMetadataRefreshRequester(requester MetadataRefreshRequester) {
	h.metadataRefreshRequester = requester
}

func (h *ItemsHandler) SetLocalWatchEventDispatcher(dispatcher LocalWatchEventDispatcher) {
	h.localWatchDispatcher = dispatcher
}

func (h *ItemsHandler) SetEbookReaderProgressStore(store EbookReaderProgressLister) {
	h.ebookProgressStore = store
}

func (h *ItemsHandler) maybeRequestStaleDetailMetadataRefresh(ctx context.Context, detail *catalog.ItemDetail) {
	if h == nil || detail == nil || h.metadataRefreshRequester == nil {
		return
	}
	switch detail.Type {
	case "episode":
		if h.episodeRepo == nil {
			return
		}
		episode, err := h.episodeRepo.GetByID(ctx, detail.ContentID)
		if err == nil {
			h.maybeRequestStaleEpisodeMetadataRefresh(ctx, episode)
		}
	case "season":
		if h.episodeRepo == nil {
			return
		}
		episodes, err := h.episodeRepo.ListBySeasonID(ctx, detail.ContentID)
		if err == nil {
			h.maybeRequestStaleSeasonMetadataRefresh(ctx, detail.ContentID, episodes)
		}
	case "series":
		if h.itemRepo == nil {
			return
		}
		item, err := h.itemRepo.GetByID(ctx, detail.ContentID)
		if err == nil && item != nil && item.EpisodeMetadataIncomplete {
			h.requestStaleMetadataRefresh(ctx, "item", item.ContentID)
		}
	}
}

func (h *ItemsHandler) maybeRequestStaleEpisodeMetadataRefresh(ctx context.Context, episode *models.Episode) {
	if h == nil || h.metadataRefreshRequester == nil || episode == nil {
		return
	}
	if metadata.EpisodeHasActionableMetadataDebt(episode, time.Now()) {
		h.requestStaleMetadataRefresh(ctx, "episode", episode.ContentID)
	}
}

func (h *ItemsHandler) maybeRequestStaleSeasonMetadataRefresh(ctx context.Context, seasonID string, episodes []*models.Episode) {
	if h == nil || h.metadataRefreshRequester == nil || strings.TrimSpace(seasonID) == "" {
		return
	}
	now := time.Now()
	for _, episode := range episodes {
		if metadata.EpisodeHasActionableMetadataDebt(episode, now) {
			h.requestStaleMetadataRefresh(ctx, "season", seasonID)
			return
		}
	}
}

func (h *ItemsHandler) requestStaleMetadataRefresh(ctx context.Context, targetType, contentID string) {
	if h == nil || h.metadataRefreshRequester == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	if err := h.metadataRefreshRequester.RequestStaleMetadataRefresh(ctx, targetType, contentID); err != nil {
		slog.Warn("catalog: failed to request stale metadata refresh",
			"target_type", targetType,
			"content_id", contentID,
			"error", err)
	}
}

// --- Response types ---

// itemListResponse is the shape of a single item in browse/search list responses.
type itemListResponse struct {
	ContentID         string                 `json:"content_id"`
	Type              string                 `json:"type"`
	Title             string                 `json:"title"`
	SeriesTitle       string                 `json:"series_title,omitempty"`
	SeasonNumber      *int                   `json:"season_number,omitempty"`
	EpisodeNumber     *int                   `json:"episode_number,omitempty"`
	Year              int                    `json:"year,omitempty"`
	Runtime           int                    `json:"runtime,omitempty"`
	Genres            []string               `json:"genres"`
	Keywords          []string               `json:"keywords"`
	Studios           []string               `json:"studios,omitempty"`
	Networks          []string               `json:"networks,omitempty"`
	ContentRating     string                 `json:"content_rating,omitempty"`
	Status            string                 `json:"status"`
	ShowStatus        string                 `json:"show_status,omitempty"`
	RatingIMDB        *float64               `json:"rating_imdb,omitempty"`
	RatingTMDB        *float64               `json:"rating_tmdb,omitempty"`
	RatingRTCritic    *int                   `json:"rating_rt_critic,omitempty"`
	RatingRTAudience  *int                   `json:"rating_rt_audience,omitempty"`
	OriginalLanguage  string                 `json:"original_language,omitempty"`
	Overview          string                 `json:"overview,omitempty"`
	PosterURL         string                 `json:"poster_url,omitempty"`
	PosterThumbhash   string                 `json:"poster_thumbhash,omitempty"`
	BackdropURL       string                 `json:"backdrop_url,omitempty"`
	BackdropThumbhash string                 `json:"backdrop_thumbhash,omitempty"`
	ReleaseDate       *string                `json:"release_date,omitempty"`
	LastAirDate       *string                `json:"last_air_date,omitempty"`
	AddedAt           *time.Time             `json:"added_at,omitempty"`
	OverlaySummary    *models.OverlaySummary `json:"overlay_summary,omitempty"`
	SortMetrics       *sortMetricsResponse   `json:"sort_metrics,omitempty"`
	UserState         *itemUserStateResponse `json:"user_state,omitempty"`
}

type sortMetricsResponse struct {
	ReleaseDate    *string  `json:"release_date,omitempty"`
	RuntimeMinutes *int     `json:"runtime_minutes,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	BitrateKbps    *int     `json:"bitrate_kbps,omitempty"`
	ProgressRatio  *float64 `json:"progress_ratio,omitempty"`
	ViewedAt       string   `json:"viewed_at,omitempty"`
	PlayCount      *int     `json:"play_count,omitempty"`
}

// browseResponse is the paginated response for the /items endpoint.
type browseResponse struct {
	Total   int                `json:"total"`
	HasMore bool               `json:"has_more"`
	Items   []itemListResponse `json:"items"`
}

type itemFiltersResponse struct {
	Genres         []string `json:"genres"`
	Studios        []string `json:"studios"`
	Networks       []string `json:"networks"`
	Countries      []string `json:"countries"`
	ContentRatings []string `json:"content_ratings"`
}

// seasonResponse is the shape of a season in API responses.
type seasonResponse struct {
	ContentID       string                  `json:"content_id"`
	SeasonNumber    int                     `json:"season_number"`
	IsSpecials      bool                    `json:"is_specials,omitempty"`
	Title           string                  `json:"title"`
	Overview        string                  `json:"overview,omitempty"`
	AirDate         string                  `json:"air_date,omitempty"`
	EpisodeCount    int                     `json:"episode_count"`
	PosterURL       string                  `json:"poster_url,omitempty"`
	PosterThumbhash string                  `json:"poster_thumbhash,omitempty"`
	UserData        *catalog.SeasonUserData `json:"user_data,omitempty"`
}

// seasonsResponse wraps the seasons list for JSON serialization.
type seasonsResponse struct {
	Seasons []seasonResponse `json:"seasons"`
}

// seasonDetailResponse wraps a single season for JSON serialization.
type seasonDetailResponse struct {
	Season seasonResponse `json:"season"`
}

// episodesListResponse wraps the episodes list for JSON serialization.
type episodesListResponse struct {
	Episodes []episodeResponse `json:"episodes"`
}

// episodeFileResponse represents a file version available for an episode.
type episodeFileResponse struct {
	FileID        int    `json:"file_id"`
	Resolution    string `json:"resolution,omitempty"`
	CodecVideo    string `json:"codec_video,omitempty"`
	HDR           bool   `json:"hdr"`
	AudioChannels int    `json:"audio_channels,omitempty"`
	Container     string `json:"container,omitempty"`
	FileSize      int64  `json:"file_size"`
}

// episodeResponse is the shape of an episode in API responses.
type episodeResponse struct {
	ContentID      string                  `json:"content_id"`
	SeasonNumber   int                     `json:"season_number"`
	EpisodeNumber  int                     `json:"episode_number"`
	Title          string                  `json:"title"`
	Overview       string                  `json:"overview,omitempty"`
	AirDate        string                  `json:"air_date,omitempty"`
	Runtime        int                     `json:"runtime"`
	ImdbID         string                  `json:"imdb_id,omitempty"`
	TmdbID         string                  `json:"tmdb_id,omitempty"`
	TvdbID         string                  `json:"tvdb_id,omitempty"`
	StillURL       string                  `json:"still_url,omitempty"`
	StillThumbhash string                  `json:"still_thumbhash,omitempty"`
	UserData       *catalog.SeasonUserData `json:"user_data,omitempty"`
	Files          []episodeFileResponse   `json:"files,omitempty"`
}

type episodeImageFallback struct {
	Path      string
	Thumbhash string
}

type watchedStateResponse struct {
	ContentID     string `json:"content_id"`
	Type          string `json:"type"`
	AffectedCount int    `json:"affected_count"`
	Played        bool   `json:"played"`
}

// --- Handler methods ---

// HandleGetItems handles GET /items with filtering, sorting, and pagination.
func (h *ItemsHandler) HandleGetItems(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog")
	if values, ok := buildLegacyItemsCatalogValues(r.URL.Query()); ok && h.catalogResolver != nil {
		h.writeCatalogBrowseResponse(w, r, values)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
}

// HandleGetLatestItems handles GET /items/latest — shortcut for sort=created_at&order=desc.
func (h *ItemsHandler) HandleGetLatestItems(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog")
	if h.catalogResolver != nil {
		h.writeCatalogBrowseResponse(w, r, buildLegacyLatestCatalogValues(r.URL.Query()))
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
}

// HandleGetItemFilters handles GET /items/filters for distinct browse values.
func (h *ItemsHandler) HandleGetItemFilters(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/filters")
	if values, ok := buildLegacyItemsCatalogValues(r.URL.Query()); ok && h.catalogResolver != nil {
		h.writeCatalogFiltersResponse(w, r, values)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
}

// HandleGetItemDetail handles GET /items/{id}.
func (h *ItemsHandler) HandleGetItemDetail(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/items/{id}")
	h.catalogResourceHandler().HandleGetItemDetail(w, r)
}

// HandleGetWatchDetail handles GET /watch/{id}.
func (h *ItemsHandler) HandleGetWatchDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Watch target ID is required")
		return
	}

	detail, err := h.detailSvc.GetWatchDetail(r.Context(), id, h.accessFilter(r))
	if err != nil {
		switch {
		case catalog.IsWatchTargetNotPlayable(err):
			writeError(w, http.StatusBadRequest, "invalid_watch_target", "Content is not directly playable")
			return
		case isNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "Watch target not found")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get watch detail")
			return
		}
	}

	if detail.Type == "movie" || detail.Type == "episode" || detail.Type == "ebook" {
		detail.UserData = h.getLeafUserData(r, detail.ContentID, detail.Type)
		applyEffectiveEditionPreference(detail.UserData, &detail.EffectiveVersionEditionKey)
	}

	writeJSON(w, http.StatusOK, detail)
}

// HandleMarkWatched handles POST /watched/{id}.
func (h *ItemsHandler) HandleMarkWatched(w http.ResponseWriter, r *http.Request) {
	h.handleSetWatchedState(w, r, true)
}

// HandleMarkUnwatched handles DELETE /watched/{id}.
func (h *ItemsHandler) HandleMarkUnwatched(w http.ResponseWriter, r *http.Request) {
	h.handleSetWatchedState(w, r, false)
}

func (h *ItemsHandler) dispatchLocalWatchEvent(
	ctx context.Context,
	kind watchsync.LocalWatchEventKind,
	userID int,
	profileID string,
	result watchstate.ManualMarkResult,
) {
	if h == nil || h.localWatchDispatcher == nil {
		return
	}
	plays := watchsync.LocalPlaysFromHistory(result.Entries)
	if len(plays) == 0 {
		return
	}
	if err := h.localWatchDispatcher.HandleLocalWatchEvent(ctx, watchsync.LocalWatchEvent{
		Kind:      kind,
		UserID:    userID,
		ProfileID: profileID,
		Plays:     plays,
	}); err != nil {
		slog.Warn("failed to queue local watch provider event", "kind", kind, "user_id", userID, "profile_id", profileID, "error", err)
	}
}

func (h *ItemsHandler) handleSetWatchedState(w http.ResponseWriter, r *http.Request, played bool) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	id := chi.URLParam(r, "id")

	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile ID is required")
		return
	}

	targetType, targets, err := h.resolveWatchedTargets(r.Context(), id, h.accessFilter(r))
	if err != nil {
		switch {
		case isNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update watched state")
		}
		return
	}

	if h.watchState == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if played {
		leafTargets := make([]watchstate.LeafWatchTarget, 0, len(targets))
		for _, target := range targets {
			leafTargets = append(leafTargets, watchstate.LeafWatchTarget{
				MediaItemID:     target.ContentID,
				DurationSeconds: target.DurationSeconds,
			})
		}
		updatedAt := time.Now().UTC()
		var result watchstate.ManualMarkResult
		result, err = h.watchState.RecordManualMarkWatchedWithResult(r.Context(), userID, profileID, leafTargets, updatedAt)
		if err == nil {
			h.dispatchLocalWatchEvent(r.Context(), watchsync.LocalWatchEventMarkedWatched, userID, profileID, result)
		}
	} else {
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, target.ContentID)
		}
		var result watchstate.ManualMarkResult
		result, err = h.watchState.RecordManualMarkUnwatchedWithResult(r.Context(), userID, profileID, targetIDs)
		if err == nil {
			h.dispatchLocalWatchEvent(r.Context(), watchsync.LocalWatchEventMarkedUnwatched, userID, profileID, result)
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update watched state")
		return
	}

	triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(r.Context(), h.EventsHub, userID, profileID, id, "", "watched", userStateEventState{
		Played: boolPtr(played),
	})

	writeJSON(w, http.StatusOK, watchedStateResponse{
		ContentID:     id,
		Type:          targetType,
		AffectedCount: len(targets),
		Played:        played,
	})
}

// HandleGetItemVersions handles GET /items/{id}/versions.
func (h *ItemsHandler) HandleGetItemVersions(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/items/{id}/versions")
	h.catalogResourceHandler().HandleGetItemVersions(w, r)
}

// HandleGetItemEpisodes handles GET /items/{id}/episodes for season IDs.
func (h *ItemsHandler) HandleGetItemEpisodes(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/items/{id}/episodes")
	h.catalogResourceHandler().HandleGetItemEpisodes(w, r)
}

// HandleGetSeasons handles GET /series/{id}/seasons.
func (h *ItemsHandler) HandleGetSeasons(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/series/{id}/seasons")
	h.catalogResourceHandler().HandleGetSeasons(w, r)
}

// HandleGetSeason handles GET /series/{id}/seasons/{num}.
func (h *ItemsHandler) HandleGetSeason(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/series/{id}/seasons/{num}")
	h.catalogResourceHandler().HandleGetSeason(w, r)
}

// HandleGetEpisodes handles GET /series/{id}/seasons/{num}/episodes.
func (h *ItemsHandler) HandleGetEpisodes(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/series/{id}/seasons/{num}/episodes")
	h.catalogResourceHandler().HandleGetEpisodes(w, r)
}

func (h *ItemsHandler) catalogResourceHandler() *CatalogResourceHandler {
	return NewCatalogResourceHandler(h)
}

func (h *ItemsHandler) catalogHandler() *CatalogHandler {
	return NewCatalogHandler(h.catalogResolver, h)
}

func (h *ItemsHandler) writeCatalogBrowseResponse(w http.ResponseWriter, r *http.Request, values map[string][]string) bool {
	req, err := catalog.ParseCatalogRequest(values)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return true
	}

	result, err := h.catalogResolver.Resolve(r.Context(), req, h.accessFilter(r))
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return true
		}
		if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Catalog source not found")
			return true
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to browse items")
		return true
	}

	overlaySummaries := h.listOverlaySummaries(r.Context(), result.Items, h.accessFilter(r))
	userStates := h.listItemUserStates(r, result.Items)
	items := make([]itemListResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, h.toItemListResponseWithOverlay(r, item, overlaySummaries[item.ContentID], userStates[item.ContentID]))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   result.Total,
		HasMore: result.HasMore,
		Items:   items,
	})
	return true
}

func (h *ItemsHandler) writeCatalogFiltersResponse(w http.ResponseWriter, r *http.Request, values map[string][]string) bool {
	req, err := catalog.ParseCatalogRequest(values)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return true
	}

	filters, err := h.catalogResolver.ListFiltersWithOptions(
		r.Context(),
		req,
		h.accessFilter(r),
		catalog.CatalogFilterOptions{IncludeTechnical: false},
	)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return true
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item filters")
		return true
	}

	writeJSON(w, http.StatusOK, itemFiltersResponse{
		Genres:         filters.Genres,
		Studios:        filters.Studios,
		Networks:       filters.Networks,
		Countries:      filters.Countries,
		ContentRatings: filters.ContentRatings,
	})
	return true
}

// toItemListResponse converts a MediaItem to an itemListResponse with presigned URLs.
func (h *ItemsHandler) toItemListResponse(r *http.Request, item *models.MediaItem) itemListResponse {
	return h.toItemListResponseWithOverlay(r, item, nil, nil)
}

func (h *ItemsHandler) toItemListResponseWithOverlay(r *http.Request, item *models.MediaItem, overlaySummary *models.OverlaySummary, userState *itemUserStateResponse) itemListResponse {
	if h.detailSvc != nil {
		if localized, err := h.detailSvc.LocalizeItemModel(r.Context(), item, h.accessFilter(r)); err == nil && localized != nil {
			item = localized
		}
	}
	resp := itemListResponse{
		ContentID:         item.ContentID,
		Type:              item.Type,
		Title:             item.Title,
		Year:              item.Year,
		Runtime:           item.Runtime,
		Genres:            item.Genres,
		Keywords:          item.Keywords,
		Studios:           item.Studios,
		Networks:          item.Networks,
		ContentRating:     item.ContentRating,
		Status:            item.Status,
		ShowStatus:        item.ShowStatus,
		RatingIMDB:        item.RatingIMDB,
		RatingTMDB:        item.RatingTMDB,
		RatingRTCritic:    item.RatingRTCritic,
		RatingRTAudience:  item.RatingRTAudience,
		OriginalLanguage:  item.OriginalLanguage,
		Overview:          item.Overview,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropThumbhash: item.BackdropThumbhash,
		OverlaySummary:    overlaySummary,
		UserState:         userState,
	}

	resp.AddedAt = item.AddedAt
	resp.ReleaseDate = item.ReleaseDate
	resp.LastAirDate = item.LastAirDate
	resp.PosterURL = h.presignURL(r, cardThumbnailPath(item.PosterPath), "card")
	resp.BackdropURL = h.presignURL(r, cardThumbnailPath(item.BackdropPath), "card")
	if resp.Genres == nil {
		resp.Genres = []string{}
	}
	if resp.Keywords == nil {
		resp.Keywords = []string{}
	}

	return resp
}

func (h *ItemsHandler) listEpisodeBrowseMetadata(
	ctx context.Context,
	items []*models.MediaItem,
) map[string]struct {
	SeriesTitle   string
	SeasonNumber  *int
	EpisodeNumber *int
} {
	if h == nil || h.episodeRepo == nil {
		return map[string]struct {
			SeriesTitle   string
			SeasonNumber  *int
			EpisodeNumber *int
		}{}
	}

	episodeIDs := make([]string, 0)
	for _, item := range items {
		if item == nil || item.Type != "episode" || strings.TrimSpace(item.ContentID) == "" {
			continue
		}
		episodeIDs = append(episodeIDs, item.ContentID)
	}
	if len(episodeIDs) == 0 {
		return map[string]struct {
			SeriesTitle   string
			SeasonNumber  *int
			EpisodeNumber *int
		}{}
	}

	episodes, err := h.episodeRepo.GetByIDs(ctx, episodeIDs)
	if err != nil || len(episodes) == 0 {
		return map[string]struct {
			SeriesTitle   string
			SeasonNumber  *int
			EpisodeNumber *int
		}{}
	}

	seriesIDs := make([]string, 0, len(episodes))
	seenSeriesIDs := make(map[string]struct{}, len(episodes))
	for _, episode := range episodes {
		if episode == nil || strings.TrimSpace(episode.SeriesID) == "" {
			continue
		}
		if _, ok := seenSeriesIDs[episode.SeriesID]; ok {
			continue
		}
		seenSeriesIDs[episode.SeriesID] = struct{}{}
		seriesIDs = append(seriesIDs, episode.SeriesID)
	}

	seriesTitles := make(map[string]string, len(seriesIDs))
	if len(seriesIDs) > 0 {
		if seriesItems, err := h.itemRepo.GetByIDs(ctx, seriesIDs); err == nil {
			for _, seriesItem := range seriesItems {
				if seriesItem == nil {
					continue
				}
				seriesTitles[seriesItem.ContentID] = seriesItem.Title
			}
		}
	}

	result := make(map[string]struct {
		SeriesTitle   string
		SeasonNumber  *int
		EpisodeNumber *int
	}, len(episodes))
	for _, episode := range episodes {
		if episode == nil {
			continue
		}
		seasonNumber := episode.SeasonNumber
		episodeNumber := episode.EpisodeNumber
		result[episode.ContentID] = struct {
			SeriesTitle   string
			SeasonNumber  *int
			EpisodeNumber *int
		}{
			SeriesTitle:   seriesTitles[episode.SeriesID],
			SeasonNumber:  &seasonNumber,
			EpisodeNumber: &episodeNumber,
		}
	}
	return result
}

func (h *ItemsHandler) listItemUserStates(r *http.Request, items []*models.MediaItem) map[string]*itemUserStateResponse {
	store, profileID, ok := h.userStoreForRequest(r)
	if !ok {
		return map[string]*itemUserStateResponse{}
	}
	states, err := resolveItemUserStatesWithOptions(r.Context(), store, profileID, h.episodeRepo, items, itemUserStateOptions{
		UserID:             apimw.GetUserID(r.Context()),
		EbookProgressStore: h.ebookProgressStore,
	})
	if err != nil {
		return map[string]*itemUserStateResponse{}
	}
	return states
}

// toEpisodeResponse converts an Episode model to an API response.
func (h *ItemsHandler) toEpisodeResponse(r *http.Request, ep *models.Episode) episodeResponse {
	return h.toEpisodeResponseWithFallback(r, ep, episodeImageFallback{})
}

func (h *ItemsHandler) toEpisodeResponseWithFallback(r *http.Request, ep *models.Episode, fallback episodeImageFallback) episodeResponse {
	if h.detailSvc != nil {
		if localized, err := h.detailSvc.LocalizeEpisodeModel(r.Context(), ep, h.accessFilter(r)); err == nil && localized != nil {
			ep = localized
		}
	}
	stillPath := ep.StillPath
	stillThumbhash := ep.StillThumbhash
	if strings.TrimSpace(stillPath) == "" && strings.TrimSpace(fallback.Path) != "" {
		stillPath = fallback.Path
		stillThumbhash = fallback.Thumbhash
	}
	resp := episodeResponse{
		ContentID:      ep.ContentID,
		SeasonNumber:   ep.SeasonNumber,
		EpisodeNumber:  ep.EpisodeNumber,
		Title:          ep.Title,
		Overview:       ep.Overview,
		Runtime:        ep.Runtime,
		ImdbID:         ep.ImdbID,
		TmdbID:         ep.TmdbID,
		TvdbID:         ep.TvdbID,
		StillThumbhash: stillThumbhash,
	}

	if ep.AirDate != nil {
		resp.AirDate = ep.AirDate.Format("2006-01-02")
	}

	resp.StillURL = h.presignURL(r, cardThumbnailPath(stillPath), "card")

	return resp
}

func (h *ItemsHandler) episodeImageFallbacks(ctx context.Context, episodes []*models.Episode) map[string]episodeImageFallback {
	if h.itemRepo == nil || len(episodes) == 0 {
		return map[string]episodeImageFallback{}
	}
	seriesIDs := make([]string, 0, 1)
	seen := make(map[string]struct{})
	for _, ep := range episodes {
		if ep == nil || strings.TrimSpace(ep.SeriesID) == "" {
			continue
		}
		if _, ok := seen[ep.SeriesID]; ok {
			continue
		}
		seen[ep.SeriesID] = struct{}{}
		seriesIDs = append(seriesIDs, ep.SeriesID)
	}
	if len(seriesIDs) == 0 {
		return map[string]episodeImageFallback{}
	}

	seriesItems, err := h.itemRepo.GetByIDs(ctx, seriesIDs)
	if err != nil {
		return map[string]episodeImageFallback{}
	}
	fallbacks := make(map[string]episodeImageFallback, len(seriesItems))
	for _, series := range seriesItems {
		if series == nil {
			continue
		}
		path := strings.TrimSpace(series.BackdropPath)
		thumbhash := series.BackdropThumbhash
		if path == "" {
			path = strings.TrimSpace(series.PosterPath)
			thumbhash = series.PosterThumbhash
		}
		if path == "" {
			continue
		}
		fallbacks[series.ContentID] = episodeImageFallback{Path: path, Thumbhash: thumbhash}
	}
	return fallbacks
}

func (h *ItemsHandler) listOverlaySummaries(ctx context.Context, items []*models.MediaItem, filter catalog.AccessFilter) map[string]*models.OverlaySummary {
	summaries := make(map[string]*models.OverlaySummary, len(items))
	if h.fileRepo == nil || len(items) == 0 {
		return summaries
	}

	groupedFiles := h.listBrowseItemFiles(ctx, items, filter)
	for contentID, files := range groupedFiles {
		if summary := overlays.BuildSummary(files); summary != nil {
			summaries[contentID] = summary
		}
	}
	return summaries
}

func (h *ItemsHandler) listSortMetrics(
	ctx context.Context,
	items []*models.MediaItem,
	sortField string,
	filter catalog.AccessFilter,
	overlaySummaries map[string]*models.OverlaySummary,
	store userstore.UserStore,
	userID int,
	profileID string,
) map[string]*sortMetricsResponse {
	metrics := make(map[string]*sortMetricsResponse, len(items))
	switch sortField {
	case "release_date":
		for _, item := range items {
			if item == nil {
				continue
			}
			releaseDate := firstNonBlankPtr(item.ReleaseDate, item.FirstAirDate)
			if releaseDate != nil {
				metrics[item.ContentID] = &sortMetricsResponse{ReleaseDate: releaseDate}
			}
		}
	case "runtime":
		for _, item := range items {
			if item == nil || item.Runtime <= 0 {
				continue
			}
			runtimeMinutes := item.Runtime
			metrics[item.ContentID] = &sortMetricsResponse{RuntimeMinutes: &runtimeMinutes}
		}
	case "resolution":
		for _, item := range items {
			if item == nil {
				continue
			}
			if summary := overlaySummaries[item.ContentID]; summary != nil && strings.TrimSpace(summary.Resolution) != "" {
				metrics[item.ContentID] = &sortMetricsResponse{Resolution: summary.Resolution}
			}
		}
	case "bitrate":
		groupedFiles := h.listBrowseItemFiles(ctx, items, filter)
		for contentID, files := range groupedFiles {
			if bitrate := maxFileBitrate(files); bitrate > 0 {
				value := bitrate
				metrics[contentID] = &sortMetricsResponse{BitrateKbps: &value}
			}
		}
	case "progress", "date_viewed", "plays":
		h.listUserSortMetrics(ctx, items, sortField, store, userID, profileID, metrics)
	}
	return metrics
}

func (h *ItemsHandler) listUserSortMetrics(
	ctx context.Context,
	items []*models.MediaItem,
	sortField string,
	store userstore.UserStore,
	userID int,
	profileID string,
	metrics map[string]*sortMetricsResponse,
) {
	if store == nil || profileID == "" || len(items) == 0 {
		return
	}
	contentIDs := uniqueItemContentIDs(items)
	if len(contentIDs) == 0 {
		return
	}

	progressMap, err := store.ListProgressByMediaItems(ctx, profileID, contentIDs)
	if err != nil {
		return
	}
	ebookProgressMap, err := h.listEbookReaderProgressByContentIDs(ctx, userID, profileID, contentIDs)
	if err != nil {
		return
	}

	switch sortField {
	case "progress":
		for _, item := range items {
			if item == nil || item.ContentID == "" {
				continue
			}
			progress, ok := progressMap[item.ContentID]
			if ok && !progress.Completed && progress.PositionSeconds > 0 && progress.DurationSeconds > 0 {
				ratio := progress.PositionSeconds / progress.DurationSeconds
				metrics[item.ContentID] = &sortMetricsResponse{ProgressRatio: &ratio}
				continue
			}
			ebookProgress, ok := ebookProgressMap[item.ContentID]
			if !ok || ebookProgress.Progress <= 0 || ebookProgress.Progress >= 0.9 {
				continue
			}
			ratio := ebookProgress.Progress
			metrics[item.ContentID] = &sortMetricsResponse{ProgressRatio: &ratio}
		}
	case "date_viewed", "plays":
		history, err := listCompletedHistoryForItems(ctx, store, profileID, contentIDs)
		if err != nil {
			return
		}
		historyCounts := make(map[string]int, len(contentIDs))
		historyViewedAt := make(map[string]string, len(contentIDs))
		for _, entry := range history {
			if entry.MediaItemID == "" {
				continue
			}
			historyCounts[entry.MediaItemID]++
			if entry.WatchedAt > historyViewedAt[entry.MediaItemID] {
				historyViewedAt[entry.MediaItemID] = entry.WatchedAt
			}
		}
		for _, item := range items {
			if item == nil || item.ContentID == "" {
				continue
			}
			resp := &sortMetricsResponse{}
			if sortField == "date_viewed" {
				viewedAt := historyViewedAt[item.ContentID]
				if progress, ok := progressMap[item.ContentID]; ok && progress.Completed && progress.UpdatedAt > viewedAt {
					viewedAt = progress.UpdatedAt
				}
				if progress, ok := ebookProgressMap[item.ContentID]; ok && progress.Progress >= 0.9 {
					ebookViewedAt := progress.UpdatedAt.UTC().Format(time.RFC3339)
					if ebookViewedAt > viewedAt {
						viewedAt = ebookViewedAt
					}
				}
				if viewedAt == "" {
					continue
				}
				resp.ViewedAt = viewedAt
			} else {
				playCount := historyCounts[item.ContentID]
				if progress, ok := progressMap[item.ContentID]; ok && progress.Completed && playCount < 1 {
					playCount = 1
				}
				if progress, ok := ebookProgressMap[item.ContentID]; ok && progress.Progress >= 0.9 && playCount < 1 {
					playCount = 1
				}
				if playCount <= 0 {
					continue
				}
				resp.PlayCount = &playCount
			}
			metrics[item.ContentID] = resp
		}
	}
}

func (h *ItemsHandler) listEbookReaderProgressByContentIDs(
	ctx context.Context,
	userID int,
	profileID string,
	contentIDs []string,
) (map[string]EbookReaderProgress, error) {
	if h == nil || h.ebookProgressStore == nil || userID <= 0 || profileID == "" || len(contentIDs) == 0 {
		return nil, nil
	}
	return h.ebookProgressStore.ListByContentIDs(ctx, userID, profileID, contentIDs)
}

func uniqueItemContentIDs(items []*models.MediaItem) []string {
	contentIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		if _, ok := seen[item.ContentID]; ok {
			continue
		}
		seen[item.ContentID] = struct{}{}
		contentIDs = append(contentIDs, item.ContentID)
	}
	return contentIDs
}

func listCompletedHistoryForItems(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	contentIDs []string,
) ([]userstore.WatchHistoryEntry, error) {
	const pageSize = 500
	var all []userstore.WatchHistoryEntry
	for offset := 0; ; offset += pageSize {
		page, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
			ProfileID:    profileID,
			MediaItemIDs: contentIDs,
			Limit:        pageSize,
			Offset:       offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
}

func (h *ItemsHandler) listBrowseItemFiles(ctx context.Context, items []*models.MediaItem, filter catalog.AccessFilter) map[string][]*models.MediaFile {
	grouped := make(map[string][]*models.MediaFile, len(items))
	if h.fileRepo == nil || len(items) == 0 {
		return grouped
	}

	contentIDs := make([]string, 0, len(items))
	episodeIDs := make([]string, 0)
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		if _, ok := seen[item.ContentID]; ok {
			continue
		}
		seen[item.ContentID] = struct{}{}
		if item.Type == "episode" {
			episodeIDs = append(episodeIDs, item.ContentID)
		} else {
			contentIDs = append(contentIDs, item.ContentID)
		}
	}

	if len(contentIDs) > 0 {
		contentFiles, err := h.fileRepo.ListByContentIDs(ctx, contentIDs)
		if err == nil {
			for contentID, files := range contentFiles {
				grouped[contentID] = catalog.FilterMediaFilesByAccess(files, filter)
			}
		}
	}

	if len(episodeIDs) > 0 {
		if batchProvider, ok := h.fileRepo.(batchEpisodeFileProvider); ok {
			episodeFiles, err := batchProvider.ListByEpisodeIDs(ctx, episodeIDs)
			if err == nil {
				for episodeID, files := range episodeFiles {
					grouped[episodeID] = catalog.FilterMediaFilesByAccess(files, filter)
				}
			}
		}
	}

	return grouped
}

func firstNonBlankPtr(values ...*string) *string {
	for _, value := range values {
		if value == nil || strings.TrimSpace(*value) == "" {
			continue
		}
		copyValue := *value
		return &copyValue
	}
	return nil
}

func maxFileBitrate(files []*models.MediaFile) int {
	maxBitrate := 0
	for _, file := range files {
		if file != nil && file.Bitrate > maxBitrate {
			maxBitrate = file.Bitrate
		}
	}
	return maxBitrate
}

// toSeasonResponse converts a Season model to an API response.
func (h *ItemsHandler) toSeasonResponse(r *http.Request, seriesID string, s *models.Season) seasonResponse {
	if h.detailSvc != nil {
		if localized, err := h.detailSvc.LocalizeSeasonModel(r.Context(), s, h.accessFilter(r)); err == nil && localized != nil {
			s = localized
		}
	}
	episodes, _ := h.episodeRepo.ListBySeason(r.Context(), seriesID, s.SeasonNumber)

	resp := seasonResponse{
		ContentID:       s.ContentID,
		SeasonNumber:    s.SeasonNumber,
		IsSpecials:      s.SeasonNumber == 0,
		Title:           s.Title,
		Overview:        s.Overview,
		EpisodeCount:    len(episodes),
		PosterThumbhash: s.PosterThumbhash,
	}
	if s.AirDate != nil {
		resp.AirDate = s.AirDate.Format("2006-01-02")
	}
	resp.PosterURL = h.presignURL(r, featuredPosterPath(s.PosterPath), "featured")
	resp.UserData = h.getAggregateUserData(r, episodes)

	return resp
}

func (h *ItemsHandler) getLeafUserData(r *http.Request, contentID string, itemType ...string) *catalog.SeasonUserData {
	if len(itemType) > 0 && itemType[0] == "ebook" {
		return h.getEbookLeafUserData(r, contentID)
	}

	store, profileID, ok := h.userStoreForRequest(r)
	if !ok {
		return nil
	}

	progress, err := store.GetProgress(r.Context(), profileID, contentID)
	if err != nil || progress == nil {
		return nil
	}

	return &catalog.SeasonUserData{
		PositionSeconds: progress.PositionSeconds,
		DurationSeconds: progress.DurationSeconds,
		IsInProgress:    !progress.Completed && progress.PositionSeconds > 0,
		Played:          progress.Completed,
		LastFileID:      progress.LastFileID,
		LastResolution:  progress.LastResolution,
		LastHDR:         progress.LastHDR,
		LastCodecVideo:  progress.LastCodecVideo,
		LastEditionKey:  progress.LastEditionKey,
	}
}

func (h *ItemsHandler) getEbookLeafUserData(r *http.Request, contentID string) *catalog.SeasonUserData {
	if h == nil || h.ebookProgressStore == nil {
		return nil
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if userID <= 0 || profileID == "" || contentID == "" {
		return nil
	}

	progress, err := h.ebookProgressStore.ListByContentIDs(r.Context(), userID, profileID, []string{contentID})
	if err != nil {
		return nil
	}
	row, ok := progress[contentID]
	if !ok || row.Progress <= 0 {
		return nil
	}

	return &catalog.SeasonUserData{
		PositionSeconds: row.Progress,
		DurationSeconds: 1,
		IsInProgress:    row.Progress < 0.9,
		Played:          row.Progress >= 0.9,
	}
}

func applyEffectiveEditionPreference(userData *catalog.SeasonUserData, target **string) {
	if userData == nil || userData.LastEditionKey == nil || *userData.LastEditionKey == "" {
		return
	}
	if target != nil && *target == nil {
		*target = userData.LastEditionKey
	}
}

func (h *ItemsHandler) getAggregateUserData(r *http.Request, episodes []*models.Episode) *catalog.SeasonUserData {
	if len(episodes) == 0 {
		return nil
	}

	store, profileID, ok := h.userStoreForRequest(r)
	if !ok {
		return nil
	}

	var watchedCount int
	var inProgressCount int
	for _, ep := range episodes {
		progress, progressErr := store.GetProgress(r.Context(), profileID, ep.ContentID)
		if progressErr != nil || progress == nil {
			continue
		}
		if progress.Completed {
			watchedCount++
			continue
		}
		if progress.PositionSeconds > 0 {
			inProgressCount++
		}
	}

	unplayedCount := len(episodes) - watchedCount
	return &catalog.SeasonUserData{
		WatchedCount:    watchedCount,
		UnplayedCount:   unplayedCount,
		InProgressCount: inProgressCount,
		Played:          watchedCount == len(episodes),
	}
}

func (h *ItemsHandler) userStoreForRequest(r *http.Request) (userstore.UserStore, string, bool) {
	if h.storeProvider == nil {
		return nil, "", false
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return nil, "", false
	}

	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if profileID == "" {
		return nil, "", false
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		return nil, "", false
	}

	return store, profileID, true
}

// cardThumbnailPath converts an S3 image path from original to w300 for use in
// browse/card views (posters, backdrops, and stills at card size).
// Full URLs (TMDB/TVDB) and plugin-prefixed paths are returned as-is —
// variant selection is handled at resolution time for plugin paths.
func cardThumbnailPath(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	// Plugin-prefixed paths pass through — variant handled at resolution time.
	if strings.Contains(path, "://") {
		return path
	}
	return strings.Replace(path, "/original.", "/w300.", 1)
}

// featuredPosterPath converts an S3 poster path from original to w500 for
// featured/hero contexts (displayed at ~220px CSS / 440px retina).
// Full URLs (TMDB/TVDB) and plugin-prefixed paths are returned as-is.
func featuredPosterPath(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if strings.Contains(path, "://") {
		return path
	}
	return strings.Replace(path, "/original.", "/w500.", 1)
}

// featuredBackdropPath converts an S3 backdrop path from original to w1920 for
// featured/hero contexts (displayed at full viewport width). Episode stills
// used as backdrops lack a w1920 variant and clamp to their largest cached
// size. Full URLs (TMDB/TVDB) and plugin-prefixed paths are returned as-is.
func featuredBackdropPath(path string) string {
	return catalog.BackdropVariantPath(path, "w1920")
}

// presignURL resolves an image path to a usable URL, delegating to the
// DetailService which handles plugin-prefixed paths, HTTP pass-through,
// and legacy S3 presigning.
func (h *ItemsHandler) presignURL(r *http.Request, path string, variant string) string {
	if h.detailSvc != nil {
		return h.detailSvc.PresignURL(r.Context(), path, variant)
	}
	return ""
}

// HandleFilterItems handles POST /items/filter with a full rule-group filter body.
func (h *ItemsHandler) HandleFilterItems(w http.ResponseWriter, r *http.Request) {
	writeDeprecatedReadHeaders(w, "/api/v1/catalog/query")
	h.catalogHandler().HandlePostCatalogQuery(w, r)
}

type filterItemsRequest struct {
	sections.FilterConfig
	LibraryID int `json:"library_id"`
	Limit     int `json:"limit"`
	Offset    int `json:"offset"`
}

func filterItemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "first_air_date", "last_air_date",
		"matched_at", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, c := range cols {
		prefixed[i] = alias + "." + c
	}
	return strings.Join(prefixed, ", ")
}

func filterSortClause(sort, order string) string {
	dir := "DESC"
	if order == "asc" {
		dir = "ASC"
	}
	switch sort {
	case "rating":
		return fmt.Sprintf("ORDER BY mi.rating_imdb %s NULLS LAST", dir)
	case "year":
		return fmt.Sprintf("ORDER BY mi.year %s", dir)
	case "title":
		return fmt.Sprintf("ORDER BY LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title)) %s, LOWER(mi.title) %s, mi.content_id ASC", dir, dir)
	default:
		return fmt.Sprintf("ORDER BY mi.created_at %s", dir)
	}
}

func (h *ItemsHandler) accessFilter(r *http.Request) catalog.AccessFilter {
	selectedFileID := 0
	if fileIDRaw := strings.TrimSpace(r.URL.Query().Get("fileId")); fileIDRaw != "" {
		if fileID, err := strconv.Atoi(fileIDRaw); err == nil && fileID > 0 {
			selectedFileID = fileID
		}
	}

	var presentationLibraryID *int
	if libraryIDRaw := strings.TrimSpace(r.URL.Query().Get("library_id")); libraryIDRaw != "" {
		if libraryID, err := strconv.Atoi(libraryIDRaw); err == nil && libraryID > 0 {
			presentationLibraryID = &libraryID
		}
	}

	if scope, ok := access.GetScope(r.Context()); ok {
		return catalog.AccessFilter{
			AllowedLibraryIDs:     scope.AllowedLibraryIDs,
			DisabledLibraryIDs:    scope.DisabledLibraryIDs,
			MaxContentRating:      scope.MaxContentRating,
			MaxPlaybackQuality:    scope.MaxPlaybackQuality,
			PresentationLibraryID: presentationLibraryID,
			SelectedFileID:        selectedFileID,
			UserID:                apimw.GetUserID(r.Context()),
			ProfileID:             apimw.GetProfileID(r.Context()),
		}
	}

	var libraryIDs []int
	var maxPlaybackQuality string
	if h.UserRepo != nil {
		userID := apimw.GetUserID(r.Context())
		if userID != 0 {
			user, userErr := h.UserRepo.GetByID(r.Context(), userID)
			if userErr != nil {
				slog.Error("looking up user for library access", "error", userErr)
			} else {
				if user.LibraryIDs != nil {
					libraryIDs = user.LibraryIDs
				}
				maxPlaybackQuality = access.NormalizePlaybackQuality(user.MaxPlaybackQuality)
			}
		}
	}

	return catalog.AccessFilter{
		AllowedLibraryIDs:     libraryIDs,
		MaxPlaybackQuality:    maxPlaybackQuality,
		PresentationLibraryID: presentationLibraryID,
		SelectedFileID:        selectedFileID,
		UserID:                apimw.GetUserID(r.Context()),
		ProfileID:             apimw.GetProfileID(r.Context()),
	}
}

func (h *ItemsHandler) ensurePresentationLibraryAccess(ctx context.Context, contentID string, filter catalog.AccessFilter) error {
	if filter.PresentationLibraryID == nil {
		return nil
	}
	membership, err := h.itemRepo.GetItemsInLibrary(ctx, []string{contentID}, *filter.PresentationLibraryID)
	if err != nil {
		return err
	}
	if !membership[contentID] {
		return catalog.ErrItemNotFound
	}
	return nil
}

type watchedLeafTarget struct {
	ContentID       string
	DurationSeconds float64
}

func (h *ItemsHandler) resolveWatchedTargets(ctx context.Context, contentID string, filter catalog.AccessFilter) (string, []watchedLeafTarget, error) {
	item, err := h.itemRepo.GetByID(ctx, contentID)
	switch {
	case err == nil:
		if err := h.itemRepo.EnsureAccessible(ctx, contentID, filter); err != nil {
			return "", nil, err
		}
		switch item.Type {
		case "movie":
			return "movie", []watchedLeafTarget{{
				ContentID:       item.ContentID,
				DurationSeconds: h.contentDurationSeconds(ctx, item.ContentID, "", item.Runtime),
			}}, nil
		case "series":
			if h.episodeRepo == nil {
				return "", nil, catalog.ErrItemNotFound
			}
			episodes, err := h.episodeRepo.ListBySeries(ctx, item.ContentID)
			if err != nil {
				return "", nil, err
			}
			return "series", h.episodeTargets(ctx, episodes), nil
		default:
			return "", nil, catalog.ErrItemNotFound
		}
	case !errors.Is(err, catalog.ErrItemNotFound):
		return "", nil, err
	}

	if h.seasonRepo != nil {
		season, err := h.seasonRepo.GetByID(ctx, contentID)
		switch {
		case err == nil:
			if err := h.itemRepo.EnsureAccessible(ctx, season.SeriesID, filter); err != nil {
				return "", nil, err
			}
			if h.episodeRepo == nil {
				return "", nil, catalog.ErrItemNotFound
			}
			episodes, err := h.episodeRepo.ListBySeasonID(ctx, season.ContentID)
			if err != nil {
				return "", nil, err
			}
			return "season", h.episodeTargets(ctx, episodes), nil
		case !errors.Is(err, catalog.ErrSeasonNotFound):
			return "", nil, err
		}
	}

	if h.episodeRepo == nil {
		return "", nil, catalog.ErrItemNotFound
	}

	episode, err := h.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		return "", nil, err
	}
	if err := h.itemRepo.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
		return "", nil, err
	}

	return "episode", []watchedLeafTarget{{
		ContentID:       episode.ContentID,
		DurationSeconds: h.contentDurationSeconds(ctx, "", episode.ContentID, episode.Runtime),
	}}, nil
}

func (h *ItemsHandler) episodeTargets(ctx context.Context, episodes []*models.Episode) []watchedLeafTarget {
	targets := make([]watchedLeafTarget, 0, len(episodes))
	for _, episode := range episodes {
		targets = append(targets, watchedLeafTarget{
			ContentID:       episode.ContentID,
			DurationSeconds: h.contentDurationSeconds(ctx, "", episode.ContentID, episode.Runtime),
		})
	}
	return targets
}

func (h *ItemsHandler) contentDurationSeconds(ctx context.Context, contentID, episodeID string, fallbackRuntimeMinutes int) float64 {
	if h.fileRepo == nil {
		if fallbackRuntimeMinutes > 0 {
			return float64(fallbackRuntimeMinutes * 60)
		}
		return 0
	}

	var (
		files []*models.MediaFile
		err   error
	)

	switch {
	case episodeID != "":
		files, err = h.fileRepo.GetByEpisodeID(ctx, episodeID)
	case contentID != "":
		files, err = h.fileRepo.GetByContentID(ctx, contentID)
	}
	if err == nil {
		for _, file := range files {
			if file != nil && file.Duration > 0 {
				return float64(file.Duration)
			}
		}
	}

	if fallbackRuntimeMinutes > 0 {
		return float64(fallbackRuntimeMinutes * 60)
	}
	return 0
}

// isNotFound checks if an error is a "not found" sentinel.
func isNotFound(err error) bool {
	return errors.Is(err, catalog.ErrItemNotFound) ||
		errors.Is(err, catalog.ErrEpisodeNotFound) ||
		errors.Is(err, catalog.ErrSeasonNotFound)
}

func (h *ItemsHandler) requestCanViewFilePaths(r *http.Request) bool {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		return false
	}
	if claims.Role == "admin" {
		return true
	}
	if h == nil || h.UserRepo == nil {
		return false
	}
	user, err := h.UserRepo.GetByID(r.Context(), claims.UserID)
	if err != nil {
		slog.WarnContext(r.Context(), "checking file path visibility permissions", "user_id", claims.UserID, "error", err)
		return false
	}
	return auth.HasEffectivePermission(user, auth.PermissionMetadataCuration)
}
