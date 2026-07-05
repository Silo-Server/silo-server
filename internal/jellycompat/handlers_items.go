package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// ItemsHandler serves Jellyfin browse/search/item endpoints.
type ItemsHandler struct {
	content      ContentService
	userData     UserDataService
	codec        *ResourceIDCodec
	mapper       *mapper
	images       *ImageCache
	nextUpRepo   *catalog.NextUpRepository
	browseRepo   *catalog.BrowseRepository
	personRepo   *catalog.PersonRepository
	detailSvc    *catalog.DetailService
	itemRepo     itemRepoForBatchLoader
	episodeRepo  episodeRepoForBatchLoader
	seasonRepo   imageSeasonRepository
	accessFilter AccessFilterResolver
	subtitleRepo subtitles.Repository
	recommender  recommendations.Recommender
	// collections is optional; when set, library collections are exposed as
	// Jellyfin BoxSets. posterPresigner/presignTTL resolve their artwork keys.
	collections collectionSource
	// queryExecutor is optional; when set, smart (live-query) collections
	// resolve their BoxSet children at read time instead of from stored items.
	queryExecutor   smartCollectionQueryExecutor
	posterPresigner LibraryPosterPresigner
	presignTTL      time.Duration
	// FileResolver is optional; when set, /MediaSegments returns real intro/
	// credits/recap/preview segments for any file that has them.
	FileResolver FilePathResolver
	// sectionsFetcher is optional; when set, the Continue Watching (Resume) path
	// is served through the capped native continue-watching fetcher instead of an
	// unbounded progress scan. It is the section subsystem's read-time fetcher and
	// is independent of any virtual-library/hub-section exposure.
	sectionsFetcher *sections.Fetcher
}

// NewItemsHandler creates a new items handler.
func NewItemsHandler(content ContentService, userData UserDataService, codec *ResourceIDCodec, cfg *config.Config, images *ImageCache, nextUpRepo *catalog.NextUpRepository, browseRepo *catalog.BrowseRepository, personRepo *catalog.PersonRepository, detailSvc *catalog.DetailService, itemRepo *catalog.ItemRepository, episodeRepo *catalog.EpisodeRepository, seasonRepo *catalog.SeasonRepository, accessFilter AccessFilterResolver, subtitleRepo subtitles.Repository) *ItemsHandler {
	h := &ItemsHandler{
		content:      content,
		userData:     userData,
		codec:        codec,
		mapper:       newMapper(codec, cfg),
		images:       images,
		nextUpRepo:   nextUpRepo,
		browseRepo:   browseRepo,
		personRepo:   personRepo,
		detailSvc:    detailSvc,
		itemRepo:     itemRepo,
		episodeRepo:  episodeRepo,
		accessFilter: accessFilter,
		subtitleRepo: subtitleRepo,
	}
	// Assign through the typed pointer only when present so the interface field
	// stays a true nil (a typed nil would defeat the nil check in
	// handleSeasonEpisodeChildren and panic on GetByID).
	if seasonRepo != nil {
		h.seasonRepo = seasonRepo
	}
	return h
}

// HandleViews serves GET /Users/{userId}/Views.
func (h *ItemsHandler) HandleViews(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if userID := firstNonEmpty(chi.URLParam(r, "userId"), r.URL.Query().Get("userId")); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	h.handleViewsResponse(w, r, session)
}

// handleViewsResponse returns the user's library views as CollectionFolder items.
func (h *ItemsHandler) handleViewsResponse(w http.ResponseWriter, r *http.Request, session *Session) {
	items, err := h.userViews(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// userViews builds the session's library views as CollectionFolder items. The
// synthetic "Collections" view is prepended (first library) when the session
// can see at least one collection; see collectionsView/collectionsViewVisible.
func (h *ItemsHandler) userViews(ctx context.Context, session *Session) ([]baseItemDTO, error) {
	libraries, err := h.content.ListUserLibraries(ctx, session)
	if err != nil {
		return nil, err
	}

	items := make([]baseItemDTO, 0, len(libraries)+1)
	if h.collectionsViewVisible(ctx, libraries) {
		items = append(items, h.collectionsView())
	}
	for _, library := range libraries {
		dto := h.mapper.viewFromLibrary(library)
		h.rememberLibraryImages(library, dto.ID)
		items = append(items, dto)
	}
	return items, nil
}

// HandleItems serves GET /Items.
func (h *ItemsHandler) HandleItems(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	if userID := firstNonEmpty(chi.URLParam(r, "userId"), chi.URLParam(r, "id")); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	query := parseItemsQuery(r, h.codec)

	// Browsing the synthetic Collections view lists its BoxSets. The view ID is
	// a fixed Jellyfin sentinel (not a codec-encoded ID), so it is matched here
	// at the router rather than decoded in parseItemsQuery. With no
	// parentLibraryID set, handleBoxSetsList lists every visible collection
	// across libraries.
	if isCollectionsViewID(newCaseInsensitiveQuery(r.URL.Query()).Get("ParentId")) {
		// The view's only children are BoxSets. Clients commonly omit
		// IncludeItemTypes when browsing a library by ParentId, so an absent
		// filter still lists BoxSets; but an explicit filter that excludes
		// BoxSet (e.g. IncludeItemTypes=Movie) has no direct children here and
		// returns empty rather than the BoxSet list.
		if query.hasItemTypeFilter && !query.wantsBoxSets {
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
			return
		}
		h.handleBoxSetsList(w, r, session, query)
		return
	}

	switch {
	case len(query.specificIDs) > 0 || len(query.specificCollectionIDs) > 0 || idsRequestCollectionsView(r):
		h.handleSpecificItems(w, r, session, query)
	case query.parentCollectionID != "":
		h.handleBoxSetChildren(w, r, session, query)
	case query.hasItemTypeFilter && len(query.itemTypes) == 0:
		// Every requested type is one catalog browse cannot serve. BoxSet has
		// its own listing (unless a user-state filter applies — collections
		// carry no favorite/played state, so those return empty, matching the
		// pre-existing favorites guard); CollectionFolder means the library
		// views; anything else (Playlist, MusicAlbum, ...) is empty.
		hasUserStateFilter := query.isFavorite || query.isResumable || query.isPlayed != nil
		switch {
		case query.wantsBoxSets && !hasUserStateFilter:
			h.handleBoxSetsList(w, r, session, query)
		case query.wantsViews && !hasUserStateFilter && query.searchTerm == "":
			h.handleViewsResponse(w, r, session)
		default:
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		}
	case query.isResumable:
		h.handleResumeResponse(w, r, session, query)
	case query.isPlayed != nil && *query.isPlayed:
		h.handlePlayedItems(w, r, session, query)
	case query.searchTerm != "":
		h.handleSearchItems(w, r, session, query)
	case query.isFavorite:
		h.handleFavoriteItems(w, r, session, query)
	case query.parentSeasonID != "":
		// ParentId is a season: list that season's episodes. Clients that browse
		// a show through generic /Items?ParentId= (e.g. Void's getEpisodes) send
		// no IncludeItemTypes, so this must not depend on a type filter.
		h.handleSeasonEpisodeChildren(w, r, session, query)
	case query.parentItemID != "":
		// ParentId is a series (or movie): list the series' seasons, or its
		// episodes when IncludeItemTypes=Episode is requested. Without this, a
		// series ParentId fell through to the library-views fallback below, so
		// clients browsing a show via /Items?ParentId= (e.g. Void's getSeasons)
		// saw the top-level libraries rendered as season tabs.
		h.handleItemParentChildren(w, r, session, query)
	case query.parentLibraryID == 0 && len(query.itemTypes) == 0:
		// No ParentId and no type filter: return top-level library views.
		// Jellyfin clients (e.g. Findroid "My Media") call GET /Items?userId=...
		// and expect CollectionFolder items representing the user's libraries.
		h.handleViewsResponse(w, r, session)
	default:
		h.handleBrowseItems(w, r, session, query)
	}
}

// emptyQueryResult is the canonical empty /Items page.
func emptyQueryResult(startIndex int) queryResultDTO {
	return queryResultDTO{
		Items:            []baseItemDTO{},
		TotalRecordCount: 0,
		StartIndex:       startIndex,
	}
}

// handleItemParentChildren serves GET /Items?ParentId={seriesId}. A series parent
// lists the series' seasons by default (and for an explicit IncludeItemTypes=Season);
// IncludeItemTypes=Episode lists every episode of the series instead. A type filter
// a series parent cannot satisfy (e.g. Movie) returns an empty page rather than a
// wrong-typed seasons listing. Movie/other item parents have no seasons, so the
// default path's ListSeasons yields an empty set and writes an empty page.
func (h *ItemsHandler) handleItemParentChildren(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	switch {
	case itemTypesContain(query.itemTypes, "episode"):
		h.writeSeriesEpisodesResponse(w, r, session, query, query.parentItemID, "", true)
	case !query.hasItemTypeFilter || itemTypesContain(query.itemTypes, "season"):
		h.handleSeasonChildItems(w, r, session, query)
	default:
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
	}
}

// handleSeasonEpisodeChildren serves GET /Items?ParentId={seasonId} by listing the
// episodes of that season. The season is resolved to its owning series so the
// shared episode-listing path (also used by /Shows/{id}/Episodes) can be reused.
func (h *ItemsHandler) handleSeasonEpisodeChildren(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	// A season's only children are its episodes; a type filter that excludes
	// Episode (e.g. IncludeItemTypes=Movie) yields nothing, mirroring the
	// series-parent path's handling of unsatisfiable type filters.
	if query.hasItemTypeFilter && !itemTypesContain(query.itemTypes, "episode") {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	if h.seasonRepo == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	season, err := h.seasonRepo.GetByID(r.Context(), query.parentSeasonID)
	if err != nil || season == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	h.writeSeriesEpisodesResponse(w, r, session, query, season.SeriesID, query.parentSeasonID, true)
}

// itemTypesContain reports whether the mapped IncludeItemTypes contain target.
func itemTypesContain(itemTypes []string, target string) bool {
	for _, itemType := range itemTypes {
		if itemType == target {
			return true
		}
	}
	return false
}

func searchItemTypesForQuery(query itemsQuery) []string {
	itemTypes := append([]string(nil), query.itemTypes...)
	if query.mediaTypesExplicit && !query.mediaTypesSet["video"] {
		return []string{compatNoMatchType}
	}
	if query.hasItemTypeFilter && len(itemTypes) == 0 {
		return []string{compatNoMatchType}
	}
	return itemTypes
}

// HandleItem serves GET /Items/{id}.
func (h *ItemsHandler) HandleItem(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if userID := chi.URLParam(r, "userId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	rawID := chi.URLParam(r, "id")

	// The synthetic Collections view is a fixed sentinel ID, not a codec-encoded
	// one; clients fetch the CollectionFolder by ID (e.g. Infuse) before browsing
	// its children, so resolve it before the codec decode attempts.
	if isCollectionsViewID(rawID) {
		writeJSON(w, http.StatusOK, h.collectionsView())
		return
	}

	// Handle library IDs — clients like Infuse request /Items/{id} for
	// CollectionFolder items using the library UUID from /UserViews.
	if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, rawID); err == nil {
		h.handleLibraryItem(w, r, session, int(libraryID))
		return
	}

	if collectionID, err := h.codec.DecodeStringID(EncodedIDCollection, rawID); err == nil {
		h.handleBoxSetItem(w, r, session, collectionID)
		return
	}

	if mediaSourceID, err := h.codec.DecodeIntID(EncodedIDMediaSource, rawID); err == nil {
		contentID, ok := h.codec.LookupMediaSourceOwner(mediaSourceID)
		if !ok {
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		}
		rawID = h.codec.EncodeStringID(EncodedIDItem, contentID)
	}

	if personID, err := h.codec.DecodeIntID(EncodedIDPerson, rawID); err == nil {
		h.handlePersonItem(w, r, session, rawID, personID)
		return
	}

	contentID, err := decodeContentID(h.codec, rawID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberDetailImages(*detail)

	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, []string{detail.ContentID})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	dto := h.mapper.itemFromDetail(*detail, favorites[detail.ContentID], progress[detail.ContentID])
	h.appendDownloadedSubtitlesToDetailDTO(r.Context(), detail.ContentID, detail.Versions, &dto)
	if strings.EqualFold(detail.Type, "series") {
		if seasons, seasonErr := h.content.ListSeasons(r.Context(), session, detail.ContentID, nil); seasonErr == nil {
			browsableSeasons := filterBrowsableSeasons(seasons)
			dto.ChildCount = len(browsableSeasons)
			dto.RecursiveItemCount = len(browsableSeasons)
			dto.SeasonCount = len(browsableSeasons)
		}
	}
	if strings.EqualFold(detail.Type, "episode") && detail.SeriesID != "" {
		seriesImgCache := make(map[string]seriesImageSet)
		h.enrichEpisodeSeriesImages(r.Context(), session, &dto, detail.SeriesID, seriesImgCache)
		if detail.SeasonNumber != nil {
			season, seasonErr := h.content.GetSeason(r.Context(), session, detail.SeriesID, *detail.SeasonNumber, nil)
			if seasonErr == nil && season != nil {
				dto.SeasonID = h.codec.EncodeStringID(EncodedIDSeason, season.ContentID)
				dto.SeasonName = season.Title
				dto.ParentID = dto.SeasonID
			}
		}
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *ItemsHandler) appendDownloadedSubtitlesToDetailDTO(ctx context.Context, contentID string, versions []catalog.FileVersion, dto *baseItemDTO) {
	if h == nil || h.subtitleRepo == nil || dto == nil || len(dto.MediaSources) == 0 || len(versions) == 0 {
		return
	}

	routeItemID := h.codec.EncodeStringID(EncodedIDItem, contentID)
	appendedAny := false

	for i, version := range versions {
		if i >= len(dto.MediaSources) {
			break
		}
		downloaded, err := h.subtitleRepo.ListDownloadedSubtitles(ctx, version.FileID)
		if err != nil || len(downloaded) == 0 {
			continue
		}

		sourceID := h.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))
		baseIndex := nextDownloadedSubtitleIndex(version)
		for j, dl := range downloaded {
			streamIndex := baseIndex + j
			format := subtitleRouteFormat(string(dl.Format))
			displayTitle := downloadedSubtitleDisplayTitle(dl)
			stream := mediaStreamDTO{
				Index:                  streamIndex,
				Type:                   "Subtitle",
				Codec:                  string(dl.Format),
				Language:               dl.Language,
				DisplayTitle:           displayTitle,
				Title:                  displayTitle,
				IsDefault:              false,
				IsExternal:             true,
				IsForced:               false,
				IsHearingImpaired:      dl.HearingImpaired,
				IsTextSubtitleStream:   true,
				SupportsExternalStream: true,
				DeliveryURL:            fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/stream.%s", routeItemID, sourceID, streamIndex, format),
				DeliveryMethod:         "External",
				Path:                   downloadedSubtitlePath(version, dl),
				IsExternalURL:          boolPtr(false),
			}
			dto.MediaSources[i].MediaStreams = append(dto.MediaSources[i].MediaStreams, stream)
			dto.MediaStreams = append(dto.MediaStreams, stream)
			appendedAny = true
		}
	}

	if appendedAny {
		dto.HasSubtitles = true
	}
}

// handlePersonItem serves GET /Items/{id} when the ID decodes as a person.
func (h *ItemsHandler) handlePersonItem(w http.ResponseWriter, r *http.Request, session *Session, routeID string, personID int64) {
	if h.personRepo == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	person, err := h.personRepo.Get(r.Context(), personID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NotFound", "Person not found")
		} else {
			writeCompatUpstreamError(w, err)
		}
		return
	}

	var photoURL string
	if h.detailSvc != nil && person.PhotoPath != "" {
		photoURL = compatPresignImage(h.detailSvc, r.Context(), person.PhotoPath, "poster", compatCardImageSize)
	}

	if photoURL != "" {
		h.images.RememberSized(routeID, "Primary", photoURL, compatCardImageSize)
	}

	dto := baseItemDTO{
		ID:       routeID,
		Name:     person.Name,
		Type:     "Person",
		ServerID: h.mapper.serverID,
		SortName: firstNonEmpty(person.SortName, person.Name),
		Overview: person.Bio,
	}

	if person.BirthDate != nil {
		dto.PremiereDate = person.BirthDate.Format(time.RFC3339)
	}

	providerIDs := map[string]string{}
	var externalURLs []map[string]any
	if person.TmdbID != "" {
		providerIDs["Tmdb"] = person.TmdbID
		externalURLs = append(externalURLs, map[string]any{
			"Name": "TMDB",
			"Url":  "https://www.themoviedb.org/person/" + person.TmdbID,
		})
	}
	if person.ImdbID != "" {
		providerIDs["Imdb"] = person.ImdbID
		externalURLs = append(externalURLs, map[string]any{
			"Name": "IMDb",
			"Url":  "https://www.imdb.com/name/" + person.ImdbID,
		})
	}
	if person.TvdbID != "" {
		providerIDs["Tvdb"] = person.TvdbID
		externalURLs = append(externalURLs, map[string]any{
			"Name": "TheTVDB",
			"Url":  "https://www.thetvdb.com/people/" + person.TvdbID,
		})
	}
	if len(providerIDs) > 0 {
		dto.ProviderIDs = providerIDs
	}
	if len(externalURLs) > 0 {
		dto.ExternalURLs = externalURLs
	}
	if person.Birthplace != "" {
		dto.ProductionLocations = []string{person.Birthplace}
	}

	if photoURL != "" {
		tag := tagValue(photoURL)
		dto.ImageTags = map[string]string{"Primary": tag}
		ratio := 2.0 / 3.0
		dto.PrimaryImageAspectRatio = &ratio
	}

	dto.UserData = &itemUserDataDTO{
		Key:    routeID,
		ItemID: routeID,
	}
	dto.People = []personDTO{}
	dto.Genres = []string{}
	dto.Tags = []string{}
	dto.LockedFields = []string{}
	dto.BackdropImageTags = []string{}

	if counts, err := h.personRepo.CountItemsByType(r.Context(), personID); err != nil {
		slog.Warn("failed to load filmography counts", "person_id", personID, "error", err)
	} else {
		dto.MovieCount = counts["movie"]
		dto.SeriesCount = counts["series"]
		dto.EpisodeCount = counts["episode"]
	}

	writeJSON(w, http.StatusOK, dto)
}

// HandleSimilar serves GET /Items/{id}/Similar, /Movies/{id}/Similar, /Shows/{id}/Similar.
func (h *ItemsHandler) HandleSimilar(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	rawID := chi.URLParam(r, "id")
	contentID, err := h.codec.DecodeStringID(EncodedIDItem, rawID)
	if err != nil {
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0})
		return
	}

	qp := newCaseInsensitiveQuery(r.URL.Query())
	limit := 12
	if v := qp.Get("Limit"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil && n > 0 {
			limit = min(n, 24)
		}
	}

	// Tier 1: embedding-based recommendations.
	if h.recommender != nil {
		scored, recErr := h.recommender.SimilarItems(r.Context(), contentID, limit)
		if recErr == nil && len(scored) > 0 {
			if h.writeSimilarFromScored(w, r, session, scored, limit) {
				return
			}
		}
	}

	// Tier 2: genre-based fallback.
	h.writeSimilarFromGenre(w, r, session, contentID, limit)
}

// writeSimilarFromScored converts recommender ScoredItem results into a Jellyfin query result.
// It returns false when all scored candidates are filtered out so the caller can
// fall back to the genre-based browse path.
func (h *ItemsHandler) writeSimilarFromScored(w http.ResponseWriter, r *http.Request, session *Session, scored []recommendations.ScoredItem, limit int) bool {
	contentIDs := make([]string, 0, len(scored))
	for _, s := range scored {
		contentIDs = append(contentIDs, s.MediaItemID)
	}

	itemsByID, err := h.fetchCompatItemsByContentIDs(r.Context(), session, contentIDs, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0})
		return true
	}

	favorites, progress, _ := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)

	// Build DTOs preserving recommendation order.
	items := make([]baseItemDTO, 0, len(scored))
	listItems := make([]upstreamListItem, 0, len(scored))
	for _, s := range scored {
		li, ok := itemsByID[s.MediaItemID]
		if !ok {
			continue
		}
		dto := h.mapper.itemFromList(li, favorites[s.MediaItemID], progress[s.MediaItemID], nil)
		dto.MediaType = ""
		dto.LocationType = ""
		dto.VideoType = ""
		items = append(items, dto)
		listItems = append(listItems, li)
		if len(items) >= limit {
			break
		}
	}
	h.rememberListImages(listItems)

	if len(items) == 0 {
		return false
	}

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
	return true
}

// writeSimilarFromGenre finds similar items by genre when the recommender is unavailable.
func (h *ItemsHandler) writeSimilarFromGenre(w http.ResponseWriter, r *http.Request, session *Session, contentID string, limit int) {
	empty := queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil || detail == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	genre := ""
	if len(detail.Genres) > 0 {
		genre = strings.TrimSpace(detail.Genres[0])
	}
	if genre == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	params := url.Values{}
	params.Set("type", detail.Type)
	params.Set("genre", genre)
	params.Set("sort", "rating_tmdb")
	params.Set("order", "desc")
	params.Set("limit", strconv.Itoa(limit+1))

	result, err := h.content.BrowseItems(r.Context(), session, params)
	if err != nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	browseIDs := make([]string, len(result.Items))
	for i, li := range result.Items {
		browseIDs[i] = li.ContentID
	}
	favorites, progress, _ := resolveUserStateForContentIDs(r.Context(), session, h.userData, browseIDs)

	items := make([]baseItemDTO, 0, limit)
	listItems := make([]upstreamListItem, 0, limit)
	for _, li := range result.Items {
		if li.ContentID == contentID {
			continue
		}
		dto := h.mapper.itemFromList(li, favorites[li.ContentID], progress[li.ContentID], nil)
		dto.MediaType = ""
		dto.LocationType = ""
		dto.VideoType = ""
		items = append(items, dto)
		listItems = append(listItems, li)
		if len(items) >= limit {
			break
		}
	}
	h.rememberListImages(listItems)

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// HandleItemStub serves stub responses for unimplemented sub-item endpoints
// like /Items/{id}/ThemeMedia, /Items/{id}/SpecialFeatures, /Items/{id}/Intros.
func (h *ItemsHandler) HandleItemStub(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            []baseItemDTO{},
		TotalRecordCount: 0,
		StartIndex:       0,
	})
}

// HandleThemeSongsStub serves an empty ThemeMediaResult for
// /Items/{id}/ThemeSongs. It cannot share HandleItemStub because this
// response shape additionally requires OwnerId (see themeMediaResultDTO).
func (h *ItemsHandler) HandleThemeSongsStub(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, themeMediaResultDTO{
		Items:            []baseItemDTO{},
		TotalRecordCount: 0,
		StartIndex:       0,
		OwnerID:          chi.URLParam(r, "id"),
	})
}

// mediaSegmentDTO mirrors Jellyfin's MediaSegmentDto shape. Times are
// expressed in 100-nanosecond ticks (the convention shared with RunTimeTicks
// and chapter position fields).
type mediaSegmentDTO struct {
	Id         string `json:"Id"`
	ItemId     string `json:"ItemId"`
	Type       string `json:"Type"`
	StartTicks int64  `json:"StartTicks"`
	EndTicks   int64  `json:"EndTicks"`
}

// mediaSegmentsResultDTO is the paged envelope for /MediaSegments responses.
type mediaSegmentsResultDTO struct {
	Items            []mediaSegmentDTO `json:"Items"`
	TotalRecordCount int               `json:"TotalRecordCount"`
	StartIndex       int               `json:"StartIndex"`
}

// HandleMediaSegments returns the intro/credits/recap/preview ranges for an
// item as a Jellyfin MediaSegments payload. Used by Jellyfin clients
// (JellyCon, Findroid, Infuse) to render skip buttons.
func (h *ItemsHandler) HandleMediaSegments(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	raw := chiURLParam(r, "id")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "Missing item id")
		return
	}
	contentID, err := h.codec.DecodeStringID(EncodedIDItem, raw)
	var requestedFileID int
	if err != nil {
		if fileID, fileErr := h.codec.DecodeIntID(EncodedIDMediaSource, raw); fileErr == nil {
			if owner, ok := h.codec.LookupMediaSourceOwner(fileID); ok {
				contentID = owner
				requestedFileID = int(fileID)
			}
		}
	}
	if contentID == "" {
		slog.Debug("jellycompat: media segments lookup with undecodable id", "raw_id", raw)
		writeJSON(w, http.StatusOK, mediaSegmentsResultDTO{Items: []mediaSegmentDTO{}})
		return
	}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		slog.Warn("jellycompat: media segments item lookup failed",
			"content_id", contentID,
			"error", err)
		writeJSON(w, http.StatusOK, mediaSegmentsResultDTO{Items: []mediaSegmentDTO{}})
		return
	}
	if detail == nil {
		writeJSON(w, http.StatusOK, mediaSegmentsResultDTO{Items: []mediaSegmentDTO{}})
		return
	}
	// Versions carry the same Intro/Credits/Recap/Preview that the native API
	// surfaces; the default-playback version owns the segments shown to clients.
	var version *catalog.FileVersion
	if requestedFileID > 0 {
		for i := range detail.Versions {
			if detail.Versions[i].FileID == requestedFileID {
				version = &detail.Versions[i]
				break
			}
		}
	}
	if requestedFileID == 0 {
		for i := range detail.Versions {
			if version == nil || (detail.Versions[i].FileID != 0 && version.FileID == 0) {
				version = &detail.Versions[i]
			}
		}
	}
	if version == nil {
		writeJSON(w, http.StatusOK, mediaSegmentsResultDTO{Items: []mediaSegmentDTO{}})
		return
	}

	segments := buildMediaSegmentDTOs(raw, version)
	writeJSON(w, http.StatusOK, mediaSegmentsResultDTO{
		Items:            segments,
		TotalRecordCount: len(segments),
		StartIndex:       0,
	})
}

// buildMediaSegmentDTOs converts the four optional marker ranges on a file
// version into the flat segment list shape Jellyfin clients expect.
func buildMediaSegmentDTOs(itemUUID string, version *catalog.FileVersion) []mediaSegmentDTO {
	if version == nil {
		return nil
	}
	segments := make([]mediaSegmentDTO, 0, 4)
	add := func(kind string, marker *catalog.Marker) {
		if marker == nil {
			return
		}
		segments = append(segments, mediaSegmentDTO{
			Id:         deriveSegmentID(itemUUID, kind),
			ItemId:     itemUUID,
			Type:       kind,
			StartTicks: secondsToTicks(marker.Start),
			EndTicks:   secondsToTicks(marker.End),
		})
	}
	add("Intro", version.Intro)
	add("Outro", version.Credits)
	add("Recap", version.Recap)
	add("Preview", version.Preview)
	return segments
}

// mediaSegmentIDNamespace is the fixed UUIDv5 namespace under which segment
// IDs are minted. A bespoke namespace ensures these IDs never collide with
// other UUIDs minted elsewhere in the codec, while remaining deterministic
// across processes and restarts.
var mediaSegmentIDNamespace = uuid.MustParse("9f1b2f4a-3c0d-5e16-9a87-2c4f8d0a1d9b")

// deriveSegmentID produces a stable UUID for a (item, kind) pair so repeated
// GETs return the same Id (Jellyfin clients cache by Id).
func deriveSegmentID(itemUUID, kind string) string {
	return uuid.NewSHA1(mediaSegmentIDNamespace, []byte(itemUUID+":"+kind)).String()
}

// HandleGroupingOptionsStub serves GET /UserViews/GroupingOptions with an empty array.
// Jellyfin returns []SpecialViewOptionDto; Silo doesn't support library grouping.
func (h *ItemsHandler) HandleGroupingOptionsStub(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, []struct{}{})
}

// HandleVirtualFolders serves GET /Library/VirtualFolders.
// Returns library metadata so clients like Infuse know library collection types.
func (h *ItemsHandler) HandleVirtualFolders(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	libraries, err := h.content.ListUserLibraries(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	folders := make([]virtualFolderDTO, 0, len(libraries))
	for _, lib := range libraries {
		folders = append(folders, virtualFolderDTO{
			Name:           lib.Name,
			Locations:      []string{},
			CollectionType: libraryCollectionType(lib.Type),
			ItemID:         h.codec.EncodeIntID(EncodedIDLibrary, int64(lib.ID)),
			LibraryOptions: virtualLibraryOptDTO{
				Enabled:                 true,
				EnableRealtimeMonitor:   true,
				EnableInternetProviders: true,
				SeasonZeroDisplayName:   "Specials",
				TypeOptions:             []string{},
			},
		})
	}
	writeJSON(w, http.StatusOK, folders)
}

// HandleFiltersStub serves GET /Items/Filters with empty filter facets.
func (h *ItemsHandler) HandleFiltersStub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string][]string{
		"Genres":          {},
		"Tags":            {},
		"OfficialRatings": {},
		"Years":           {},
	})
}

// HandleFilters2Stub serves GET /Items/Filters2, Jellyfin's v2 query-filters
// endpoint. Clients like Fladder call it to populate their filter UI; without a
// route it fell through to GET /Items/{id} and 404'd. Jellyfin returns a
// QueryFilters object whose fields default to empty arrays, so an empty (but
// correctly-shaped) result is contract-faithful and never blocks the UI.
func (h *ItemsHandler) HandleFilters2Stub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, queryFiltersDTO{
		Genres:            []nameGuidPair{},
		Tags:              []string{},
		AudioLanguages:    []nameValuePair{},
		SubtitleLanguages: []nameValuePair{},
	})
}

// HandleLocalTrailers serves GET /Items/{id}/LocalTrailers (and the legacy
// /Users/{userId}/Items/{id}/LocalTrailers alias). Jellyfin returns a bare
// BaseItemDto array — not the {Items,TotalRecordCount,StartIndex} envelope, so
// this cannot reuse HandleItemStub. Silo does not index local trailer files, so
// the result is always empty; returning [] matches Jellyfin's contract for an
// item with no local trailers and stops the chi 404 that clients (Infuse,
// Moonfin) otherwise hit on every item-detail load.
func (h *ItemsHandler) HandleLocalTrailers(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	writeJSON(w, http.StatusOK, []baseItemDTO{})
}

// HandleLatest serves GET /Items/Latest.
func (h *ItemsHandler) HandleLatest(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if userID := firstNonEmpty(chi.URLParam(r, "userId"), chi.URLParam(r, "id")); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	query := parseItemsQuery(r, h.codec)

	// Jellyfin's /Items/Latest auto-infers item types from the parent
	// library's collection type when no explicit IncludeItemTypes is given.
	// Without this, clients like Infuse may not display "Latest Movies"
	// sections because they rely on the library-type-appropriate item filter.
	// libraryItemType is "movie", "series", or "" (any other/mixed type) — it
	// also decides fast-path eligibility below, so it is resolved once here.
	var libraryItemType string
	if query.parentLibraryID > 0 {
		libraryItemType = h.inferLibraryItemType(r.Context(), session, query.parentLibraryID)
		if len(query.itemTypes) == 0 && libraryItemType != "" {
			query.itemTypes = []string{libraryItemType}
		}
	}

	// Build the fallback's browse params FIRST: fast-path eligibility is
	// decided off these actual params (see latestFastPathEligible), so a filter
	// later added to buildBrowseParams can never be silently ignored by the
	// cached path.
	params := buildLatestBrowseParams(query)

	// Fast path: a per-library Latest is the same user-agnostic list as the native
	// "recently added" library rail (both order by mil.first_seen_at DESC). When
	// eligible, serve it through the native section fetch so it reuses the shared
	// resolved-list cache instead of re-running BrowseItems; otherwise fall back.
	if h.sectionsFetcher != nil && latestFastPathEligible(params, libraryItemType) {
		items, err := h.loadLatestViaSections(r.Context(), session, query)
		if err == nil {
			applyImageTypeLimit(items, query.imageTypeLimit)
			writeJSON(w, http.StatusOK, items)
			return
		}
		// errLatestNotEligible is an expected "this request can't use the cached
		// path" signal (e.g. a client requesting a type other than the library's
		// own), not a failure — fall back quietly.
		if !errors.Is(err, errLatestNotEligible) {
			slog.Warn("jellycompat: latest via sections failed, falling back to browse", "error", err)
		}
	}
	result, err := h.content.BrowseItems(r.Context(), session, params)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	items, err := h.buildLatestItemDTOs(r.Context(), session, query, result.Items)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, items)
}

// buildLatestItemDTOs maps a resolved list of upstream list items to the
// /Items/Latest wire response, applying the per-user overlay (favorites,
// progress, episode targets), the library ParentId, and the optional detail-
// field upgrade. It is the shared tail of both the native section fast path and
// the BrowseItems fallback so the overlay logic lives in exactly one place.
func (h *ItemsHandler) buildLatestItemDTOs(ctx context.Context, session *Session, query itemsQuery, listItems []upstreamListItem) ([]baseItemDTO, error) {
	h.rememberListImages(listItems)

	contentIDs := contentIDsFromListItems(listItems)
	favorites, progress, err := resolveUserStateForContentIDs(ctx, session, h.userData, contentIDs)
	if err != nil {
		return nil, err
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(ctx, session, episodeContentIDsFromListItems(listItems), libraryIDPtr(query.parentLibraryID))
	if err != nil {
		return nil, err
	}

	// Encode library ID for the ParentId field on each item.
	var libraryParentID string
	if query.parentLibraryID > 0 {
		libraryParentID = h.codec.EncodeIntID(EncodedIDLibrary, int64(query.parentLibraryID))
	}

	var detailsByID map[string]*upstreamItemDetail
	if query.needsDetailFields {
		detailsByID = h.batchListItemDetails(ctx, session, contentIDs, libraryIDPtr(query.parentLibraryID))
	}

	items := make([]baseItemDTO, 0, len(listItems))
	for _, item := range listItems {
		if query.needsDetailFields {
			if detail, ok := detailsByID[item.ContentID]; ok && detail != nil {
				h.rememberDetailImages(*detail)
				dto := h.mapper.itemFromDetailWithFields(*detail, favorites[detail.ContentID], progress[detail.ContentID], query.requestedFields)
				if libraryParentID != "" {
					dto.ParentID = libraryParentID
				}
				if target, ok := episodeTargets[detail.ContentID]; ok {
					h.applyCompatEpisodeTarget(&dto, target)
				}
				items = append(items, dto)
				continue
			}
		}

		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		if libraryParentID != "" {
			dto.ParentID = libraryParentID
		}
		if target, ok := episodeTargets[item.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}
	return items, nil
}

// errLatestNotEligible signals that a /Items/Latest request cannot be served
// through the native section path without diverging from the BrowseItems
// fallback, so HandleLatest should fall back quietly rather than log a failure.
var errLatestNotEligible = errors.New("jellycompat: latest not eligible for native section path")

// latestFastPathReproducibleParams is the closed set of browse params the
// synthetic recently-added section reproduces exactly:
//   - limit / offset — offset must be 0 (checked below); limit is applied by
//     slicing the shared list;
//   - type / library_id — expressed by the section config + library scope;
//   - sort / order — buildLatestBrowseParams pins recently_added/desc on both
//     paths;
//   - include_total — Latest returns a bare array, so the total is unused;
//   - max_content_rating — folded into the access filter (and the cache key).
//
// Any OTHER param buildBrowseParams emits — today's filters (genre,
// name_prefix, person_id, is_played, require_backdrop) and any filter added in
// the future — disqualifies the fast path, because the shared cached list
// cannot honor it. Deciding off the actual params (not a hand-mirrored field
// list) is what keeps this gate and the fallback from silently drifting apart.
var latestFastPathReproducibleParams = map[string]struct{}{
	"limit":              {},
	"offset":             {},
	"type":               {},
	"library_id":         {},
	"sort":               {},
	"order":              {},
	"include_total":      {},
	"max_content_rating": {},
}

// latestFastPathEligible reports whether a /Items/Latest request can be served
// through the native recently-added section (and its shared cache) with results
// identical to the BrowseItems fallback. It inspects the browse params the
// fallback would actually receive: every param must be one the section path
// reproduces, the request must be for the first page of a movies/series
// library, the limit must fit the fixed shared fetch budget, and a client
// rating cap must be a known rating (an unknown string matches nothing in
// BrowseItems and must not mint arbitrary cache keys).
func latestFastPathEligible(params url.Values, libraryItemType string) bool {
	if libraryItemType != "movie" && libraryItemType != "series" {
		return false
	}
	for key := range params {
		if _, ok := latestFastPathReproducibleParams[key]; !ok {
			return false
		}
	}
	if params.Get("offset") != "0" {
		return false
	}
	if limit := catalog.ParseIntParam(params.Get("limit")); limit > compatLatestCacheFetchLimit {
		return false
	}
	if rating := strings.TrimSpace(params.Get("max_content_rating")); rating != "" {
		if _, known := access.RatingRank(rating); !known {
			return false
		}
	}
	return true
}

// loadLatestViaSections serves a per-library /Items/Latest through the native
// recently-added section fetch, reusing the shared resolved-list cache. The
// synthetic section carries the same type+config+limit+scope the native library
// rail uses, so with the identity-independent cache key both surfaces collapse
// to one shared entry. The cached *models.MediaItem values are treated
// read-only; LocalizeItemModels deep-copies before any presign mutation.
func (h *ItemsHandler) loadLatestViaSections(ctx context.Context, session *Session, query itemsQuery) ([]baseItemDTO, error) {
	// The caller has already restricted this to movies/series libraries. Pin the
	// exact single type the BrowseItems fallback would use (movie or series) so
	// the two paths return byte-identical membership; if a client asked for some
	// other type against this library, fall back rather than risk a mismatch.
	cfg := latestRecentlyAddedConfig(query.itemTypes)
	if cfg == nil {
		return nil, errLatestNotEligible
	}

	filter := h.resolveAccessFilter(ctx, session)
	// Fold the client's clamped max content rating into the access filter so the
	// shared cached list respects the client's cap AND the cache key captures it
	// — identical to the clamp BrowseItems applies.
	filter.MaxContentRating = clampMaxContentRating(filter.MaxContentRating, query.maxOfficialRating)

	limit := query.limit
	if limit <= 0 {
		limit = compatDefaultBrowseLimit
	}
	// Eligibility already rejected limits beyond the shared fetch budget, but
	// this path must stay safe if called directly.
	if limit > compatLatestCacheFetchLimit {
		return nil, errLatestNotEligible
	}
	libraryID := query.parentLibraryID
	// Always fetch the FIXED compatLatestCacheFetchLimit budget and slice down
	// to the requested page below. ItemLimit is a cache-key component, so
	// echoing the raw client Limit would let one client mint a distinct
	// process-global cache entry per Limit value; with the fixed budget every
	// compat Latest request for a scope+library shares exactly one entry.
	resolved := sections.ResolvedSection{
		ID:          "compat-latest",
		SectionType: sections.SectionRecentlyAdded,
		Title:       "Latest",
		ItemLimit:   compatLatestCacheFetchLimit,
		Config:      cfg,
	}

	withItems, err := h.sectionsFetcher.FetchOne(ctx, resolved, &libraryID, nil, session.StreamAppUserID, session.ProfileID, filter)
	if err != nil {
		return nil, err
	}

	// The shared list is ordered by first_seen_at DESC, so its first N entries
	// are exactly what a direct limit-N fetch would return.
	sharedItems := withItems.Items
	if len(sharedItems) > limit {
		sharedItems = sharedItems[:limit]
	}

	listItems := h.compatListItemsFromModels(ctx, filter, sharedItems)
	// The BrowseItems fallback aggregates series watch state via
	// EnrichSeriesUserData; a series has no progress row of its own, so without
	// this its Latest rail would drop Played/UnplayedItemCount and diverge from
	// page 2. Enrich the freshly-built per-request list items (never the cached
	// models) before the DTOs are built so userDataDTO sees the populated
	// UserData. It runs under the request session, so counts are per-profile, and
	// only touches series rows (movies are left untouched).
	h.content.EnrichSeriesUserData(ctx, session, listItems)
	return h.buildLatestItemDTOs(ctx, session, query, listItems)
}

// compatLatestCacheFetchLimit is the fixed number of rows the Latest fast path
// asks the shared recently-added cache for, regardless of the client's Limit
// (the response is sliced down afterwards). Fixing the fetch size keeps the
// client's Limit out of the cache key — one entry per scope+library instead of
// one per distinct Limit value — and comfortably covers the Latest hot path
// (clients request ~16-50); larger limits fall back to BrowseItems.
const compatLatestCacheFetchLimit = 100

// latestRecentlyAddedConfig encodes the inferred item-type filter into the
// recently-added section Config (the filter_type field buildRecentlyAddedQuery
// reads). It normalizes through compatScopedTypes — the SAME helper BrowseItems
// uses — and only encodes a concrete single type (movie or series); an empty or
// mixed library yields a nil config (all types).
func latestRecentlyAddedConfig(itemTypes []string) json.RawMessage {
	scoped := compatScopedTypes(strings.Join(itemTypes, ","))
	var filterType string
	switch scoped {
	case "movie", "series":
		filterType = scoped
	default:
		return nil
	}
	cfg, err := json.Marshal(sections.SectionConfigFilters{FilterType: filterType})
	if err != nil {
		return nil
	}
	return cfg
}

// compatListItemsFromModels localizes cached media-item models, converts them to
// the compat list-item wire shape, and presigns their image URLs. The cached
// models are never mutated in place: LocalizeItemModels deep-copies, and the
// per-item presign operates on the freshly-built upstreamListItem values.
func (h *ItemsHandler) compatListItemsFromModels(ctx context.Context, filter catalog.AccessFilter, items []*models.MediaItem) []upstreamListItem {
	localized := items
	if h.detailSvc != nil {
		if loc, err := h.detailSvc.LocalizeItemModels(ctx, items, filter); err == nil && loc != nil {
			localized = loc
		}
	}
	listItems := make([]upstreamListItem, 0, len(localized))
	for _, mi := range localized {
		if mi == nil {
			continue
		}
		listItems = append(listItems, mediaItemToListItem(mi))
	}
	presignCompatListItems(ctx, h.detailSvc, listItems)
	return listItems
}

// HandleSuggestions serves GET /Items/Suggestions.
func (h *ItemsHandler) HandleSuggestions(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	query := parseSuggestionsQuery(r, h.codec)
	params := buildBrowseParams(query)
	params.Set("sort", "rating_imdb")
	params.Set("order", "desc")
	result, err := h.content.BrowseItems(r.Context(), session, params)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberListImages(result.Items)

	contentIDs := contentIDsFromListItems(result.Items)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, episodeContentIDsFromListItems(result.Items), nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], nil)
		if target, ok := episodeTargets[item.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// HandleGenres serves GET /Genres.
func (h *ItemsHandler) HandleGenres(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if userID := r.URL.Query().Get("UserId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	params := urlValuesFromItemsQuery(parseItemsQuery(r, h.codec))
	filters, err := h.content.ListItemFilters(r.Context(), session, params)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	items := make([]baseItemDTO, 0, len(filters.Genres))
	for _, genre := range filters.Genres {
		if strings.TrimSpace(genre) == "" {
			continue
		}
		items = append(items, baseItemDTO{
			ID:   h.codec.EncodeStringID(EncodedIDGenre, genre),
			Type: "Genre",
			Name: genre,
		})
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// HandleGenreByName serves GET /Genres/{name}. Jellyfin addresses genres by
// (URL-escaped) display name; clients use the returned Id for GenreIds= item
// queries.
func (h *ItemsHandler) HandleGenreByName(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if decoded, err := url.PathUnescape(name); err == nil {
		name = strings.TrimSpace(decoded)
	}
	if name == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Genre not found")
		return
	}

	// Confirm the genre exists within the caller's visible scope so the
	// response carries the canonical casing.
	filters, err := h.content.ListItemFilters(r.Context(), session, urlValuesFromItemsQuery(parseItemsQuery(r, h.codec)))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	for _, genre := range filters.Genres {
		if strings.EqualFold(strings.TrimSpace(genre), name) {
			writeJSON(w, http.StatusOK, baseItemDTO{
				ID:       h.codec.EncodeStringID(EncodedIDGenre, genre),
				Type:     "Genre",
				Name:     genre,
				ServerID: h.mapper.serverID,
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "NotFound", "Genre not found")
}

// HandleSeasons serves GET /Shows/{id}/Seasons.
func (h *ItemsHandler) HandleSeasons(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rv := recover(); rv != nil {
			slog.Error("jellycompat HandleSeasons panic",
				"error", fmt.Sprint(rv),
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"stack", string(debug.Stack()),
			)
			writeError(w, http.StatusInternalServerError, "ServerError", "Internal error")
		}
	}()

	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	query := parseItemsQuery(r, h.codec)

	seriesID, err := h.codec.DecodeStringID(EncodedIDItem, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Series not found")
		return
	}

	seasons, err := h.content.ListSeasons(r.Context(), session, seriesID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.writeSeasonItemsResponse(w, r, session, seriesID, seasons, query, false)
}

func (h *ItemsHandler) handleSeasonChildItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	seasons, err := h.content.ListSeasons(r.Context(), session, query.parentItemID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.writeSeasonItemsResponse(w, r, session, query.parentItemID, seasons, query, true)
}

func (h *ItemsHandler) writeSeasonItemsResponse(w http.ResponseWriter, r *http.Request, session *Session, seriesID string, seasons []upstreamSeason, query itemsQuery, page bool) {
	seasons = filterBrowsableSeasons(seasons)
	h.rememberSeasonImages(seasons, seriesID)

	total := len(seasons)
	if page {
		start := query.startIndex
		if start > total {
			start = total
		}
		end := total
		if query.limit > 0 && start+query.limit < end {
			end = start + query.limit
		}
		seasons = seasons[start:end]
	}

	favorites, err := resolveFavoritesForContentIDs(r.Context(), session, h.userData, seasonContentIDs(seasons))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items := make([]baseItemDTO, 0, len(seasons))
	for _, season := range seasons {
		if query.needsDetailFields {
			detail, detailErr := h.content.GetItemDetail(r.Context(), session, season.ContentID, nil)
			if detailErr == nil {
				h.rememberDetailImages(*detail)
				dto := h.mapper.itemFromDetailWithFields(*detail, favorites[season.ContentID], nil, query.requestedFields)
				dto.ParentID = h.codec.EncodeStringID(EncodedIDItem, seriesID)
				dto.SeriesID = h.codec.EncodeStringID(EncodedIDItem, seriesID)
				dto.IndexNumber = &season.SeasonNumber
				dto.ParentIndexNumber = nil
				items = append(items, dto)
				continue
			}
		}
		items = append(items, h.mapper.seasonFromUpstream(season, seriesID, favorites[season.ContentID]))
	}
	applyImageTypeLimit(items, query.imageTypeLimit)

	startIndex := 0
	if page {
		startIndex = query.startIndex
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       startIndex,
	})
}

func filterBrowsableSeasons(seasons []upstreamSeason) []upstreamSeason {
	filtered := make([]upstreamSeason, 0, len(seasons))
	for _, season := range seasons {
		if season.EpisodeCount <= 0 {
			continue
		}
		filtered = append(filtered, season)
	}
	return filtered
}

// HandleEpisodes serves GET /Shows/{id}/Episodes.
func (h *ItemsHandler) HandleEpisodes(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	query := parseItemsQuery(r, h.codec)

	seriesID, err := h.codec.DecodeStringID(EncodedIDItem, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Series not found")
		return
	}

	// AdjacentTo (Wholphin autoplay/skip) only needs the requested episode plus
	// its immediate neighbors. Serve it from a bounded index seek instead of
	// materializing the whole series, which is multi-second for long soaps.
	if query.adjacentTo != "" {
		h.writeAdjacentEpisodesResponse(w, r, session, query, seriesID)
		return
	}

	var requestedSeasonID string
	if rawSeasonID := strings.TrimSpace(newCaseInsensitiveQuery(r.URL.Query()).Get("SeasonId")); rawSeasonID != "" {
		decodedSeasonID, decodeErr := h.codec.DecodeStringID(EncodedIDSeason, rawSeasonID)
		if decodeErr != nil {
			writeError(w, http.StatusNotFound, "NotFound", "Season not found")
			return
		}
		requestedSeasonID = decodedSeasonID
	}

	h.writeSeriesEpisodesResponse(w, r, session, query, seriesID, requestedSeasonID, false)
}

// writeSeriesEpisodesResponse lists a series' episodes (optionally scoped to a
// single season via requestedSeasonID) and writes them as a Jellyfin /Items page.
// It is the shared core of GET /Shows/{id}/Episodes and the generic
// /Items?ParentId= series/season browse paths. page applies StartIndex/Limit to
// the result: the generic /Items browse paths page (matching the /Items contract
// and the sibling seasons listing); /Shows/{id}/Episodes passes false to keep its
// long-standing whole-season response.
func (h *ItemsHandler) writeSeriesEpisodesResponse(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, seriesID, requestedSeasonID string, page bool) {
	seasons, err := h.content.ListSeasons(r.Context(), session, seriesID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	episodeModels, err := h.listSeriesEpisodes(r.Context(), session, seriesID, seasons, requestedSeasonID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.writeEpisodeModelsPage(w, r, session, query, seriesID, seasons, episodeModels, page)
}

// writeEpisodeModelsPage maps a resolved set of episode models to Jellyfin
// baseItemDTOs and writes them as an /Items page. It is the shared downstream
// half of the episode-listing paths: writeSeriesEpisodesResponse feeds it the
// whole series (or a single season), while writeAdjacentEpisodesResponse feeds
// it only the bounded prev/self/next window. seasons is used solely for season
// title/ID hydration; episodeModels carries the rows actually rendered.
func (h *ItemsHandler) writeEpisodeModelsPage(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, seriesID string, seasons []upstreamSeason, episodeModels []*models.Episode, page bool) {
	h.rememberSeasonImages(seasons, seriesID)

	seasonTitleByID := make(map[string]string, len(seasons))
	seasonTitleByNumber := make(map[int]string, len(seasons))
	seasonIDByNumber := make(map[int]string, len(seasons))
	for _, season := range seasons {
		seasonTitleByID[season.ContentID] = season.Title
		seasonTitleByNumber[season.SeasonNumber] = season.Title
		seasonIDByNumber[season.SeasonNumber] = season.ContentID
	}

	sort.SliceStable(episodeModels, func(i, j int) bool {
		if episodeModels[i] == nil || episodeModels[j] == nil {
			return episodeModels[i] != nil
		}
		if episodeModels[i].SeasonNumber == episodeModels[j].SeasonNumber {
			return episodeModels[i].EpisodeNumber < episodeModels[j].EpisodeNumber
		}
		return episodeModels[i].SeasonNumber < episodeModels[j].SeasonNumber
	})
	episodeModels = compactEpisodeModels(episodeModels)
	episodeModels = trimEpisodesFromStartItem(episodeModels, query.startItemID, h.codec)

	contentIDs := contentIDsFromEpisodes(episodeModels)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, contentIDs, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	// When detail-level fields are requested (Fields=MediaSources, MediaStreams,
	// Chapters, People), fetch every episode's detail in a single batched call
	// via the catalog's hoisted path, instead of fanning out one
	// GetItemDetail per episode. This eliminates ~5×N redundant series-level
	// lookups (parent series row, localization, credits, version preference,
	// backdrop presign) for an N-episode series.
	var episodeDetails map[string]*upstreamItemDetail
	if query.needsDetailFields && h.detailSvc != nil {
		filter := h.resolveAccessFilter(r.Context(), session)
		details, detailErr := h.detailSvc.GetEpisodeDetailsForSeries(r.Context(), seriesID, contentIDs, filter)
		if detailErr != nil {
			writeCompatUpstreamError(w, detailErr)
			return
		}
		episodeDetails = make(map[string]*upstreamItemDetail, len(details))
		for contentID, detail := range details {
			upstream := itemDetailToUpstream(detail)
			episodeDetails[contentID] = &upstream
		}
	}

	items := make([]baseItemDTO, 0, len(episodeModels))
	for _, episode := range episodeModels {
		if query.needsDetailFields {
			if detail, ok := episodeDetails[episode.ContentID]; ok && detail != nil {
				h.rememberDetailImages(*detail)
				dto := h.mapper.itemFromDetailWithFields(*detail, favorites[episode.ContentID], progress[episode.ContentID], query.requestedFields)
				dto.SeasonName = firstNonEmpty(seasonTitleByID[episode.SeasonID], seasonTitleByNumber[episode.SeasonNumber])
				if dto.SeasonID == "" {
					if seasonID := firstNonEmpty(episode.SeasonID, seasonIDByNumber[episode.SeasonNumber]); seasonID != "" {
						dto.SeasonID = h.codec.EncodeStringID(EncodedIDSeason, seasonID)
					}
				}
				if dto.ParentID == "" {
					dto.ParentID = dto.SeasonID
				}
				if dto.SeriesID == "" {
					dto.SeriesID = h.codec.EncodeStringID(EncodedIDItem, seriesID)
				}
				if target, ok := episodeTargets[episode.ContentID]; ok {
					h.applyCompatEpisodeTarget(&dto, target)
				}
				items = append(items, dto)
				continue
			}
		}

		upstreamEpisode := modelEpisodeToUpstream(episode, seriesID)
		if target, ok := episodeTargets[episode.ContentID]; ok {
			upstreamEpisode.StillURL = firstNonEmpty(target.Item.StillURL, target.Item.PosterURL, upstreamEpisode.StillURL)
			upstreamEpisode.SeriesTitle = firstNonEmpty(target.Item.SeriesTitle, upstreamEpisode.SeriesTitle)
			upstreamEpisode.HasMediaFiles = target.Item.HasMediaFiles
		}
		dto := h.mapper.episodeFromUpstream(upstreamEpisode, favorites[episode.ContentID], progress[episode.ContentID])
		dto.SeasonName = firstNonEmpty(seasonTitleByID[episode.SeasonID], seasonTitleByNumber[episode.SeasonNumber])
		if dto.SeasonID == "" {
			if seasonID := seasonIDByNumber[episode.SeasonNumber]; seasonID != "" {
				dto.SeasonID = h.codec.EncodeStringID(EncodedIDSeason, seasonID)
			}
		}
		dto.ParentID = dto.SeasonID
		dto.SeriesID = h.codec.EncodeStringID(EncodedIDItem, seriesID)
		if target, ok := episodeTargets[episode.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}

	sort.SliceStable(items, func(i, j int) bool {
		leftSeason := 0
		rightSeason := 0
		if items[i].ParentIndexNumber != nil {
			leftSeason = *items[i].ParentIndexNumber
		}
		if items[j].ParentIndexNumber != nil {
			rightSeason = *items[j].ParentIndexNumber
		}
		if leftSeason == rightSeason {
			leftEpisode := 0
			rightEpisode := 0
			if items[i].IndexNumber != nil {
				leftEpisode = *items[i].IndexNumber
			}
			if items[j].IndexNumber != nil {
				rightEpisode = *items[j].IndexNumber
			}
			return leftEpisode < rightEpisode
		}
		return leftSeason < rightSeason
	})

	total := len(items)
	startIndex := 0
	if page {
		startIndex = query.startIndex
		items = slicePage(items, query.startIndex, query.limit)
		if items == nil {
			items = []baseItemDTO{}
		}
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       startIndex,
	})
}

// writeAdjacentEpisodesResponse serves the AdjacentTo case of
// GET /Shows/{id}/Episodes: it resolves the referenced episode and returns it
// together with its immediate previous/next neighbors (at most three items,
// crossing season boundaries) via a bounded index seek. This intentionally
// returns a prev/self/next window rather than Jellyfin's exact AdjacentTo
// shape — it satisfies Wholphin's autoplay/skip use without paying the cost of
// loading and mapping every episode of the series.
//
// When the bounded repo path is unavailable (no direct episode repo) or the
// AdjacentTo id cannot be decoded/resolved, it falls back to the full-series
// listing so behavior is never worse than before AdjacentTo was honored.
func (h *ItemsHandler) writeAdjacentEpisodesResponse(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, seriesID string) {
	if h.episodeRepo == nil {
		h.writeSeriesEpisodesResponse(w, r, session, query, seriesID, "", false)
		return
	}

	targetContentID, err := decodeItemID(h.codec, query.adjacentTo)
	if err != nil || targetContentID == "" {
		h.writeSeriesEpisodesResponse(w, r, session, query, seriesID, "", false)
		return
	}

	targets, err := h.episodeRepo.GetByIDs(r.Context(), []string{targetContentID})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	var target *models.Episode
	for _, ep := range targets {
		if ep != nil && ep.ContentID == targetContentID {
			target = ep
			break
		}
	}
	if target == nil {
		// Unknown episode: return an empty page rather than the whole series.
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: 0})
		return
	}
	if target.SeriesID != seriesID {
		// Malformed request: the AdjacentTo episode belongs to a different series
		// than the {id} path. Returning that other series' neighbors mislabeled
		// with this series' ID would be silently wrong, so return an empty page.
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: 0})
		return
	}

	episodeModels, err := h.episodeRepo.ListAdjacentInSeries(r.Context(), target.SeriesID, target.SeasonNumber, target.EpisodeNumber)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	seasons, err := h.content.ListSeasons(r.Context(), session, seriesID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	h.writeEpisodeModelsPage(w, r, session, query, seriesID, seasons, episodeModels, false)
}

// HandleNextUp serves GET /Shows/NextUp.
func (h *ItemsHandler) HandleNextUp(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	qp := newCaseInsensitiveQuery(r.URL.Query())
	if userID := qp.Get("UserId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	query := parseItemsQuery(r, h.codec)

	q := catalog.NextUpQuery{
		UserID:          session.StreamAppUserID,
		ProfileID:       session.ProfileID,
		Limit:           query.limit + query.startIndex, // fetch enough to paginate
		EnableResumable: parseBool(qp.Get("enableResumable"), false),
	}

	// Parse SeriesId filter
	if rawSeriesID := strings.TrimSpace(qp.Get("SeriesId")); rawSeriesID != "" {
		seriesID, err := decodeItemID(h.codec, rawSeriesID)
		if err != nil {
			writeError(w, http.StatusNotFound, "NotFound", "Series not found")
			return
		}
		q.SeriesID = seriesID
	}

	// Parse date cutoff
	if rawCutoff := qp.Get("nextUpDateCutoff"); rawCutoff != "" {
		if t, err := time.Parse(time.RFC3339, rawCutoff); err == nil {
			q.DateCutoff = &t
		}
	}

	results, err := h.nextUpRepo.ListNextUp(r.Context(), q)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	h.writeNextUpResponse(w, r, session, results, query)
}

// writeNextUpResponse renders a slice of catalog.NextUpResult into the
// shared NextUp/Upcoming response shape. Used by HandleNextUp and
// HandleUpcoming so the two endpoints stay in lockstep.
func (h *ItemsHandler) writeNextUpResponse(w http.ResponseWriter, r *http.Request, session *Session, results []catalog.NextUpResult, query itemsQuery) {
	contentIDs := contentIDsFromNextUpResults(results)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, contentIDs, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	// The scan stays on list-level data (per-item GetItemDetail fanout over
	// the FULL next-up list was 5N DB roundtrips for users with long lists);
	// only the sliced page below is upgraded to detail when the client asked
	// for detail-level Fields — see upgradeProgressPageToDetail for why the
	// listing's MediaSources are load-bearing for Infuse/SenPlayer.
	pageIDs := make([]string, 0, len(results))
	items := make([]baseItemDTO, 0, len(results))
	for _, res := range results {
		target, ok := episodeTargets[res.ContentID]
		if !ok {
			continue
		}
		dto := h.mapper.itemFromList(target.Item, favorites[res.ContentID], progress[res.ContentID], query.requestedFields)
		h.applyCompatEpisodeTarget(&dto, target)
		stubDetailListFields(&dto, query.requestedFields)
		pageIDs = append(pageIDs, res.ContentID)
		items = append(items, dto)
	}

	total := len(items)
	items = sliceBaseItems(items, query.startIndex, query.limit)
	pageIDs = slicePageIDs(pageIDs, query.startIndex, query.limit)
	if query.needsDetailFields {
		for i := range items {
			if i >= maxDetailUpgrades || i >= len(pageIDs) {
				break
			}
			detail, detailErr := h.content.GetItemDetail(r.Context(), session, pageIDs[i], nil)
			if detailErr != nil {
				continue
			}
			h.rememberDetailImages(*detail)
			dto := h.mapper.itemFromDetailWithFields(*detail, favorites[pageIDs[i]], progress[pageIDs[i]], query.requestedFields)
			if target, ok := episodeTargets[pageIDs[i]]; ok {
				h.applyCompatEpisodeTarget(&dto, target)
			}
			items[i] = dto
		}
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

// slicePageIDs applies sliceBaseItems' bounds to the parallel content-id
// slice so page positions stay aligned after StartIndex/Limit slicing.
func slicePageIDs(ids []string, startIndex, limit int) []string {
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex >= len(ids) {
		return []string{}
	}
	if limit <= 0 {
		limit = len(ids)
	}
	end := min(startIndex+limit, len(ids))
	return ids[startIndex:end]
}

// HandleUpcoming serves GET /Shows/Upcoming.
//
// Android TV's show-detail page calls this endpoint to populate a single
// "Upcoming" tile scoped to the currently open series. Without it the
// client falls back to global /Shows/NextUp and leaks unrelated shows
// onto the page. We model the response as NextUp scoped to a SeriesId
// (in-progress episode → next aired → next season's episode 1).
//
// SeriesId/ParentId is required, but we return 200 with an empty result
// instead of 404 when it's missing — a 404 here re-triggers the
// Android TV fallback we are trying to suppress.
func (h *ItemsHandler) HandleUpcoming(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	qp := newCaseInsensitiveQuery(r.URL.Query())
	if userID := qp.Get("UserId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}

	query := parseItemsQuery(r, h.codec)
	if query.limit <= 0 {
		query.limit = 1
	}

	rawSeriesID := strings.TrimSpace(qp.Get("SeriesId"))
	if rawSeriesID == "" {
		rawSeriesID = strings.TrimSpace(qp.Get("ParentId"))
	}
	if rawSeriesID == "" {
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: query.startIndex})
		return
	}
	seriesID, err := decodeItemID(h.codec, rawSeriesID)
	if err != nil {
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: query.startIndex})
		return
	}

	q := catalog.NextUpQuery{
		UserID:          session.StreamAppUserID,
		ProfileID:       session.ProfileID,
		SeriesID:        seriesID,
		Limit:           query.limit + query.startIndex,
		EnableResumable: parseBool(qp.Get("enableResumable"), true),
	}

	results, err := h.nextUpRepo.ListNextUp(r.Context(), q)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	h.writeNextUpResponse(w, r, session, results, query)
}

// HandleResume serves GET /UserItems/Resume.
func (h *ItemsHandler) HandleResume(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	query := parseItemsQuery(r, h.codec)
	h.handleResumeResponse(w, r, session, query)
}

// HandleSearchHints serves GET /Search/Hints.
func (h *ItemsHandler) HandleSearchHints(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	q := newCaseInsensitiveQuery(r.URL.Query())
	query := strings.TrimSpace(q.Get("SearchTerm"))
	// Search hints are served by the catalog search provider (the same
	// Meilisearch-backed path as /Items media search), which does its own
	// short-term handling and result bounding, so the aux short-term gate is
	// intentionally NOT applied here — short titles ("Up", "It") stay
	// discoverable through type-ahead. The result cap is still clamped below.
	if query == "" {
		writeJSON(w, http.StatusOK, searchHintResultDTO{})
		return
	}

	limit := clampAuxSearchLimit(parsePositiveInt(q.Get("Limit"), auxSearchMaxResults))
	result, err := h.content.SearchItems(r.Context(), session, SearchItemsOptions{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberListImages(result.Items)

	hints := make([]searchHintDTO, 0, len(result.Items))
	for _, item := range result.Items {
		id := h.mapper.itemFromList(item, false, nil, nil).ID
		hints = append(hints, searchHintDTO{
			ItemID:           id,
			ID:               id,
			Name:             item.Title,
			Type:             jellyfinItemType(item.Type),
			ProductionYear:   item.Year,
			RunTimeTicks:     minutesToTicks(item.Runtime),
			PrimaryImageTag:  tagValue(item.PosterURL),
			BackdropImageTag: tagValue(item.BackdropURL),
			Series:           item.SeriesTitle,
			Genres:           item.Genres,
		})
	}
	writeJSON(w, http.StatusOK, searchHintResultDTO{
		SearchHints:      hints,
		TotalRecordCount: result.Total,
	})
}

func (h *ItemsHandler) handleLibraryItem(w http.ResponseWriter, r *http.Request, session *Session, libraryID int) {
	libraries, err := h.content.ListUserLibraries(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	for _, library := range libraries {
		if library.ID == libraryID {
			dto := h.mapper.viewFromLibrary(library)
			h.rememberLibraryImages(library, dto.ID)
			writeJSON(w, http.StatusOK, dto)
			return
		}
	}
	writeError(w, http.StatusNotFound, "NotFound", "Item not found")
}

// batchListItemDetails resolves detail payloads for a page of content IDs using
// the batched content path, falling back to per-item GetItemDetail only if the
// batch call itself errors. The returned map is keyed by content ID; ids absent
// from it could not be resolved to a detail and must be rendered from list data
// by the caller — matching the historical per-item GetItemDetail error → list
// fallback behavior. Individually-unresolvable ids (e.g. season rows, which the
// item-detail batch path does not handle) are simply absent and likewise fall
// back to list rendering rather than a per-item detail fetch. Returns nil for an
// empty input.
func (h *ItemsHandler) batchListItemDetails(ctx context.Context, session *Session, contentIDs []string, libraryID *int) map[string]*upstreamItemDetail {
	if len(contentIDs) == 0 {
		return nil
	}
	if details, err := h.content.GetItemDetailsByIDs(ctx, session, contentIDs, libraryID); err == nil {
		return details
	}
	details := make(map[string]*upstreamItemDetail, len(contentIDs))
	for _, id := range contentIDs {
		detail, derr := h.content.GetItemDetail(ctx, session, id, libraryID)
		if derr != nil || detail == nil {
			continue
		}
		details[id] = detail
	}
	return details
}

func (h *ItemsHandler) handleBrowseItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	result, err := h.content.BrowseItems(r.Context(), session, buildBrowseParams(query))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberListImages(result.Items)

	contentIDs := contentIDsFromListItems(result.Items)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, episodeContentIDsFromListItems(result.Items), libraryIDPtr(query.parentLibraryID))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	// Encode library ID for the ParentId field on each item.
	var libraryParentID string
	if query.parentLibraryID > 0 {
		libraryParentID = h.codec.EncodeIntID(EncodedIDLibrary, int64(query.parentLibraryID))
	}

	var detailsByID map[string]*upstreamItemDetail
	if query.needsDetailFields {
		detailsByID = h.batchListItemDetails(r.Context(), session, contentIDs, libraryIDPtr(query.parentLibraryID))
	}

	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		if query.needsDetailFields {
			if detail, ok := detailsByID[item.ContentID]; ok && detail != nil {
				h.rememberDetailImages(*detail)
				dto := h.mapper.itemFromDetailWithFields(*detail, favorites[detail.ContentID], progress[detail.ContentID], query.requestedFields)
				if libraryParentID != "" {
					dto.ParentID = libraryParentID
				}
				if target, ok := episodeTargets[detail.ContentID]; ok {
					h.applyCompatEpisodeTarget(&dto, target)
				}
				items = append(items, dto)
				continue
			}
		}
		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		if libraryParentID != "" {
			dto.ParentID = libraryParentID
		}
		if target, ok := episodeTargets[item.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: result.Total,
		StartIndex:       query.startIndex,
	})
}

func (h *ItemsHandler) handleFavoriteItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	// Client requested specific types (e.g. Playlist, BoxSet, Video) that all
	// mapped to nothing — return empty rather than returning all favorites.
	if query.hasItemTypeFilter && len(query.itemTypes) == 0 {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            []baseItemDTO{},
			TotalRecordCount: 0,
			StartIndex:       query.startIndex,
		})
		return
	}

	// Push the supported browse filters (type, library, sort, genre, name
	// prefix, max content rating) into a single SQL query that JOINs
	// user_favorites with media_items. This replaces the legacy fetch-all-
	// then-filter path that pulled up to 10,000 favorites into memory before
	// applying browse filters (audit 2026-05-01 §3.6 / catalog SQL plan
	// task 4.2).
	if favoriteItemsNeedBrowseFilters(query) && h.browseRepo != nil && favoriteBrowseFiltersSupportedBySQL(query) {
		access := h.resolveAccessFilter(r.Context(), session)
		filters := catalog.BrowseFavoritesFilters{
			UserID:             session.StreamAppUserID,
			ProfileID:          session.ProfileID,
			ItemType:           strings.Join(query.itemTypes, ","),
			Genre:              query.genreName,
			NamePrefix:         query.namePrefix,
			LibraryID:          query.parentLibraryID,
			AllowedLibraryIDs:  access.AllowedLibraryIDs,
			DisabledLibraryIDs: access.DisabledLibraryIDs,
			MaxContentRating:   clampMaxContentRating(access.MaxContentRating, query.maxOfficialRating),
			ExcludedMediaTypes: access.ExcludedMediaTypes,
			SortField:          query.sort,
			SortOrder:          query.order,
			Limit:              query.limit,
			Offset:             query.startIndex,
		}
		result, err := h.browseRepo.BrowseFavorites(r.Context(), filters)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}

		listItems := make([]upstreamListItem, 0, len(result.Items))
		for _, mi := range result.Items {
			listItems = append(listItems, mediaItemToListItem(mi))
		}
		presignCompatListItems(r.Context(), h.detailSvc, listItems)
		h.rememberListImages(listItems)

		progress, progressErr := resolveProgressForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(listItems))
		if progressErr != nil {
			writeCompatUpstreamError(w, progressErr)
			return
		}
		items := make([]baseItemDTO, 0, len(listItems))
		for _, item := range listItems {
			items = append(items, h.mapper.itemFromList(item, true, progress[item.ContentID], query.requestedFields))
		}
		applyImageTypeLimit(items, query.imageTypeLimit)
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            items,
			TotalRecordCount: result.Total,
			StartIndex:       query.startIndex,
		})
		return
	}

	favoriteLimit := max(query.limit+query.startIndex, 200)
	if favoriteItemsNeedBrowseFilters(query) {
		favoriteLimit = 10000
	}
	favoriteItems, err := h.userData.ListFavorites(r.Context(), session, favoriteLimit, 0)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	if favoriteItemsNeedBrowseFilters(query) {
		contentIDs := contentIDsFromListItems(favoriteItems)
		if len(contentIDs) == 0 {
			writeJSON(w, http.StatusOK, queryResultDTO{
				Items:            []baseItemDTO{},
				TotalRecordCount: 0,
				StartIndex:       query.startIndex,
			})
			return
		}
		params := buildBrowseParams(query)
		params.Del("offset")
		params.Set("offset", strconv.Itoa(query.startIndex))
		params.Set("content_ids", strings.Join(contentIDs, ","))
		result, browseErr := h.content.BrowseItems(r.Context(), session, params)
		if browseErr != nil {
			writeCompatUpstreamError(w, browseErr)
			return
		}
		h.rememberListImages(result.Items)

		progress, progressErr := resolveProgressForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(result.Items))
		if progressErr != nil {
			writeCompatUpstreamError(w, progressErr)
			return
		}
		items := make([]baseItemDTO, 0, len(result.Items))
		for _, item := range result.Items {
			items = append(items, h.mapper.itemFromList(item, true, progress[item.ContentID], query.requestedFields))
		}
		applyImageTypeLimit(items, query.imageTypeLimit)
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            items,
			TotalRecordCount: result.Total,
			StartIndex:       query.startIndex,
		})
		return
	}

	if len(query.itemTypes) > 0 {
		typeSet := make(map[string]bool, len(query.itemTypes))
		for _, t := range query.itemTypes {
			typeSet[strings.ToLower(t)] = true
		}
		filtered := favoriteItems[:0]
		for _, item := range favoriteItems {
			if typeSet[strings.ToLower(item.Type)] {
				filtered = append(filtered, item)
			}
		}
		favoriteItems = filtered
	}

	h.rememberListImages(favoriteItems)

	progress, err := resolveProgressForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(favoriteItems))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items := make([]baseItemDTO, 0, len(favoriteItems))
	for _, item := range favoriteItems {
		items = append(items, h.mapper.itemFromList(item, true, progress[item.ContentID], nil))
	}
	total := len(items)
	items = sliceBaseItems(items, query.startIndex, query.limit)
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

func (h *ItemsHandler) handleSearchItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	result, err := h.content.SearchItems(r.Context(), session, SearchItemsOptions{
		Query:     query.searchTerm,
		ItemTypes: searchItemTypesForQuery(query),
		Limit:     query.limit,
		Offset:    query.startIndex,
		LibraryID: libraryIDPtr(query.parentLibraryID),
		SkipTotal: !query.enableTotalRecordCount,
	})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberListImages(result.Items)

	contentIDs := contentIDsFromListItems(result.Items)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	episodeTargets, err := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, episodeContentIDsFromListItems(result.Items), nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	// Encode library ID for the ParentId field on each item (empty for global search).
	var libraryParentID string
	if query.parentLibraryID > 0 {
		libraryParentID = h.codec.EncodeIntID(EncodedIDLibrary, int64(query.parentLibraryID))
	}

	var detailsByID map[string]*upstreamItemDetail
	if query.needsDetailFields {
		detailsByID = h.batchListItemDetails(r.Context(), session, contentIDs, libraryIDPtr(query.parentLibraryID))
	}

	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		if query.needsDetailFields {
			if detail, ok := detailsByID[item.ContentID]; ok && detail != nil {
				h.rememberDetailImages(*detail)
				dto := h.mapper.itemFromDetailWithFields(*detail, favorites[detail.ContentID], progress[detail.ContentID], query.requestedFields)
				if libraryParentID != "" {
					dto.ParentID = libraryParentID
				}
				if target, ok := episodeTargets[detail.ContentID]; ok {
					h.applyCompatEpisodeTarget(&dto, target)
				}
				items = append(items, dto)
				continue
			}
		}

		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		if libraryParentID != "" {
			dto.ParentID = libraryParentID
		}
		if target, ok := episodeTargets[item.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: result.Total,
		StartIndex:       query.startIndex,
	})
}

func (h *ItemsHandler) handleSpecificItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, query.specificIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	items := make([]baseItemDTO, 0, len(query.specificIDs))
	for _, contentID := range query.specificIDs {
		detail, itemErr := h.content.GetItemDetail(r.Context(), session, contentID, libraryIDPtr(query.parentLibraryID))
		if itemErr != nil {
			if isHTTPStatus(itemErr, http.StatusNotFound) {
				continue
			}
			writeCompatUpstreamError(w, itemErr)
			return
		}
		h.rememberDetailImages(*detail)
		dto := h.mapper.itemFromDetailWithFields(*detail, favorites[detail.ContentID], progress[detail.ContentID], query.requestedFields)
		h.appendDownloadedSubtitlesToDetailDTO(r.Context(), detail.ContentID, detail.Versions, &dto)
		items = append(items, dto)
	}

	// Ids= may also reference collections (BoxSet route IDs handed out by the
	// collections listing); append their DTOs so clients can re-hydrate them.
	boxSets, err := h.boxSetsByIDs(r.Context(), session, query.specificCollectionIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items = append(items, boxSets...)

	// Ids= may also reference the synthetic Collections view; prepend its DTO so
	// clients re-hydrate the CollectionFolder the same way as a real library.
	if idsRequestCollectionsView(r) {
		items = append([]baseItemDTO{h.collectionsView()}, items...)
	}

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       query.startIndex,
	})
}

// handlePlayedItems serves IsPlayed=True requests by querying completed
// progress entries directly, rather than browsing the entire catalog and
// post-filtering. This avoids the pathological 3+ second scan when few
// items match.
func (h *ItemsHandler) handlePlayedItems(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	if h.userData == nil {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            []baseItemDTO{},
			TotalRecordCount: 0,
			StartIndex:       query.startIndex,
		})
		return
	}
	if query.mediaTypesExplicit && !query.mediaTypesSet["video"] {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            []baseItemDTO{},
			TotalRecordCount: 0,
			StartIndex:       query.startIndex,
		})
		return
	}

	typeSet := make(map[string]bool, len(query.itemTypes))
	for _, t := range query.itemTypes {
		typeSet[strings.ToLower(t)] = true
	}

	libraryID := libraryIDPtr(query.parentLibraryID)
	items, total, err := h.loadProgressPage(r.Context(), session, "completed", query, typeSet, libraryID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	applyImageTypeLimit(items, query.imageTypeLimit)

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

func (h *ItemsHandler) handleResumeResponse(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	if h.userData == nil {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            []baseItemDTO{},
			TotalRecordCount: 0,
			StartIndex:       query.startIndex,
		})
		return
	}
	if query.mediaTypesExplicit && !query.mediaTypesSet["video"] {
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            []baseItemDTO{},
			TotalRecordCount: 0,
			StartIndex:       query.startIndex,
		})
		return
	}

	typeSet := make(map[string]bool, len(query.itemTypes))
	for _, t := range query.itemTypes {
		typeSet[strings.ToLower(t)] = true
	}

	// Fast path: when the native sections subsystem is wired, serve Continue
	// Watching through the hard-capped continue-watching fetcher instead of
	// loadProgressPage's unbounded scan. loadProgressPage re-paginates the
	// entire in-progress list (and re-derives superseded/completed history per
	// batch) whenever EnableTotalRecordCount=true or the visible count is under
	// the limit; the fetcher caps scanning at continueProgressMaxScanned and
	// filters via the indexed home-dismissal index. The native "watching" scope
	// returns movies+episodes, so we take the fast path whenever the request is
	// unconstrained OR asks for Movie/Episode (which covers the dominant traffic
	// — clients overwhelmingly send IncludeItemTypes including Movie+Episode);
	// loadResumeViaSections applies the IncludeItemTypes set as a post-filter so
	// narrower requests are still honored. Requests scoped only to types the
	// watching scope cannot serve (e.g. Series/Season-only) fall through to
	// loadProgressPage below.
	if h.sectionsFetcher != nil && (len(typeSet) == 0 || typeSet["episode"] || typeSet["movie"]) {
		items, total, err := h.loadResumeViaSections(r.Context(), session, query, typeSet)
		if err == nil {
			applyImageTypeLimit(items, query.imageTypeLimit)
			writeJSON(w, http.StatusOK, queryResultDTO{
				Items:            items,
				TotalRecordCount: total,
				StartIndex:       query.startIndex,
			})
			return
		}
		slog.Warn("jellycompat: resume via sections failed, falling back to progress scan", "error", err)
	}

	items, total, err := h.loadProgressPage(r.Context(), session, "in_progress", query, typeSet, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

// maxResumeItems caps how many Continue Watching entries the sections fast path
// returns per request, regardless of the client's requested limit. Resume rows
// are display surfaces, not bulk exports; clamping keeps the capped fetcher's
// scan bounded and predictable under load.
const maxResumeItems = 50

// resumeScanMaxRows bounds how many progress rows loadProgressPage will scan in
// a single request. Without a cap, a client sending EnableTotalRecordCount=true
// (or a Series/Season-only request, which matches no leaf in-progress row) forces
// the in-progress loop to page through the profile's entire history — an
// O(history) scan that reached tens of seconds for heavy watchers.
//
// In the common case the loop exits far earlier (the page fills from the first
// batch or two), so this cap only engages for pathological/empty-match requests;
// its value just bounds that worst case. 300 gives ample headroom to fill a
// ~20-item Continue Watching page even when the great majority of recent rows are
// dismissed/superseded, while keeping the wasted scan on empty-match requests
// small. Progress rows are recency-ordered, so the first resumeScanMaxRows cover
// every realistically-visible entry; beyond the cap the returned total becomes a
// clamped lower bound (the "+" suffix behavior clients already tolerate).
const resumeScanMaxRows = 300

// loadResumeViaSections serves Continue Watching through the native
// continue-watching fetcher. The fetcher applies the same dismissal and
// superseded-episode filtering as FilterResumeProgress, so visible parity with
// loadProgressPage is preserved, but scanning is hard-capped. Next-up injection
// is suppressed (Resume must stay in-progress-only) and the resolved items are
// re-hydrated through the shared progress path so episode DTOs keep their
// SeriesId/IndexNumber/SeasonId targeting — metadata the section list mapping
// (which discards SectionItemMeta) would drop.
func (h *ItemsHandler) loadResumeViaSections(ctx context.Context, session *Session, query itemsQuery, typeSet map[string]bool) ([]baseItemDTO, int, error) {
	pageSize := query.limit
	if pageSize <= 0 {
		pageSize = maxResumeItems
	}
	if pageSize > maxResumeItems {
		pageSize = maxResumeItems
	}

	// FetchOne always scans from offset 0; over-fetch by StartIndex so a deep
	// page can be sliced out of the capped result (Resume is normally offset 0).
	// Clamp the fetch to maxResumeItems so a large client-supplied StartIndex
	// can't inflate the fetcher's scan past the cap; a StartIndex at or beyond
	// the cap can never land on a visible row, so return empty up front.
	if query.startIndex >= maxResumeItems {
		return []baseItemDTO{}, 0, nil
	}
	fetchLimit := pageSize + query.startIndex
	if fetchLimit > maxResumeItems {
		fetchLimit = maxResumeItems
	}

	filter := h.resolveAccessFilter(ctx, session)
	resolved := sections.ResolvedSection{
		ID:             "compat-resume",
		SectionType:    sections.SectionContinueWatching,
		Title:          "Continue Watching",
		ItemLimit:      fetchLimit,
		Config:         sections.ContinueTypeConfig(sections.ContinueTypeWatching),
		SuppressNextUp: true,
	}

	result, err := h.sectionsFetcher.FetchOne(ctx, resolved, nil, filter.AllowedLibraryIDs, session.StreamAppUserID, session.ProfileID, filter)
	if err != nil {
		return nil, 0, err
	}

	entries := make([]upstreamProgress, 0, len(result.Items))
	for _, mi := range result.Items {
		if mi == nil {
			continue
		}
		meta := result.ItemMeta[mi.ContentID]
		// Defensive: only in-progress resume points belong on the Resume row.
		// SuppressNextUp already prevents next-up injection upstream; this
		// guards against any other source slipping in.
		if meta.ItemSource != "" && meta.ItemSource != "in_progress" {
			continue
		}
		// Honor IncludeItemTypes the same way loadProgressPage does. The
		// watching scope only emits movies/episodes, so this trims to a narrower
		// request (e.g. Movie-only); applying it before the StartIndex slice and
		// the page cap keeps paging correct.
		if len(typeSet) > 0 && !typeSet[strings.ToLower(mi.Type)] {
			continue
		}
		entry := upstreamProgress{MediaItemID: mi.ContentID}
		if meta.PositionSeconds != nil {
			entry.PositionSeconds = *meta.PositionSeconds
		}
		if meta.DurationSeconds != nil {
			entry.DurationSeconds = *meta.DurationSeconds
		}
		if meta.ProgressUpdatedAt != nil {
			entry.UpdatedAt = *meta.ProgressUpdatedAt
		}
		entries = append(entries, entry)
	}

	// Slice out the requested page from the capped, ordered result.
	if query.startIndex > 0 {
		if query.startIndex >= len(entries) {
			entries = nil
		} else {
			entries = entries[query.startIndex:]
		}
	}
	if len(entries) > pageSize {
		entries = entries[:pageSize]
	}

	hydrated, err := h.hydrateProgressItems(ctx, session, entries, query.requestedFields, nil)
	if err != nil {
		return nil, 0, err
	}
	dtos := h.finishProgressPage(ctx, session, hydrated, query, nil)
	return dtos, len(dtos), nil
}

type progressHydratedItem struct {
	itemType   string
	contentID  string
	isFavorite bool
	entry      upstreamProgress
	target     *compatEpisodeTarget
	dto        baseItemDTO
}

// maxDetailUpgrades caps how many returned-page items get re-mapped through
// the per-item detail path when the client requested detail-level Fields.
// Entries past the cap keep their stubbed list-level DTO. Guardrail against
// absurd client limits — normal Resume/NextUp requests are 20-40 items.
const maxDetailUpgrades = 100

// sortedTypeSet returns the (already lowercased) keys of a type set in a stable
// order so the SQL pre-filter binds a deterministic types array.
func sortedTypeSet(typeSet map[string]bool) []string {
	keys := make([]string, 0, len(typeSet))
	for k := range typeSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (h *ItemsHandler) loadProgressPage(ctx context.Context, session *Session, status string, query itemsQuery, typeSet map[string]bool, libraryID *int) ([]baseItemDTO, int, error) {
	// Resume views hide dismissed and superseded entries, so the visible list
	// is sparser than the raw store list. The raw-offset fast path below is
	// only safe for the first page; deeper StartIndex values must go through
	// the scan-from-zero branch, which paginates over visible entries.
	resumeFiltered := status == "in_progress"
	if len(typeSet) == 0 && libraryID == nil && !query.enableTotalRecordCount && (!resumeFiltered || query.startIndex == 0) {
		batchSize := min(max(query.limit*2, 48), 200)
		if batchSize <= 0 {
			batchSize = 48
		}

		result := make([]progressHydratedItem, 0, query.limit)
		offset := query.startIndex
		for {
			progressEntries, err := h.userData.ListProgress(ctx, session, status, batchSize, offset)
			if err != nil {
				return nil, 0, err
			}
			if len(progressEntries) == 0 {
				break
			}
			rawCount := len(progressEntries)
			if resumeFiltered {
				progressEntries, err = h.userData.FilterResumeProgress(ctx, session, progressEntries)
				if err != nil {
					return nil, 0, err
				}
			}

			items, err := h.hydrateProgressItems(ctx, session, progressEntries, query.requestedFields, libraryID)
			if err != nil {
				return nil, 0, err
			}
			for _, item := range items {
				if len(result) >= query.limit {
					break
				}
				result = append(result, item)
			}
			if len(result) >= query.limit || rawCount < batchSize {
				break
			}
			// Same bound as the general loop below: the resume fast path filters in
			// memory via FilterResumeProgress, so a sparse visible set (heavy
			// watcher with mostly dismissed/superseded recent rows) would otherwise
			// page to the end of history without ever filling the page. This is the
			// default Continue Watching shape and the fallback hit when the sections
			// fetcher is unavailable, so it must be bounded too.
			if resumeFiltered && offset+rawCount >= resumeScanMaxRows {
				break
			}
			offset += rawCount
		}
		return h.finishProgressPage(ctx, session, result, query, libraryID), 0, nil
	}

	batchSize := min(max(query.limit*3, 48), 200)
	if batchSize <= 0 {
		batchSize = 48
	}

	// Push the type/library predicate into SQL for the completed (watched-items)
	// path: the store filters before paging, so the scan reads only matching
	// rows instead of the profile's entire completed history. The in-memory type
	// check below and the library-scoped hydration stay as a correctness
	// backstop (access/parental exclusions still apply). The in_progress path is
	// deliberately left on ListProgress — its FilterResumeProgress hiding makes
	// the visible set sparser than any SQL pre-filter could express.
	useFilteredFetch := status == "completed" && (len(typeSet) > 0 || libraryID != nil)
	var filteredTypes []string
	if useFilteredFetch {
		filteredTypes = sortedTypeSet(typeSet)
	}
	fetchProgress := func(off int) ([]upstreamProgress, error) {
		if useFilteredFetch {
			return h.userData.ListProgressFiltered(ctx, session, status, filteredTypes, libraryID, batchSize, off)
		}
		return h.userData.ListProgress(ctx, session, status, batchSize, off)
	}

	items := make([]progressHydratedItem, 0, query.limit)
	matchedCount := 0
	offset := 0

	for {
		progressEntries, err := fetchProgress(offset)
		if err != nil {
			return nil, 0, err
		}
		if len(progressEntries) == 0 {
			break
		}
		rawCount := len(progressEntries)
		if resumeFiltered {
			progressEntries, err = h.userData.FilterResumeProgress(ctx, session, progressEntries)
			if err != nil {
				return nil, 0, err
			}
		}

		hydrated, err := h.hydrateProgressItems(ctx, session, progressEntries, query.requestedFields, libraryID)
		if err != nil {
			return nil, 0, err
		}
		for _, item := range hydrated {
			if len(typeSet) > 0 && !typeSet[item.itemType] {
				continue
			}
			if matchedCount >= query.startIndex && len(items) < query.limit {
				items = append(items, item)
			}
			matchedCount++
		}

		offset += rawCount
		if rawCount < batchSize {
			break
		}
		if !query.enableTotalRecordCount && len(items) >= query.limit {
			break
		}
		// Bound the resume scan: never page through more than resumeScanMaxRows of
		// history in one request, even when the visible (post-filter) page stays
		// sparse. The in-progress path filters in memory via FilterResumeProgress,
		// so a heavy watcher whose recent rows are mostly dismissed/superseded
		// could otherwise keep paging to the end of history without ever filling
		// the page. Progress rows are recency-ordered, so the most recent
		// resumeScanMaxRows already cover every realistically-visible Continue
		// Watching entry; beyond the cap we stop and return what we have (and, for
		// EnableTotalRecordCount, a clamped lower-bound total).
		//
		// Gated on resumeFiltered so the completed (watched-items) path is exempt:
		// there the SQL pre-filter already bounds the scan and clients paginate on
		// an exact TotalRecordCount, so capping would underreport the total and
		// drop deep StartIndex pages.
		if resumeFiltered && offset >= resumeScanMaxRows {
			break
		}
	}

	total := 0
	if query.enableTotalRecordCount {
		total = matchedCount
	}
	return h.finishProgressPage(ctx, session, items, query, libraryID), total, nil
}

// finishProgressPage upgrades the returned page to detail-mapped DTOs when
// the client asked for detail-level Fields, then unwraps to the DTO slice.
func (h *ItemsHandler) finishProgressPage(ctx context.Context, session *Session, page []progressHydratedItem, query itemsQuery, libraryID *int) []baseItemDTO {
	h.upgradeProgressPageToDetail(ctx, session, page, query, libraryID)
	dtos := make([]baseItemDTO, 0, len(page))
	for _, item := range page {
		dtos = append(dtos, item.dto)
	}
	return dtos
}

// upgradeProgressPageToDetail re-maps the returned page of progress rows
// through the detail path when the client requested detail-level Fields
// (e.g. MediaSources). The scan loop in loadProgressPage stays on list-level
// data so type filtering over long in-progress lists never fans out — the
// per-ENTRY fanout was the 38s timeout in error-report-2026-05-08.md §6 —
// but the returned page is bounded by the request limit, so this costs the
// same as HandleLatest's existing per-item detail path. The listing's
// MediaSources ARE load-bearing for some clients: Infuse and SenPlayer build
// their Continue Watching rows from them and discard entries served only the
// list-level stub.
func (h *ItemsHandler) upgradeProgressPageToDetail(ctx context.Context, session *Session, page []progressHydratedItem, query itemsQuery, libraryID *int) {
	if !query.needsDetailFields {
		return
	}
	limit := len(page)
	if limit > maxDetailUpgrades {
		limit = maxDetailUpgrades
	}
	upgradeIDs := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		upgradeIDs = append(upgradeIDs, page[i].contentID)
	}
	detailsByID := h.batchListItemDetails(ctx, session, upgradeIDs, libraryID)
	for i := 0; i < limit; i++ {
		detail, ok := detailsByID[page[i].contentID]
		if !ok || detail == nil {
			continue
		}
		h.rememberDetailImages(*detail)
		dto := h.mapper.itemFromDetailWithFields(*detail, page[i].isFavorite, &page[i].entry, query.requestedFields)
		if page[i].target != nil {
			h.applyCompatEpisodeTarget(&dto, *page[i].target)
		}
		page[i].dto = dto
	}
}

func (h *ItemsHandler) hydrateProgressItems(ctx context.Context, session *Session, entries []upstreamProgress, fields map[string]bool, libraryID *int) ([]progressHydratedItem, error) {
	result := make([]progressHydratedItem, 0, len(entries))
	contentIDs := contentIDsFromProgressEntries(entries)
	if len(contentIDs) == 0 {
		return result, nil
	}

	favorites, err := resolveFavoritesForContentIDs(ctx, session, h.userData, contentIDs)
	if err != nil {
		return nil, err
	}
	itemsByID, err := h.fetchCompatItemsByContentIDs(ctx, session, contentIDs, libraryID)
	if err != nil {
		return nil, err
	}
	episodesByID, err := h.fetchCompatEpisodeTargetsByContentIDs(ctx, session, contentIDs, libraryID)
	if err != nil {
		return nil, err
	}

	// Resume / progress views deliberately serve list-level data even when the
	// client requests Fields=People|Chapters|MediaStreams|MediaSources. The
	// per-entry GetItemDetail fanout this loop used to do scaled to ~5N DB
	// roundtrips for users with large in-progress lists and was the cause of
	// the 38s timeout in error-report-2026-05-08.md §6. Standard Jellyfin
	// clients (Streamyfin, Infuse, Wholphin, Jellyfin-web) refetch
	// MediaSources via /Items/{id}/PlaybackInfo on play, so the detail data
	// served here was never load-bearing for the Resume row UX.
	for _, entry := range entries {
		if item, ok := itemsByID[entry.MediaItemID]; ok {
			dto := h.mapper.itemFromList(item, favorites[entry.MediaItemID], &entry, fields)
			stubDetailListFields(&dto, fields)
			result = append(result, progressHydratedItem{
				itemType:   strings.ToLower(item.Type),
				contentID:  entry.MediaItemID,
				isFavorite: favorites[entry.MediaItemID],
				entry:      entry,
				dto:        dto,
			})
			continue
		}
		if target, ok := episodesByID[entry.MediaItemID]; ok {
			dto := h.mapper.itemFromList(target.Item, favorites[entry.MediaItemID], &entry, fields)
			h.applyCompatEpisodeTarget(&dto, target)
			stubDetailListFields(&dto, fields)
			result = append(result, progressHydratedItem{
				itemType:   "episode",
				contentID:  entry.MediaItemID,
				isFavorite: favorites[entry.MediaItemID],
				entry:      entry,
				target:     &target,
				dto:        dto,
			})
		}
	}

	return result, nil
}

func (h *ItemsHandler) resolveAccessFilter(ctx context.Context, session *Session) catalog.AccessFilter {
	if h.accessFilter != nil {
		return withCompatAccessExclusions(h.accessFilter(ctx, session.StreamAppUserID, session.ProfileID))
	}
	return withCompatAccessExclusions(catalog.AccessFilter{})
}

func (h *ItemsHandler) rememberCompatEpisodeImages(dto baseItemDTO, stillURL string, series seriesImageSet) {
	if h.images == nil {
		return
	}
	h.images.RememberSized(dto.ID, "Primary", stillURL, compatCardImageSize)
	h.images.RememberSized(dto.ID, "Backdrop", series.BackdropURL, compatCardImageSize)
	if dto.SeriesID != "" {
		h.images.RememberSized(dto.SeriesID, "Primary", series.PosterURL, compatCardImageSize)
		h.images.RememberSized(dto.SeriesID, "Backdrop", series.BackdropURL, compatCardImageSize)
		h.images.RememberSized(dto.SeriesID, "Thumb", series.BackdropURL, compatCardImageSize)
	}
}

func (h *ItemsHandler) applyCompatEpisodeTarget(dto *baseItemDTO, target compatEpisodeTarget) {
	if target.SeasonID != "" && h.codec != nil {
		encodedSeasonID := h.codec.EncodeStringID(EncodedIDSeason, target.SeasonID)
		if dto.SeasonID == "" {
			dto.SeasonID = encodedSeasonID
		}
		if dto.ParentID == "" {
			dto.ParentID = encodedSeasonID
		}
	}
	if dto.SeasonName == "" && target.SeasonName != "" {
		dto.SeasonName = target.SeasonName
	}
	h.mapper.applySeriesImages(dto, target.SeriesImages)
	h.rememberCompatEpisodeImages(*dto, firstNonEmpty(target.Item.StillURL, target.Item.PosterURL), target.SeriesImages)
}

func (h *ItemsHandler) listSeriesEpisodes(ctx context.Context, session *Session, seriesID string, seasons []upstreamSeason, requestedSeasonID string) ([]*models.Episode, error) {
	if h.episodeRepo != nil {
		if requestedSeasonID != "" {
			for _, season := range seasons {
				if season.ContentID == requestedSeasonID {
					return h.episodeRepo.ListBySeason(ctx, seriesID, season.SeasonNumber)
				}
			}
			return []*models.Episode{}, nil
		}
		return h.episodeRepo.ListBySeries(ctx, seriesID)
	}

	episodes := make([]*models.Episode, 0)
	for _, season := range seasons {
		if requestedSeasonID != "" && season.ContentID != requestedSeasonID {
			continue
		}
		upstreamEpisodes, err := h.content.ListEpisodes(ctx, session, seriesID, season.SeasonNumber, nil)
		if err != nil {
			return nil, err
		}
		for _, episode := range upstreamEpisodes {
			episodes = append(episodes, &models.Episode{
				ContentID:     episode.ContentID,
				SeriesID:      seriesID,
				SeasonID:      firstNonEmpty(episode.SeasonID, season.ContentID),
				SeasonNumber:  episode.SeasonNumber,
				EpisodeNumber: episode.EpisodeNumber,
				Title:         episode.Title,
				Overview:      episode.Overview,
				Runtime:       episode.Runtime,
				StillPath:     episode.StillURL,
			})
		}
	}
	return episodes, nil
}

func compactEpisodeModels(episodes []*models.Episode) []*models.Episode {
	write := 0
	for _, episode := range episodes {
		if episode == nil {
			continue
		}
		episodes[write] = episode
		write++
	}
	return episodes[:write]
}

func trimEpisodesFromStartItem(episodes []*models.Episode, rawStartItemID string, codec *ResourceIDCodec) []*models.Episode {
	rawStartItemID = strings.TrimSpace(rawStartItemID)
	if rawStartItemID == "" {
		return episodes
	}
	if codec == nil {
		return []*models.Episode{}
	}
	startContentID, err := decodeItemID(codec, rawStartItemID)
	if err != nil || startContentID == "" {
		return []*models.Episode{}
	}
	for i, episode := range episodes {
		if episode != nil && episode.ContentID == startContentID {
			return episodes[i:]
		}
	}
	return []*models.Episode{}
}

func intPtr(value int) *int {
	return &value
}

func contentIDsFromListItems(items []upstreamListItem) []string {
	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		contentIDs = append(contentIDs, item.ContentID)
	}
	return normalizeContentIDs(contentIDs)
}

func episodeContentIDsFromListItems(items []upstreamListItem) []string {
	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(item.Type, "episode") {
			contentIDs = append(contentIDs, item.ContentID)
		}
	}
	return normalizeContentIDs(contentIDs)
}

func seasonContentIDs(seasons []upstreamSeason) []string {
	contentIDs := make([]string, 0, len(seasons))
	for _, season := range seasons {
		contentIDs = append(contentIDs, season.ContentID)
	}
	return normalizeContentIDs(contentIDs)
}

func contentIDsFromEpisodes(episodes []*models.Episode) []string {
	contentIDs := make([]string, 0, len(episodes))
	for _, episode := range episodes {
		if episode != nil {
			contentIDs = append(contentIDs, episode.ContentID)
		}
	}
	return normalizeContentIDs(contentIDs)
}

func contentIDsFromNextUpResults(results []catalog.NextUpResult) []string {
	contentIDs := make([]string, 0, len(results))
	for _, result := range results {
		contentIDs = append(contentIDs, result.ContentID)
	}
	return normalizeContentIDs(contentIDs)
}

func contentIDsFromProgressEntries(entries []upstreamProgress) []string {
	contentIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		contentIDs = append(contentIDs, entry.MediaItemID)
	}
	return normalizeContentIDs(contentIDs)
}

func libraryIDPtr(libraryID int) *int {
	if libraryID <= 0 {
		return nil
	}
	return &libraryID
}

func urlValuesFromItemsQuery(query itemsQuery) url.Values {
	return buildBrowseParams(query)
}

func progressMap(entries []upstreamProgress) map[string]*upstreamProgress {
	result := make(map[string]*upstreamProgress, len(entries))
	for i := range entries {
		entry := entries[i]
		result[entry.MediaItemID] = &entry
	}
	return result
}

func decodeContentID(codec *ResourceIDCodec, raw string) (string, error) {
	if id, err := decodeItemID(codec, raw); err == nil {
		return id, nil
	}
	return codec.DecodeStringID(EncodedIDSeason, raw)
}

func decodeItemID(codec *ResourceIDCodec, raw string) (string, error) {
	return codec.DecodeStringID(EncodedIDItem, raw)
}

func validatePseudoUser(w http.ResponseWriter, userID string, session *Session) bool {
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return false
	}
	if userID == "" || userID == session.PseudoUserID.String() {
		return true
	}
	writeError(w, http.StatusNotFound, "NotFound", "User not found")
	return false
}

func sliceBaseItems(items []baseItemDTO, startIndex, limit int) []baseItemDTO {
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex >= len(items) {
		return []baseItemDTO{}
	}
	if limit <= 0 {
		limit = len(items)
	}
	end := min(startIndex+limit, len(items))
	return items[startIndex:end]
}

func (h *ItemsHandler) rememberLibraryImages(library upstreamUserLibrary, routeID string) {
	if h.images == nil || library.PosterURL == "" {
		return
	}
	h.images.RememberSized(routeID, "Primary", library.PosterURL, compatCardImageSize)
}

func (h *ItemsHandler) rememberListImages(items []upstreamListItem) {
	if h.images == nil {
		return
	}
	for _, item := range items {
		routeID := h.codec.EncodeStringID(EncodedIDItem, item.ContentID)
		h.images.RememberSized(routeID, "Primary", firstNonEmpty(item.StillURL, item.PosterURL), compatCardImageSize)
		h.images.RememberSized(routeID, "Backdrop", item.BackdropURL, compatCardImageSize)
		h.images.RememberSized(routeID, "Logo", item.LogoURL, compatCardImageSize)
	}
}

func (h *ItemsHandler) rememberDetailImages(detail upstreamItemDetail) {
	if h.images == nil {
		return
	}
	// Detail payloads carry featured-sized artwork: catalog/detail.go presigns
	// at size="" which maps to w500 for poster/logo and w1920 for backdrop in
	// cachedImageVariantKey. Both match the "medium" compat bucket — seeding at
	// compatCardImageSize would mislabel the route bucket and pollute card-size
	// entries learned from list paths; seeding at "original" would shadow the
	// genuine original asset.
	const detailImageSize = "medium"
	routeIDs := []string{h.codec.EncodeStringID(EncodedIDItem, detail.ContentID)}
	if strings.EqualFold(detail.Type, "season") {
		routeIDs = append(routeIDs, h.codec.EncodeStringID(EncodedIDSeason, detail.ContentID))
	}
	primaryURL := firstNonEmpty(detail.PosterURL, detail.BackdropURL)
	for _, routeID := range routeIDs {
		if primaryURL != "" {
			h.images.RememberSized(routeID, "Primary", primaryURL, detailImageSize)
		}
		if detail.BackdropURL != "" {
			h.images.RememberSized(routeID, "Backdrop", detail.BackdropURL, detailImageSize)
		}
		if detail.LogoURL != "" {
			h.images.RememberSized(routeID, "Logo", detail.LogoURL, detailImageSize)
		}
	}
	for _, cast := range detail.Cast {
		if cast.PhotoURL != "" {
			if pid, _ := strconv.ParseInt(cast.PersonID, 10, 64); pid > 0 {
				h.images.RememberSized(h.codec.EncodeIntID(EncodedIDPerson, pid), "Primary", cast.PhotoURL, compatCardImageSize)
			}
		}
	}
	for _, crew := range detail.Crew {
		if crew.PhotoURL != "" {
			if pid, _ := strconv.ParseInt(crew.PersonID, 10, 64); pid > 0 {
				h.images.RememberSized(h.codec.EncodeIntID(EncodedIDPerson, pid), "Primary", crew.PhotoURL, compatCardImageSize)
			}
		}
	}
}

func (h *ItemsHandler) rememberSeasonImages(seasons []upstreamSeason, seriesID string) {
	if h.images == nil {
		return
	}
	for _, season := range seasons {
		h.images.RememberSized(h.codec.EncodeStringID(EncodedIDSeason, season.ContentID), "Primary", season.PosterURL, compatCardImageSize)
		h.images.RememberSized(h.codec.EncodeStringID(EncodedIDItem, season.ContentID), "Primary", season.PosterURL, compatCardImageSize)
		if seriesID != "" {
			h.images.RememberSized(h.codec.EncodeStringID(EncodedIDItem, seriesID), "Primary", season.PosterURL, compatCardImageSize)
		}
	}
}

func (h *ItemsHandler) rememberEpisodeImages(episodes []upstreamEpisode) {
	if h.images == nil {
		return
	}
	for _, episode := range episodes {
		h.images.RememberSized(h.codec.EncodeStringID(EncodedIDItem, episode.ContentID), "Primary", episode.StillURL, compatCardImageSize)
	}
}

// enrichEpisodeSeriesImages looks up the parent series poster/backdrop and
// applies them to an episode DTO. The cache avoids repeated lookups when
// multiple episodes belong to the same series.
func (h *ItemsHandler) enrichEpisodeSeriesImages(ctx context.Context, session *Session, dto *baseItemDTO, seriesContentID string, cache map[string]seriesImageSet) {
	if seriesContentID == "" || dto.SeriesID == "" {
		return
	}
	imgs, ok := cache[seriesContentID]
	if !ok {
		detail, err := h.content.GetItemDetail(ctx, session, seriesContentID, nil)
		if err == nil {
			imgs = seriesImageSet{
				ContentID:         detail.ContentID,
				PosterURL:         detail.PosterURL,
				PosterPath:        detail.PosterPath,
				PosterThumbhash:   detail.PosterThumbhash,
				BackdropURL:       detail.BackdropURL,
				BackdropPath:      detail.BackdropPath,
				BackdropThumbhash: detail.BackdropThumbhash,
				UpdatedAt:         detail.UpdatedAt,
			}
			h.rememberDetailImages(*detail)
		}
		cache[seriesContentID] = imgs
	}
	h.mapper.applySeriesImages(dto, imgs)
	if imgs.BackdropURL != "" && h.images != nil {
		h.images.RememberSized(dto.SeriesID, "Thumb", imgs.BackdropURL, compatCardImageSize)
	}
}

func max(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func writeCompatUpstreamError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUpstreamReplaced) {
		writeError(w, http.StatusConflict, "Conflict", "Playback session changed concurrently; retry the request")
		return
	}
	if errors.Is(err, playback.ErrTooManyStreams) {
		writeError(w, http.StatusTooManyRequests, "TooManyStreams", "Too many concurrent streams")
		return
	}
	if errors.Is(err, playback.ErrTooManyTranscodes) {
		writeError(w, http.StatusTooManyRequests, "TooManyTranscodes", "Too many concurrent transcodes")
		return
	}
	if errors.Is(err, playback.ErrPlaybackNotAllowed) {
		writeError(w, http.StatusForbidden, "PlaybackNotAllowed", "Playback denied by server policy")
		return
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "NotFound", "Resource not found")
		case http.StatusUnauthorized:
			writeError(w, http.StatusUnauthorized, "Unauthorized", "Authentication failed")
		default:
			writeError(w, http.StatusBadGateway, "UpstreamError", httpErr.Error())
		}
		return
	}
	writeError(w, http.StatusInternalServerError, "ServerError", "Unexpected compat error")
}

// inferLibraryItemType looks up the collection type for a library and returns
// the matching browse item type. Jellyfin's /Items/Latest uses this to return
// Movie items for movies libraries and Series items for tvshows libraries.
func (h *ItemsHandler) inferLibraryItemType(ctx context.Context, session *Session, libraryID int) string {
	libraries, err := h.content.ListUserLibraries(ctx, session)
	if err != nil {
		return ""
	}
	for _, lib := range libraries {
		if lib.ID == libraryID {
			switch lib.Type {
			case "movies":
				return "movie"
			case "series":
				return "series"
			default:
				return ""
			}
		}
	}
	return ""
}

func isHTTPStatus(err error, status int) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == status
}

// applyImageTypeLimit clears image tags from DTOs when ImageTypeLimit=0.
func applyImageTypeLimit(items []baseItemDTO, limit *int) {
	if limit == nil || *limit > 0 {
		return
	}
	for i := range items {
		items[i].ImageTags = map[string]string{}
		items[i].BackdropImageTags = nil
		items[i].PrimaryImageAspectRatio = nil
	}
}
