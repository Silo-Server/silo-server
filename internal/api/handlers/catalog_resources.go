package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CatalogResourceHandler serves canonical catalog resource read routes.
type CatalogResourceHandler struct {
	items             *ItemsHandler
	watchlistPromoter WatchlistItemPromoter
}

// WatchlistItemPromoter moves the profile's watchlist entry for a title the
// library did not have onto the library watchlist once the item carries the
// title's IDs. *watchlist.Titles implements it; a failure is its to log.
// QueuePromotable queues, on a batch sent to the catalog database, whether
// the profile has any entry PromoteWatchlistItem could move.
type WatchlistItemPromoter interface {
	PromoteWatchlistItem(ctx context.Context, access catalog.AccessFilter, contentID string)
	QueuePromotable(b *pgx.Batch, access catalog.AccessFilter, promotable *bool)
}

// SetWatchlistPromoter makes item detail promote the viewer's matching
// watchlist entry before it reports user_state.in_watchlist.
func (h *CatalogResourceHandler) SetWatchlistPromoter(p WatchlistItemPromoter) {
	h.watchlistPromoter = p
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
	view, err := h.ItemDetail(r.Context(), viewerFromRequest(r, filter), id, false)
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
		filter.ImageSizeFor(imagesize.SlotPoster),
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

// enrichItemDetail adds the viewer's state to detail. curates is
// canViewFilePaths for the request; paths are stripped without it. A
// prefetch leaves watchlist promotion to the view that follows it.
func (h *CatalogResourceHandler) enrichItemDetail(ctx context.Context, v ItemViewer, detail *catalog.ItemDetail, curates, prefetch bool) {
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
		target := h.items.resolvePlayableTargets(ctx, v, []catalog.PlayableTargetInput{input}, nil, v.Access)[input.Key()]
		detail.PlayContentID = target.ContentID
		detail.PlaySeasonNumber = target.SeasonNumber
	}

	state, batched := h.batchedViewerState(ctx, v, detail, prefetch)
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
		if batched && detail.Type != itemTypeEbook {
			detail.SeasonUserData = state.leaf
		} else {
			detail.SeasonUserData = h.items.getLeafUserData(ctx, v, detail.ContentID, detail.Type)
		}
		applyEffectiveEditionPreference(detail.SeasonUserData, &detail.EffectiveVersionEditionKey)
	}

	detail.ViewerCurates = curates
	if !detail.ViewerCurates {
		for i := range detail.Versions {
			detail.Versions[i].FilePath = ""
		}
		detail.FolderPaths = nil
	}

	if batched {
		state.applyTo(detail)
		return
	}
	h.enrichViewerState(ctx, v, detail, prefetch)
}

// detailViewerState is the viewer's own state for one item detail, as
// batchedViewerState reads it.
type detailViewerState struct {
	// read is false when the batch failed; the detail then shows no viewer
	// state, as it shows none when a one-at-a-time read fails.
	read      bool
	leaf      *catalog.SeasonUserData
	userState catalog.ItemUserState
	rating    *int
}

// applyTo sets the detail's user state and rating. Played comes from the
// detail's user data, which the caller has already set.
func (s detailViewerState) applyTo(detail *catalog.ItemDetail) {
	if !s.read {
		return
	}
	userState := s.userState
	userState.Played = detail.SeasonUserData != nil && detail.SeasonUserData.Played
	detail.UserState = &userState
	if s.rating != nil {
		detail.UserRating = s.rating
	}
}

// batchedViewerState reads in one round trip what enrichItemDetail and
// enrichViewerState otherwise read one query at a time: a leaf item's
// progress (and a movie's version choice), the favorite, watchlist and
// not-interested marks, the rating, and whether the profile has a watchlist
// title entry that promotion could move onto the library watchlist. It
// needs a store whose state lives beside the catalog; batched is false
// otherwise, and the caller reads one at a time.
func (h *CatalogResourceHandler) batchedViewerState(ctx context.Context, v ItemViewer, detail *catalog.ItemDetail, prefetch bool) (state detailViewerState, batched bool) {
	store, profileID, ok := h.items.viewerUserStore(ctx, v.ProfileID)
	if !ok || h.items.browseRepo == nil || h.items.browseRepo.Pool() == nil {
		return state, false
	}
	leaf := detail.Type == itemTypeMovie || detail.Type == itemTypeEpisode || detail.Type == itemTypeAudiobook
	var batch pgx.Batch
	var viewer userstore.ItemViewerState
	if !userstore.QueueItemViewerState(store, &batch, userstore.ItemViewerStateQuery{
		ProfileID:     profileID,
		MediaItemID:   detail.ContentID,
		Progress:      leaf,
		VersionChoice: detail.Type == itemTypeMovie,
		Marks:         true,
	}, &viewer) {
		return state, false
	}
	userID := apimw.GetUserID(ctx)
	var rating *catalog.UserRating
	if h.items.ratingsRepo != nil {
		catalog.QueueUserRating(&batch, userID, profileID, detail.ContentID, &rating)
	}
	promoteAccess := v.Access
	promoteAccess.UserID, promoteAccess.ProfileID = userID, profileID
	var promotable bool
	if !prefetch && h.watchlistPromoter != nil && (detail.Type == itemTypeMovie || detail.Type == itemTypeSeries) {
		h.watchlistPromoter.QueuePromotable(&batch, promoteAccess, &promotable)
	}
	if err := h.items.browseRepo.Pool().SendBatch(ctx, &batch).Close(); err != nil {
		return state, true
	}
	if promotable {
		// The batch read the watchlist before promotion could add the item.
		h.watchlistPromoter.PromoteWatchlistItem(ctx, promoteAccess, detail.ContentID)
		inWatchlist, err := store.InWatchlist(ctx, profileID, detail.ContentID)
		if err != nil {
			return state, true
		}
		viewer.InWatchlist = inWatchlist
	}

	state.read = true
	if leaf {
		state.leaf = leafUserDataFromProgress(viewer.Progress)
		if detail.Type == itemTypeMovie {
			state.leaf = applyVersionChoice(state.leaf, viewer.VersionChoice)
		}
	}
	state.userState = catalog.ItemUserState{
		IsFavorite:    viewer.IsFavorite,
		InWatchlist:   viewer.InWatchlist,
		NotInterested: viewer.NotInterested,
	}
	if rating != nil {
		state.rating = &rating.Rating
	}
	return state, true
}

func (h *CatalogResourceHandler) enrichViewerState(ctx context.Context, v ItemViewer, detail *catalog.ItemDetail, prefetch bool) {
	store, profileID, ok := h.items.viewerUserStore(ctx, v.ProfileID)
	if !ok || detail == nil {
		return
	}

	isFavorite, err := store.IsFavorite(ctx, profileID, detail.ContentID)
	if err != nil {
		return
	}
	if !prefetch && h.watchlistPromoter != nil && (detail.Type == "movie" || detail.Type == "series") {
		promoteAccess := v.Access
		promoteAccess.UserID, promoteAccess.ProfileID = apimw.GetUserID(ctx), profileID
		h.watchlistPromoter.PromoteWatchlistItem(ctx, promoteAccess, detail.ContentID)
	}
	inWatchlist, err := store.InWatchlist(ctx, profileID, detail.ContentID)
	if err != nil {
		return
	}
	notInterested, err := store.GetNotInterested(ctx, profileID, detail.ContentID)
	if err != nil {
		return
	}

	detail.UserState = &catalog.ItemUserState{
		Played:        detail.SeasonUserData != nil && detail.SeasonUserData.Played,
		IsFavorite:    isFavorite,
		InWatchlist:   inWatchlist,
		NotInterested: notInterested != nil,
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
