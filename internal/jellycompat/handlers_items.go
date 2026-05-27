package jellycompat

import (
	"context"
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

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/recommendations"
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
	accessFilter AccessFilterResolver
	subtitleRepo subtitles.Repository
	recommender  recommendations.Recommender
	// FileResolver is optional; when set, /MediaSegments returns real intro/
	// credits/recap/preview segments for any file that has them.
	FileResolver FilePathResolver
}

// NewItemsHandler creates a new items handler.
func NewItemsHandler(content ContentService, userData UserDataService, codec *ResourceIDCodec, cfg *config.Config, images *ImageCache, nextUpRepo *catalog.NextUpRepository, browseRepo *catalog.BrowseRepository, personRepo *catalog.PersonRepository, detailSvc *catalog.DetailService, itemRepo *catalog.ItemRepository, episodeRepo *catalog.EpisodeRepository, accessFilter AccessFilterResolver, subtitleRepo subtitles.Repository) *ItemsHandler {
	return &ItemsHandler{
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

	libraries, err := h.content.ListUserLibraries(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	items := make([]baseItemDTO, 0, len(libraries))
	for _, library := range libraries {
		dto := h.mapper.viewFromLibrary(library)
		h.rememberLibraryImages(library, dto.ID)
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
}

// handleViewsResponse returns the user's library views as CollectionFolder items.
func (h *ItemsHandler) handleViewsResponse(w http.ResponseWriter, r *http.Request, session *Session) {
	libraries, err := h.content.ListUserLibraries(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	items := make([]baseItemDTO, 0, len(libraries))
	for _, library := range libraries {
		dto := h.mapper.viewFromLibrary(library)
		h.rememberLibraryImages(library, dto.ID)
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
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
	switch {
	case len(query.specificIDs) > 0:
		h.handleSpecificItems(w, r, session, query)
	case query.isResumable:
		h.handleResumeResponse(w, r, session, query)
	case query.isPlayed != nil && *query.isPlayed:
		h.handlePlayedItems(w, r, session, query)
	case query.searchTerm != "":
		h.handleSearchItems(w, r, session, query)
	case query.isFavorite:
		h.handleFavoriteItems(w, r, session, query)
	case query.parentLibraryID == 0 && len(query.itemTypes) == 0:
		// No ParentId and no type filter: return top-level library views.
		// Jellyfin clients (e.g. Findroid "My Media") call GET /Items?userId=...
		// and expect CollectionFolder items representing the user's libraries.
		h.handleViewsResponse(w, r, session)
	default:
		h.handleBrowseItems(w, r, session, query)
	}
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

	// Handle library IDs — clients like Infuse request /Items/{id} for
	// CollectionFolder items using the library UUID from /UserViews.
	if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, rawID); err == nil {
		h.handleLibraryItem(w, r, session, int(libraryID))
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
	if query.parentLibraryID > 0 && len(query.itemTypes) == 0 {
		if inferredType := h.inferLibraryItemType(r.Context(), session, query.parentLibraryID); inferredType != "" {
			query.itemTypes = []string{inferredType}
		}
	}

	params := buildLatestBrowseParams(query)
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

	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		if query.needsDetailFields {
			detail, detailErr := h.content.GetItemDetail(r.Context(), session, item.ContentID, libraryIDPtr(query.parentLibraryID))
			if detailErr == nil {
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
	writeJSON(w, http.StatusOK, items)
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
	seasons = filterBrowsableSeasons(seasons)
	h.rememberSeasonImages(seasons, seriesID)

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
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
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

	seasons, err := h.content.ListSeasons(r.Context(), session, seriesID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.rememberSeasonImages(seasons, seriesID)

	qp := newCaseInsensitiveQuery(r.URL.Query())

	var requestedSeasonID string
	if rawSeasonID := strings.TrimSpace(qp.Get("SeasonId")); rawSeasonID != "" {
		decodedSeasonID, decodeErr := h.codec.DecodeStringID(EncodedIDSeason, rawSeasonID)
		if decodeErr != nil {
			writeError(w, http.StatusNotFound, "NotFound", "Season not found")
			return
		}
		requestedSeasonID = decodedSeasonID
	}

	seasonTitleByID := make(map[string]string, len(seasons))
	seasonTitleByNumber := make(map[int]string, len(seasons))
	seasonIDByNumber := make(map[int]string, len(seasons))
	for _, season := range seasons {
		seasonTitleByID[season.ContentID] = season.Title
		seasonTitleByNumber[season.SeasonNumber] = season.Title
		seasonIDByNumber[season.SeasonNumber] = season.ContentID
	}

	episodeModels, err := h.listSeriesEpisodes(r.Context(), session, seriesID, seasons, requestedSeasonID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

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

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(items),
		StartIndex:       0,
	})
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

	items := make([]baseItemDTO, 0, len(results))
	for _, res := range results {
		target, ok := episodeTargets[res.ContentID]
		if !ok {
			continue
		}
		// NextUp deliberately serves list-level data even when the client
		// requests Fields=People|Chapters|MediaStreams|MediaSources, mirroring
		// the Resume hydrator (handlers_items.go ~L1721). Per-item
		// GetItemDetail fanout was 5N DB roundtrips for users with long
		// next-up lists, and the detail data was never load-bearing for the
		// row UX — clients refetch MediaSources via /Items/{id}/PlaybackInfo
		// on play.
		dto := h.mapper.itemFromList(target.Item, favorites[res.ContentID], progress[res.ContentID], query.requestedFields)
		h.applyCompatEpisodeTarget(&dto, target)
		stubDetailListFields(&dto, query.requestedFields)
		items = append(items, dto)
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
	if query == "" {
		writeJSON(w, http.StatusOK, searchHintResultDTO{})
		return
	}

	limit := parsePositiveInt(q.Get("Limit"), 20)
	result, err := h.content.SearchItems(r.Context(), session, query, nil, limit, 0, nil)
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

	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		if query.needsDetailFields {
			detail, detailErr := h.content.GetItemDetail(r.Context(), session, item.ContentID, libraryIDPtr(query.parentLibraryID))
			if detailErr == nil {
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
			listItem := mediaItemToListItem(mi)
			h.presignCompatListItem(r.Context(), &listItem)
			listItems = append(listItems, listItem)
		}
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
	result, err := h.content.SearchItems(r.Context(), session, query.searchTerm, query.itemTypes, query.limit, query.startIndex, libraryIDPtr(query.parentLibraryID))
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

	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		if query.needsDetailFields {
			detail, detailErr := h.content.GetItemDetail(r.Context(), session, item.ContentID, libraryIDPtr(query.parentLibraryID))
			if detailErr == nil {
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

type progressHydratedItem struct {
	itemType string
	dto      baseItemDTO
}

func (h *ItemsHandler) loadProgressPage(ctx context.Context, session *Session, status string, query itemsQuery, typeSet map[string]bool, libraryID *int) ([]baseItemDTO, int, error) {
	if len(typeSet) == 0 && libraryID == nil && !query.enableTotalRecordCount {
		batchSize := min(max(query.limit*2, 48), 200)
		if batchSize <= 0 {
			batchSize = 48
		}

		result := make([]baseItemDTO, 0, query.limit)
		offset := query.startIndex
		for {
			progressEntries, err := h.userData.ListProgress(ctx, session, status, batchSize, offset)
			if err != nil {
				return nil, 0, err
			}
			if len(progressEntries) == 0 {
				break
			}

			items, err := h.hydrateProgressItems(ctx, session, progressEntries, query.requestedFields, libraryID)
			if err != nil {
				return nil, 0, err
			}
			for _, item := range items {
				if len(result) >= query.limit {
					break
				}
				result = append(result, item.dto)
			}
			if len(result) >= query.limit || len(progressEntries) < batchSize {
				break
			}
			offset += len(progressEntries)
		}
		return result, 0, nil
	}

	batchSize := min(max(query.limit*3, 48), 200)
	if batchSize <= 0 {
		batchSize = 48
	}

	items := make([]baseItemDTO, 0, query.limit)
	matchedCount := 0
	offset := 0

	for {
		progressEntries, err := h.userData.ListProgress(ctx, session, status, batchSize, offset)
		if err != nil {
			return nil, 0, err
		}
		if len(progressEntries) == 0 {
			break
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
				items = append(items, item.dto)
			}
			matchedCount++
		}

		offset += len(progressEntries)
		if len(progressEntries) < batchSize {
			break
		}
		if !query.enableTotalRecordCount && len(items) >= query.limit {
			break
		}
	}

	total := 0
	if query.enableTotalRecordCount {
		total = matchedCount
	}
	return items, total, nil
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
				itemType: strings.ToLower(item.Type),
				dto:      dto,
			})
			continue
		}
		if target, ok := episodesByID[entry.MediaItemID]; ok {
			dto := h.mapper.itemFromList(target.Item, favorites[entry.MediaItemID], &entry, fields)
			h.applyCompatEpisodeTarget(&dto, target)
			stubDetailListFields(&dto, fields)
			result = append(result, progressHydratedItem{
				itemType: "episode",
				dto:      dto,
			})
		}
	}

	return result, nil
}

func (h *ItemsHandler) resolveAccessFilter(ctx context.Context, session *Session) catalog.AccessFilter {
	if h.accessFilter != nil {
		return h.accessFilter(ctx, session.StreamAppUserID, session.ProfileID)
	}
	return catalog.AccessFilter{}
}

func (h *ItemsHandler) presignCompatListItem(ctx context.Context, item *upstreamListItem) {
	if item.PosterPath == "" {
		item.PosterPath = item.PosterURL
	}
	if item.BackdropPath == "" {
		item.BackdropPath = item.BackdropURL
	}
	if item.LogoPath == "" {
		item.LogoPath = item.LogoURL
	}
	if item.StillPath == "" {
		item.StillPath = item.StillURL
	}
	item.PosterURL = h.presignCompatImagePath(ctx, item.PosterURL, "poster")
	item.BackdropURL = h.presignCompatImagePath(ctx, item.BackdropURL, "backdrop")
	item.LogoURL = h.presignCompatImagePath(ctx, item.LogoURL, "logo")
	item.StillURL = h.presignCompatImagePath(ctx, item.StillURL, "still")
}

func (h *ItemsHandler) presignCompatImagePath(ctx context.Context, path, imageType string) string {
	return compatPresignImage(h.detailSvc, ctx, path, imageType, compatCardImageSize)
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
	if errors.Is(err, playback.ErrTooManyStreams) {
		writeError(w, http.StatusTooManyRequests, "TooManyStreams", "Too many concurrent streams")
		return
	}
	if errors.Is(err, playback.ErrTooManyTranscodes) {
		writeError(w, http.StatusTooManyRequests, "TooManyTranscodes", "Too many concurrent transcodes")
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
