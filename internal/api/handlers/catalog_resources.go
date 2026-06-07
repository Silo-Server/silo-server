package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

	detail, err := h.items.detailSvc.GetItemDetail(r.Context(), id, h.items.accessFilter(r))
	if err != nil {
		if isNotFound(err) {
			syntheticDetail, syntheticErr := h.syntheticSeasonDetail(r, id)
			if syntheticErr != nil {
				if isNotFound(syntheticErr) {
					writeError(w, http.StatusNotFound, "not_found", "Item not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get item detail")
				return
			}
			h.enrichItemDetail(r, syntheticDetail)
			h.items.maybeRequestStaleDetailMetadataRefresh(r.Context(), syntheticDetail)
			writeJSON(w, http.StatusOK, syntheticDetail)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get item detail")
		return
	}

	h.enrichItemDetail(r, detail)
	h.items.maybeRequestStaleDetailMetadataRefresh(r.Context(), detail)
	writeJSON(w, http.StatusOK, detail)
}

func (h *CatalogResourceHandler) HandleGetItemVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	detail, err := h.items.detailSvc.GetItemDetail(r.Context(), id, h.items.accessFilter(r))
	if err != nil {
		if isNotFound(err) {
			if _, _, ok := parseSyntheticSeasonID(id); ok {
				writeJSON(w, http.StatusOK, []catalog.FileVersion{})
				return
			}
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get item versions")
		return
	}

	if !h.items.requestCanViewFilePaths(r) {
		for i := range detail.Versions {
			detail.Versions[i].FilePath = ""
		}
	}

	writeJSON(w, http.StatusOK, detail.Versions)
}

func (h *CatalogResourceHandler) HandleGetItemEpisodes(w http.ResponseWriter, r *http.Request) {
	filter := h.items.accessFilter(r)
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if h.items.seasonRepo == nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	season, err := h.items.seasonRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrSeasonNotFound) {
			seriesID, seasonNum, ok := parseSyntheticSeasonID(id)
			if !ok {
				writeError(w, http.StatusNotFound, "not_found", "Item not found")
				return
			}
			if err := h.items.itemRepo.EnsureAccessible(r.Context(), seriesID, filter); err != nil {
				if isNotFound(err) {
					writeError(w, http.StatusNotFound, "not_found", "Item not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
				return
			}
			if err := h.items.ensurePresentationLibraryAccess(r.Context(), seriesID, filter); err != nil {
				if isNotFound(err) {
					writeError(w, http.StatusNotFound, "not_found", "Item not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
				return
			}
			episodes, err := h.items.episodeRepo.ListBySeason(r.Context(), seriesID, seasonNum)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
				return
			}
			if len(episodes) == 0 {
				writeError(w, http.StatusNotFound, "not_found", "Item not found")
				return
			}
			h.writeEpisodeResponses(w, r, episodes)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
		return
	}
	if err := h.items.itemRepo.EnsureAccessible(r.Context(), season.SeriesID, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
		return
	}
	if err := h.items.ensurePresentationLibraryAccess(r.Context(), season.SeriesID, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
		return
	}

	episodes, err := h.items.episodeRepo.ListBySeasonID(r.Context(), season.ContentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list item episodes")
		return
	}
	h.items.maybeRequestStaleSeasonMetadataRefresh(r.Context(), season.ContentID, episodes)

	resp := make([]episodeResponse, 0, len(episodes))
	fallbacks := h.items.episodeImageFallbacks(r.Context(), episodes)
	for _, ep := range episodes {
		epResp := h.items.toEpisodeResponseWithFallback(r, ep, fallbacks[ep.SeriesID])
		if h.items.fileRepo != nil {
			files, fileErr := h.items.fileRepo.GetByEpisodeID(r.Context(), ep.ContentID)
			if fileErr == nil {
				for _, f := range files {
					if !catalog.FileAllowedByAccess(f, filter) {
						continue
					}
					epResp.Files = append(epResp.Files, episodeFileResponse{
						FileID:        f.ID,
						Resolution:    f.Resolution,
						CodecVideo:    f.CodecVideo,
						HDR:           f.HDR,
						AudioChannels: f.AudioChannels,
						Container:     f.Container,
						FileSize:      f.FileSize,
					})
				}
			}
		}
		epResp.UserData = h.items.getLeafUserData(r, ep.ContentID)
		resp = append(resp, epResp)
	}

	writeJSON(w, http.StatusOK, episodesListResponse{Episodes: resp})
}

func (h *CatalogResourceHandler) HandleGetSeasons(w http.ResponseWriter, r *http.Request) {
	filter := h.items.accessFilter(r)
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Series ID is required")
		return
	}

	if err := h.items.itemRepo.EnsureAccessible(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list seasons")
		return
	}
	if err := h.items.ensurePresentationLibraryAccess(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list seasons")
		return
	}

	if h.items.seasonRepo != nil {
		seasons, err := h.items.seasonRepo.ListBySeries(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list seasons")
			return
		}

		if len(seasons) > 0 {
			resp := make([]seasonResponse, 0, len(seasons))
			for _, s := range seasons {
				sr := h.items.toSeasonResponse(r, id, s)
				if sr.EpisodeCount == 0 {
					continue
				}
				resp = append(resp, sr)
			}

			writeJSON(w, http.StatusOK, seasonsResponse{Seasons: resp})
			return
		}
	}

	summaries, err := h.items.episodeRepo.ListSeasons(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list seasons")
		return
	}

	resp := make([]seasonResponse, 0, len(summaries))
	for _, s := range summaries {
		title := "Season " + strconv.Itoa(s.SeasonNumber)
		if s.SeasonNumber == 0 {
			title = "Specials"
		}
		episodes, _ := h.items.episodeRepo.ListBySeason(r.Context(), id, s.SeasonNumber)
		resp = append(resp, seasonResponse{
			ContentID:    fmt.Sprintf("%s-S%02d", id, s.SeasonNumber),
			SeasonNumber: s.SeasonNumber,
			IsSpecials:   s.SeasonNumber == 0,
			EpisodeCount: s.EpisodeCount,
			Title:        title,
			UserData:     h.items.getAggregateUserData(r, episodes),
		})
	}

	writeJSON(w, http.StatusOK, seasonsResponse{Seasons: resp})
}

func (h *CatalogResourceHandler) HandleGetSeason(w http.ResponseWriter, r *http.Request) {
	filter := h.items.accessFilter(r)
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

	if err := h.items.itemRepo.EnsureAccessible(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get season")
		return
	}
	if err := h.items.ensurePresentationLibraryAccess(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get season")
		return
	}

	if h.items.seasonRepo != nil {
		season, err := h.items.seasonRepo.GetBySeriesAndNumber(r.Context(), id, num)
		switch {
		case err == nil:
			episodes, _ := h.items.episodeRepo.ListBySeasonID(r.Context(), season.ContentID)
			h.items.maybeRequestStaleSeasonMetadataRefresh(r.Context(), season.ContentID, episodes)
			writeJSON(w, http.StatusOK, seasonDetailResponse{
				Season: h.items.toSeasonResponse(r, id, season),
			})
			return
		case !errors.Is(err, catalog.ErrSeasonNotFound):
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get season")
			return
		}
	}

	episodes, err := h.items.episodeRepo.ListBySeason(r.Context(), id, num)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get season")
		return
	}
	if len(episodes) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Season not found")
		return
	}

	title := "Season " + strconv.Itoa(num)
	if num == 0 {
		title = "Specials"
	}
	seasonID := fmt.Sprintf("%s-S%02d", id, num)
	resp := seasonResponse{
		ContentID:    seasonID,
		SeasonNumber: num,
		IsSpecials:   num == 0,
		Title:        title,
		EpisodeCount: len(episodes),
		UserData:     h.items.getAggregateUserData(r, episodes),
	}
	writeJSON(w, http.StatusOK, seasonDetailResponse{Season: resp})
}

func (h *CatalogResourceHandler) HandleGetEpisodes(w http.ResponseWriter, r *http.Request) {
	filter := h.items.accessFilter(r)
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

	if err := h.items.itemRepo.EnsureAccessible(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list episodes")
		return
	}
	if err := h.items.ensurePresentationLibraryAccess(r.Context(), id, filter); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list episodes")
		return
	}

	episodes, err := h.items.episodeRepo.ListBySeason(r.Context(), id, num)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list episodes")
		return
	}
	if h.items.seasonRepo != nil {
		if season, seasonErr := h.items.seasonRepo.GetBySeriesAndNumber(r.Context(), id, num); seasonErr == nil && season != nil {
			h.items.maybeRequestStaleSeasonMetadataRefresh(r.Context(), season.ContentID, episodes)
		}
	}
	h.writeEpisodeResponses(w, r, episodes)
}

func (h *CatalogResourceHandler) syntheticSeasonDetail(r *http.Request, seasonID string) (*catalog.ItemDetail, error) {
	seriesID, seasonNum, ok := parseSyntheticSeasonID(seasonID)
	if !ok {
		return nil, catalog.ErrItemNotFound
	}
	if h.items.detailSvc == nil || h.items.episodeRepo == nil {
		return nil, catalog.ErrItemNotFound
	}

	seriesDetail, err := h.items.detailSvc.GetItemDetail(r.Context(), seriesID, h.items.accessFilter(r))
	if err != nil {
		return nil, err
	}

	episodes, err := h.items.episodeRepo.ListBySeason(r.Context(), seriesID, seasonNum)
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
	seasonResp := h.items.toSeasonResponse(r, seriesID, season)
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

func (h *CatalogResourceHandler) writeEpisodeResponses(w http.ResponseWriter, r *http.Request, episodes []*models.Episode) {
	filter := h.items.accessFilter(r)
	resp := make([]episodeResponse, 0, len(episodes))
	fallbacks := h.items.episodeImageFallbacks(r.Context(), episodes)
	for _, ep := range episodes {
		epResp := h.items.toEpisodeResponseWithFallback(r, ep, fallbacks[ep.SeriesID])
		if h.items.fileRepo != nil {
			files, fileErr := h.items.fileRepo.GetByEpisodeID(r.Context(), ep.ContentID)
			if fileErr == nil {
				for _, f := range files {
					if !catalog.FileAllowedByAccess(f, filter) {
						continue
					}
					epResp.Files = append(epResp.Files, episodeFileResponse{
						FileID:        f.ID,
						Resolution:    f.Resolution,
						CodecVideo:    f.CodecVideo,
						HDR:           f.HDR,
						AudioChannels: f.AudioChannels,
						Container:     f.Container,
						FileSize:      f.FileSize,
					})
				}
			}
		}
		epResp.UserData = h.items.getLeafUserData(r, ep.ContentID)
		resp = append(resp, epResp)
	}
	writeJSON(w, http.StatusOK, episodesListResponse{Episodes: resp})
}

func parseSyntheticSeasonID(contentID string) (string, int, bool) {
	seriesID, seasonPart, ok := strings.Cut(contentID, "-S")
	if !ok || seriesID == "" || seasonPart == "" {
		return "", 0, false
	}
	seasonNum, err := strconv.Atoi(seasonPart)
	if err != nil {
		return "", 0, false
	}
	return seriesID, seasonNum, true
}

func (h *CatalogResourceHandler) enrichItemDetail(r *http.Request, detail *catalog.ItemDetail) {
	switch detail.Type {
	case "season":
		if h.items.episodeRepo != nil {
			episodes, err := h.items.episodeRepo.ListBySeasonID(r.Context(), detail.ContentID)
			if err == nil {
				detail.SeasonUserData = h.items.getAggregateUserData(r, episodes)
			}
		}
	case "series":
		if h.items.episodeRepo != nil {
			episodes, err := h.items.episodeRepo.ListBySeries(r.Context(), detail.ContentID)
			if err == nil {
				detail.SeasonUserData = h.items.getAggregateUserData(r, episodes)
			}
		}
	case "movie", "episode", "audiobook":
		detail.SeasonUserData = h.items.getLeafUserData(r, detail.ContentID)
		applyEffectiveEditionPreference(detail.SeasonUserData, &detail.EffectiveVersionEditionKey)
	}

	if !h.items.requestCanViewFilePaths(r) {
		for i := range detail.Versions {
			detail.Versions[i].FilePath = ""
		}
		detail.FolderPaths = nil
	}

	h.enrichViewerState(r, detail)
}

func (h *CatalogResourceHandler) enrichViewerState(r *http.Request, detail *catalog.ItemDetail) {
	store, profileID, ok := h.items.userStoreForRequest(r)
	if !ok || detail == nil {
		return
	}

	isFavorite, err := store.IsFavorite(r.Context(), profileID, detail.ContentID)
	if err != nil {
		return
	}
	inWatchlist, err := store.InWatchlist(r.Context(), profileID, detail.ContentID)
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

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return
	}

	rating, err := h.items.ratingsRepo.Get(r.Context(), userID, profileID, detail.ContentID)
	if err != nil || rating == nil {
		return
	}

	detail.UserRating = &rating.Rating
}
