package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// CatalogResourceHandler serves canonical catalog resource read routes.
type CatalogResourceHandler struct {
	items *ItemsHandler
}

// NewCatalogResourceHandler creates a new canonical catalog resource handler.
func NewCatalogResourceHandler(items *ItemsHandler) *CatalogResourceHandler {
	return &CatalogResourceHandler{items: items}
}

func (h *CatalogResourceHandler) HandleGetItemDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	view, err := h.ItemDetail(r.Context(), viewerFromRequest(r, filter), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *CatalogResourceHandler) HandleGetItemVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	view, err := h.ItemVersions(r.Context(), viewerFromRequest(r, filter), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// HandleGetMangaFiles returns the local file listing for a manga series (the
// series "View Details" dialog): folder paths plus per-chapter file rows.
func (h *CatalogResourceHandler) HandleGetMangaFiles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	view, err := h.MangaFiles(r.Context(), viewerFromRequest(r, filter), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *CatalogResourceHandler) HandleGetItemEpisodes(w http.ResponseWriter, r *http.Request) {
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	view, err := h.ItemEpisodes(r.Context(), viewerFromRequest(r, filter), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, episodesListResponse{Episodes: view})
}

func (h *CatalogResourceHandler) HandleGetSeasons(w http.ResponseWriter, r *http.Request) {
	includeArtwork, valid := seasonListArtwork(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_include_artwork", "include_artwork must be true or false")
		return
	}
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}
	view, err := h.seriesSeasons(r.Context(), viewerFromRequest(r, filter), id, includeArtwork)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seasonsResponse{Seasons: view})
}

func (h *CatalogResourceHandler) HandleGetSeason(w http.ResponseWriter, r *http.Request) {
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	numStr := chi.URLParam(r, "num")
	if id == "" || numStr == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID and season number are required")
		return
	}

	num, err := strconv.Atoi(numStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid season number")
		return
	}
	view, err := h.SeriesSeason(r.Context(), viewerFromRequest(r, filter), id, num)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seasonDetailResponse{Season: view})
}

func (h *CatalogResourceHandler) HandleGetEpisodes(w http.ResponseWriter, r *http.Request) {
	filter, ok := h.items.accessFilterOrError(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	numStr := chi.URLParam(r, "num")
	if id == "" || numStr == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID and season number are required")
		return
	}

	num, err := strconv.Atoi(numStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid season number")
		return
	}
	view, err := h.SeasonEpisodes(r.Context(), viewerFromRequest(r, filter), id, num)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, episodesListResponse{Episodes: view})
}

func (h *CatalogResourceHandler) syntheticSeasonDetail(ctx context.Context, v ItemViewer, seasonID string) (*catalog.ItemDetail, error) {
	seriesID, seasonNum, ok := parseSyntheticSeasonID(seasonID)
	if !ok {
		return nil, catalog.ErrItemNotFound
	}
	if h.items.detailSvc == nil || h.items.episodeRepo == nil {
		return nil, catalog.ErrItemNotFound
	}

	filter := v.Access

	seriesDetail, err := h.items.detailSvc.GetItemDetail(ctx, seriesID, filter)
	if err != nil {
		return nil, err
	}

	episodes, err := h.items.episodeRepo.ListBySeason(ctx, seriesID, seasonNum)
	if err != nil {
		return nil, fmt.Errorf("listing season episodes: %w", err)
	}
	if len(episodes) == 0 {
		return nil, catalog.ErrItemNotFound
	}

	season := &models.Season{
		ContentID:    seasonID,
		SeriesID:     seriesID,
		SeasonNumber: seasonNum,
	}
	if seasonNum == 0 {
		season.Title = "Specials"
	} else {
		season.Title = "Season " + strconv.Itoa(seasonNum)
	}
	seasonResp := h.items.toSeasonResponseFromEpisodes(
		ctx,
		v,
		seriesID,
		season,
		episodes,
		h.items.getAggregateUserData(ctx, v, episodes),
		filter.ImageSize,
	)
	return &catalog.ItemDetail{
		ContentID:         seasonID,
		Type:              "season",
		Title:             seasonResp.Title,
		Overview:          seasonResp.Overview,
		SeriesID:          seriesID,
		SeriesTitle:       seriesDetail.Title,
		SeasonNumber:      &seasonNum,
		EpisodeCount:      &seasonResp.EpisodeCount,
		IsSpecials:        seasonNum == 0,
		SeasonUserData:    seasonResp.UserData,
		Cast:              seriesDetail.Cast,
		Crew:              seriesDetail.Crew,
		BackdropURL:       seriesDetail.BackdropURL,
		BackdropThumbhash: seriesDetail.BackdropThumbhash,
		PosterThumbhash:   seasonResp.PosterThumbhash,
		Versions:          []catalog.FileVersion{},
		Subtitles:         []catalog.SubtitleInfo{},
	}, nil
}

func parseSyntheticSeasonID(contentID string) (string, int, bool) {
	return catalog.ParseSyntheticSeasonID(contentID)
}

func (h *CatalogResourceHandler) enrichItemDetail(ctx context.Context, v ItemViewer, detail *catalog.ItemDetail) {
	if detail == nil {
		return
	}
	{
		input := catalog.PlayableTargetInput{
			ContentID:    detail.ContentID,
			Type:         detail.Type,
			SeriesID:     detail.SeriesID,
			SeasonNumber: detail.SeasonNumber,
		}
		playTargets := h.items.resolvePlayableTargetInputs(ctx, v, []catalog.PlayableTargetInput{input}, nil, v.Access)
		detail.PlayContentID = playTargets[input.Key()]
	}

	switch detail.Type {
	case "season":
		if h.items.episodeRepo != nil {
			if userData, ok := h.items.parentRollupUserData(ctx, v, detail.Type, detail.ContentID); ok {
				detail.SeasonUserData = userData
			} else if episodes, err := h.items.episodeRepo.ListBySeasonID(ctx, detail.ContentID); err == nil {
				detail.SeasonUserData = h.items.getAggregateUserData(ctx, v, episodes)
			}
		}
	case "series":
		if h.items.episodeRepo != nil {
			if userData, ok := h.items.parentRollupUserData(ctx, v, detail.Type, detail.ContentID); ok {
				detail.SeasonUserData = userData
			} else if episodes, err := h.items.episodeRepo.ListBySeries(ctx, detail.ContentID); err == nil {
				detail.SeasonUserData = h.items.getAggregateUserData(ctx, v, episodes)
			}
		}
	case "movie", "episode", "audiobook", "ebook":
		detail.SeasonUserData = h.items.getLeafUserData(ctx, v, detail.ContentID, detail.Type)
		applyEffectiveEditionPreference(detail.SeasonUserData, &detail.EffectiveVersionEditionKey)
	}

	if !h.items.canViewFilePaths(ctx) {
		for i := range detail.Versions {
			detail.Versions[i].FilePath = ""
		}
		detail.FolderPaths = nil
	}

	h.enrichViewerState(ctx, v, detail)
}

func (h *CatalogResourceHandler) enrichViewerState(ctx context.Context, v ItemViewer, detail *catalog.ItemDetail) {
	store, profileID, ok := h.items.viewerUserStore(ctx, v.ProfileID)
	if !ok || detail == nil {
		return
	}

	isFavorite, err := store.IsFavorite(ctx, profileID, detail.ContentID)
	if err != nil {
		return
	}
	inWatchlist, err := store.InWatchlist(ctx, profileID, detail.ContentID)
	if err != nil {
		return
	}

	detail.UserState = &catalog.ItemUserState{
		Played:      detail.SeasonUserData != nil && detail.SeasonUserData.Played,
		IsFavorite:  isFavorite,
		InWatchlist: inWatchlist,
	}

	if h.items.ratingsRepo == nil {
		return
	}

	userID := apimw.GetUserID(ctx)
	if userID == 0 {
		return
	}

	rating, err := h.items.ratingsRepo.Get(ctx, userID, profileID, detail.ContentID)
	if err != nil || rating == nil {
		return
	}

	detail.UserRating = &rating.Rating
}

// seasonListArtworkParam is the series-seasons query parameter advertised by
// the images capability; false omits all poster preparation.
const seasonListArtworkParam = "include_artwork"

// seasonListArtwork allows text-only selectors to omit all poster preparation.
func seasonListArtwork(r *http.Request) (bool, bool) {
	value := r.URL.Query().Get(seasonListArtworkParam)
	if value == "" {
		return true, true
	}
	include, err := strconv.ParseBool(value)
	return include, err == nil
}
