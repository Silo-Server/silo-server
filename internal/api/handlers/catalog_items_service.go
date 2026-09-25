package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Catalog item seams: the profile-scoped catalog browse and item reads the
// v1 handlers and the v2 operations share. Each takes the caller as an
// ItemViewer (resolved access filter plus declared profile), returns the
// view the v1 handler writes verbatim, and reports failure as an *APIError
// carrying the v1 status and message. A canceled request context is
// returned as-is so the caller can stay silent, as v1 does.

// CatalogBrowseView is one page of the catalog browse.
type CatalogBrowseView = catalogResponse

// SearchDiagnosticsView is the per-query search observability object.
type SearchDiagnosticsView = searchDiagnostics

// EffectiveSortView is the order a saved/default sort resolved to.
type EffectiveSortView = effectiveSortResponse

// SortMetricsView is what a sorted listing sorted on.
type SortMetricsView = sortMetricsResponse

// CatalogFiltersView is the facet document of a catalog scope.
type CatalogFiltersView = catalogFiltersResponse

// CatalogFacetSearchView is a facet typeahead answer.
type CatalogFacetSearchView = catalogFacetSearchResponse

// CatalogQueryView is one page of the JSON-body catalog query.
type CatalogQueryView = browseResponse

// CatalogQueryRequest is the JSON-body catalog query.
type CatalogQueryRequest = filterItemsRequest

// AudiobookGroupsView is one page of grouped audiobooks.
type AudiobookGroupsView = audiobookGroupsResponse

// AudiobookGroupView is one author, narrator, or series group.
type AudiobookGroupView = audiobookGroupResponse

// EpisodeView is one episode row.
type EpisodeView = episodeResponse

// EpisodeFileView is one file of an episode row.
type EpisodeFileView = episodeFileResponse

// SeasonView is one season row.
type SeasonView = seasonResponse

// specialsSeasonTitle names season 0.
const specialsSeasonTitle = "Specials"

// Browse answers one page of the catalog: the resolver's page enriched
// into cards, grouped by work when asked.
func (h *CatalogHandler) Browse(ctx context.Context, v ItemViewer, req catalog.CatalogRequest, groupedByWork bool) (CatalogBrowseView, error) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		return CatalogBrowseView{}, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	if req.CursorPaging {
		// A distant window seek may scan a large sorted prefix. Bound the whole
		// resolve/enrichment operation and preserve earlier client cancellation.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	if groupedByWork {
		result, entries, err := h.resolveGroupedCatalogByWork(ctx, req, v.Access)
		if err != nil {
			return CatalogBrowseView{}, catalogResolveError(ctx, err, true)
		}
		resultItems := groupedCatalogItems(entries)
		items := h.catalogItemResponses(ctx, v, resultItems, catalogSortMetricField(req, result), playableTargetLibraryIDs(req), v.Access)
		for i := range items {
			if i < len(entries) && entries[i].summary != nil {
				applyWorkSummaryToCatalogItem(&items[i], entries[i].summary)
			}
		}
		return catalogBrowseView(result, items), nil
	}
	result, err := h.resolver.Resolve(ctx, req, v.Access)
	if err != nil {
		return CatalogBrowseView{}, catalogResolveError(ctx, err, false)
	}
	items := h.catalogItemResponses(ctx, v, result.Items, catalogSortMetricField(req, result), playableTargetLibraryIDs(req), v.Access)
	return catalogBrowseView(result, items), nil
}

// catalogResolveError keeps grouped and ordinary catalog resolution on the
// same error contract. A grouped search still runs through the bounded
// PostgreSQL path and must report search_timeout rather than hiding a
// deadline behind the generic 500. Superseded live queries cancel their
// request context: there is no client left, so the error passes through
// untouched and the caller writes nothing.
func catalogResolveError(ctx context.Context, err error, groupedByWork bool) error {
	if errors.Is(err, catalog.ErrSearchWindowUnsupported) {
		return apiError(http.StatusNotImplemented, "search_window_unsupported", "The configured search index result window is unsupported")
	}
	if errors.Is(err, catalog.ErrSearchContinuationUnavailable) {
		return apiError(http.StatusServiceUnavailable, "search_continuation_unavailable", "Search continuation storage is unavailable")
	}

	if errors.Is(err, catalog.ErrCatalogStorageUnsupported) {
		return apiError(http.StatusServiceUnavailable, "catalog_storage_unsupported", err.Error())
	}
	if errors.Is(err, catalog.ErrCatalogCursorChanged) {
		return apiError(http.StatusBadRequest, "catalog_cursor_changed", err.Error())
	}
	if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
		return apiError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Catalog source not found")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		slog.WarnContext(ctx, "catalog: search deadline exceeded", "component", "api")
		return apiError(http.StatusGatewayTimeout, "search_timeout", "Search took too long and was stopped")
	}
	slog.ErrorContext(ctx, "catalog: resolve failed", "component", "api", "grouped_by_work", groupedByWork, "err_msg", err.Error())
	return apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve catalog")
}

func catalogBrowseView(result *catalog.CatalogResult, items []itemListResponse) CatalogBrowseView {
	var snapshot string
	if !result.SnapshotAt.IsZero() {
		snapshot = result.SnapshotAt.Format(time.RFC3339Nano)
	}
	// Search diagnostics are present only when the resolver used a search provider.
	var diag *searchDiagnostics
	if result.Provider != "" {
		diag = &searchDiagnostics{Provider: result.Provider, Mode: result.Mode, SemanticUsed: result.SemanticUsed, FallbackReason: result.FallbackReason, IndexPendingUpdates: result.IndexPendingEvents, ResultWindowLimit: result.ResultWindowLimit, SessionExpiresAt: result.SessionExpiresAt}
	}
	var effectiveSort *effectiveSortResponse
	if field := strings.TrimSpace(result.EffectiveSort.Field); field != "" {
		effectiveSort = &effectiveSortResponse{Field: field, Order: result.EffectiveSort.Order}
	}
	var resolvedSort *catalog.QuerySort
	if result.EffectiveSortResolved {
		resolvedSort = new(result.EffectiveSort)
	}
	return CatalogBrowseView{
		ResolvedSort:      resolvedSort,
		Next:              result.Next,
		CursorScope:       result.CursorScope,
		Total:             result.Total,
		TotalExact:        result.TotalExact,
		HasMore:           result.HasMore,
		Items:             items,
		Snapshot:          snapshot,
		SearchDiagnostics: diag,
		EffectiveSort:     effectiveSort,
	}
}

// Filters answers the facet document of a catalog scope.
func (h *CatalogHandler) Filters(ctx context.Context, v ItemViewer, req catalog.CatalogRequest, includeTechnical bool) (CatalogFiltersView, error) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		return CatalogFiltersView{}, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	filters, err := h.resolver.ListFiltersWithOptions(ctx, req, v.Access, catalog.CatalogFilterOptions{IncludeTechnical: includeTechnical})
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			return CatalogFiltersView{}, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		return CatalogFiltersView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list catalog filters")
	}
	view := CatalogFiltersView{
		Genres: filters.Genres, Studios: filters.Studios, Networks: filters.Networks, Countries: filters.Countries,
		OriginalLanguages: filters.OriginalLanguages, ContentRatings: filters.ContentRatings,
		Authors: filters.Authors, Narrators: filters.Narrators, Series: filters.Series,
	}
	if includeTechnical {
		view.Resolutions = &filters.Resolutions
		view.AudioLanguages = &filters.AudioLanguages
		view.SubtitleLanguages = &filters.SubtitleLanguages
	}
	return view, nil
}

// SearchFacet answers a prefix typeahead over one filter facet. limit is
// already validated and clamped by the caller.
func (h *CatalogHandler) SearchFacet(ctx context.Context, v ItemViewer, req catalog.CatalogRequest, facet, prefix string, limit int) (CatalogFacetSearchView, error) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		return CatalogFacetSearchView{}, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	result, err := h.resolver.SearchFacet(ctx, req, v.Access, facet, prefix, limit)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			return CatalogFacetSearchView{}, apiError(http.StatusBadRequest, "bad_request", err.Error())
		}
		return CatalogFacetSearchView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to search catalog facet")
	}
	matches := result.Matches
	if matches == nil {
		matches = []string{}
	}
	return CatalogFacetSearchView{Matches: matches, HasMore: result.HasMore}, nil
}

// QueryItems answers one page of the JSON-body catalog query. The limit is
// already clamped by the caller; the filter body is validated here.
func (h *CatalogHandler) QueryItems(ctx context.Context, v ItemViewer, req CatalogQueryRequest) (CatalogQueryView, error) {
	if h == nil || h.itemsH == nil || h.itemsH.browseRepo == nil {
		return CatalogQueryView{}, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	fb := sections.NewFilterBuilder("mi")
	filterWhere, filterArgs, err := fb.Build(req.FilterConfig)
	if err != nil {
		return CatalogQueryView{}, apiError(http.StatusBadRequest, "bad_request", "Invalid filter: "+err.Error())
	}
	accessFilter := v.Access
	if req.LibraryID > 0 {
		accessFilter.PresentationLibraryID = &req.LibraryID
	}
	libraryIDs := accessFilter.AllowedLibraryIDs

	var conditions []string
	args := filterArgs
	argIdx := fb.ArgIdx()
	if filterWhere != "" {
		conditions = append(conditions, filterWhere)
	}
	fromClause := "media_items mi"
	libraryConditions, libraryArgs, nextArgIdx, earlyEmpty := buildPostCatalogLibraryScope(req.LibraryID, libraryIDs, accessFilter.DisabledLibraryIDs, argIdx)
	if earlyEmpty {
		return CatalogQueryView{Items: []itemListResponse{}, Total: 0}, nil
	}
	conditions = append(conditions, libraryConditions...)
	args = append(args, libraryArgs...)
	argIdx = nextArgIdx
	catalog.ApplySectionAccessFilter("mi", catalog.AccessFilter{MaturityLimits: accessFilter.MaturityLimits}, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}
	sortClause := "ORDER BY mi.created_at DESC"
	if req.Sort != "" {
		sortClause = filterSortClause(req.Sort, req.Order)
	}
	// Single-pass query: COUNT(*) OVER () returns the same total on every
	// row of the filtered set, so it is read once from the first scanned row
	// instead of firing a separate SELECT COUNT(*).
	query := fmt.Sprintf(`SELECT %s, COUNT(*) OVER () AS total_count FROM %s %s %s LIMIT $%d OFFSET $%d`,
		filterItemColumns("mi"), fromClause, whereClause, sortClause, argIdx, argIdx+1)
	args = append(args, req.Limit, req.Offset)

	rows, err := h.itemsH.browseRepo.Pool().Query(ctx, query, args...)
	if err != nil {
		return CatalogQueryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to query items")
	}
	defer rows.Close()

	var total int
	modelItems := make([]*models.MediaItem, 0)
	for rows.Next() {
		var item models.MediaItem
		var rowTotal int
		scanErr := rows.Scan(
			&item.ContentID, &item.Type, &item.Title, &item.SortTitle, &item.OriginalTitle,
			&item.Year, &item.Genres, &item.ContentRating, &item.Runtime, &item.Overview, &item.Tagline,
			&item.RatingIMDB, &item.RatingTMDB, &item.RatingRTCritic, &item.RatingRTAudience,
			&item.ImdbID, &item.TmdbID, &item.TvdbID,
			&item.PosterPath, &item.PosterThumbhash, &item.BackdropPath, &item.BackdropThumbhash, &item.LogoPath,
			&item.MetadataS3Path, &item.MetadataEtag, &item.SeasonCount,
			&item.Studios, &item.Networks, &item.Countries, &item.FirstAirDate, &item.LastAirDate,
			&item.MatchedAt, &item.Status, &item.CreatedAt, &item.UpdatedAt,
			&rowTotal,
		)
		if scanErr != nil {
			return CatalogQueryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to scan item")
		}
		modelItems = append(modelItems, &item)
		total = rowTotal
	}
	// COUNT(*) OVER () emits no rows when the data SELECT is empty, so total
	// stays 0 even when the broader result set has matching rows (OFFSET past
	// the last page). Re-query the count to give callers the real total.
	if len(modelItems) == 0 && req.Offset > 0 {
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s %s) sub", fromClause, whereClause)
		countArgs := args[:len(args)-2]
		if err := h.itemsH.browseRepo.Pool().QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
			return CatalogQueryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to count items")
		}
	}

	viewer := ItemViewer{Access: accessFilter, ProfileID: v.ProfileID}
	userStates := h.itemsH.listItemUserStates(ctx, viewer, modelItems)
	items := make([]itemListResponse, 0, len(modelItems))
	for _, item := range modelItems {
		if h.itemsH.detailSvc != nil {
			if localized, locErr := h.itemsH.detailSvc.LocalizeItemModel(ctx, item, accessFilter); locErr == nil && localized != nil {
				item = localized
			}
		}
		items = append(items, h.itemsH.toItemListResponseWithOverlay(ctx, viewer, item, nil, userStates[item.ContentID], accessFilter.ImageSize))
	}
	return CatalogQueryView{Total: total, HasMore: req.Offset+req.Limit < total, Items: items}, nil
}

// AudiobookGroups answers one page of authors, narrators, or series of an
// audiobook library with aggregate stats and cover-stack posters. The query
// is already validated by the caller.
func (h *CatalogHandler) AudiobookGroups(ctx context.Context, v ItemViewer, query catalog.AudiobookGroupsQuery) (AudiobookGroupsView, error) {
	if h == nil || h.itemsH == nil || h.itemsH.browseRepo == nil {
		return AudiobookGroupsView{}, apiError(http.StatusInternalServerError, "internal_error", "Catalog is not configured")
	}
	if query.CursorPaging {
		if h.itemsH.storeProvider == nil {
			return AudiobookGroupsView{}, catalogResolveError(ctx, catalog.ErrCatalogStorageUnsupported, false)
		}
		store, err := h.itemsH.storeProvider.ForUser(ctx, v.Access.UserID)
		if err != nil {
			return AudiobookGroupsView{}, catalogResolveError(ctx, err, false)
		}
		if !userstore.HasCatalogSQLState(store) {
			return AudiobookGroupsView{}, catalogResolveError(ctx, catalog.ErrCatalogStorageUnsupported, false)
		}
	}
	result, err := catalog.ListAudiobookGroups(ctx, h.itemsH.browseRepo.Pool(), query, v.Access)
	if err != nil {
		if query.CursorPaging {
			return AudiobookGroupsView{}, catalogResolveError(ctx, err, false)
		}
		return AudiobookGroupsView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list audiobook groups")
	}
	return h.audiobookGroupsView(ctx, result, v.Access), nil
}

// ItemDetail answers the full detail of one item, a synthetic season
// included, enriched with the viewer's state.
func (h *CatalogResourceHandler) ItemDetail(ctx context.Context, v ItemViewer, id string) (*catalog.ItemDetail, error) {
	detail, err := h.items.detailSvc.GetItemDetail(ctx, id, v.Access)
	if err != nil {
		if isNotFound(err) {
			syntheticDetail, syntheticErr := h.syntheticSeasonDetail(ctx, v, id)
			if syntheticErr != nil {
				if isNotFound(syntheticErr) {
					return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
				}
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get item detail")
			}
			h.enrichItemDetail(ctx, v, syntheticDetail)
			h.items.maybeRequestStaleDetailMetadataRefresh(ctx, syntheticDetail)
			return syntheticDetail, nil
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get item detail")
	}
	h.enrichItemDetail(ctx, v, detail)
	h.items.maybeRequestStaleDetailMetadataRefresh(ctx, detail)
	return detail, nil
}

// ItemVersions answers the file versions of one item; a synthetic season
// has none. Paths are stripped for viewers without file-path visibility.
func (h *CatalogResourceHandler) ItemVersions(ctx context.Context, v ItemViewer, id string) ([]catalog.FileVersion, error) {
	versions, err := h.items.detailSvc.GetItemVersions(ctx, id, v.Access)
	if err != nil {
		if isNotFound(err) {
			if _, _, ok := parseSyntheticSeasonID(id); ok {
				return []catalog.FileVersion{}, nil
			}
			return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get item versions")
	}
	if !h.items.canViewFilePaths(ctx) {
		for i := range versions {
			versions[i].FilePath = ""
		}
	}
	return versions, nil
}

// MangaFiles answers the local file listing of a manga series: folder paths
// plus per-chapter file rows. Paths are stripped for viewers without
// file-path visibility, matching ItemVersions; names and sizes remain.
func (h *CatalogResourceHandler) MangaFiles(ctx context.Context, v ItemViewer, id string) (*catalog.MangaSeriesFiles, error) {
	files, err := h.items.detailSvc.GetMangaChapterFiles(ctx, id, v.Access)
	if err != nil {
		if isNotFound(err) {
			return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get manga files")
	}
	if !h.items.canViewFilePaths(ctx) {
		files.FolderPaths = nil
		for i := range files.Files {
			files.Files[i].FilePath = ""
		}
	}
	return files, nil
}

// ItemEpisodes answers the episodes of one season item, stored or synthetic.
func (h *CatalogResourceHandler) ItemEpisodes(ctx context.Context, v ItemViewer, id string) ([]EpisodeView, error) {
	const failed = "Failed to list item episodes"
	if h.items.seasonRepo == nil {
		return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	season, err := h.items.seasonRepo.GetByID(ctx, id)
	if err != nil {
		if !errors.Is(err, catalog.ErrSeasonNotFound) {
			return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
		}
		seriesID, seasonNum, ok := parseSyntheticSeasonID(id)
		if !ok {
			return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		if err := h.ensureSeriesVisible(ctx, v, seriesID, failed); err != nil {
			return nil, err
		}
		episodes, err := h.items.episodeRepo.ListBySeason(ctx, seriesID, seasonNum)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
		}
		if len(episodes) == 0 {
			return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return h.items.buildEpisodeResponses(ctx, v, episodes), nil
	}
	if err := h.ensureSeriesVisible(ctx, v, season.SeriesID, failed); err != nil {
		return nil, err
	}
	episodes, err := h.items.episodeRepo.ListBySeasonID(ctx, season.ContentID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	h.items.maybeRequestStaleSeasonMetadataRefresh(ctx, season.ContentID, episodes)
	return h.items.buildEpisodeResponses(ctx, v, episodes), nil
}

// ensureSeriesVisible is the access and presentation-library check every
// series-scoped read runs first; failed names the 500 message of the caller.
func (h *CatalogResourceHandler) ensureSeriesVisible(ctx context.Context, v ItemViewer, seriesID, failed string) error {
	if err := h.items.itemRepo.EnsureAccessible(ctx, seriesID, v.Access); err != nil {
		if isNotFound(err) {
			return apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	if err := h.items.ensurePresentationLibraryAccess(ctx, seriesID, v.Access); err != nil {
		if isNotFound(err) {
			return apiError(http.StatusNotFound, "not_found", "Item not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	return nil
}

// SeriesSeasons answers the seasons of a series with their episode rollups,
// with optional poster artwork.
func (h *CatalogResourceHandler) SeriesSeasons(ctx context.Context, v ItemViewer, id string, includeArtwork bool) ([]SeasonView, error) {
	return h.seriesSeasons(ctx, v, id, includeArtwork)
}

// seriesSeasons is SeriesSeasons with the season-list artwork switch. A
// text-only selector passes includeArtwork=false to skip every poster URL
// signature and thumbhash; the remaining metadata is unchanged.
func (h *CatalogResourceHandler) seriesSeasons(ctx context.Context, v ItemViewer, id string, includeArtwork bool) ([]SeasonView, error) {
	const failed = "Failed to list seasons"
	if err := h.ensureSeriesVisible(ctx, v, id, failed); err != nil {
		return nil, err
	}
	filter := v.Access
	if h.items.seasonRepo != nil {
		seasons, err := h.items.seasonRepo.ListBySeries(ctx, id)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
		}
		if len(seasons) > 0 {
			if h.items.detailSvc != nil {
				if localized, locErr := h.items.detailSvc.LocalizeSeasonModels(ctx, seasons, filter); locErr == nil && len(localized) == len(seasons) {
					seasons = localized
				}
			}
			episodeCounts, userData, err := h.items.seriesSeasonRollups(ctx, v, id)
			if err != nil {
				return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
			}

			// Sign every season poster in one batch instead of resolving each
			// season again; a text-only selector skips the batch entirely.
			var posterURLs map[string]catalog.ResolvedImageURL
			if includeArtwork && h.items.detailSvc != nil {
				paths := make([]string, 0, len(seasons))
				for _, season := range seasons {
					if episodeCounts[season.SeasonNumber] > 0 && season.PosterPath != "" {
						paths = append(paths, sizedPosterPath(season.PosterPath, filter.ImageSize))
					}
				}
				posterURLs = h.items.detailSvc.PresignURLsWithExpiry(ctx, paths, requestVariantHint("featured", filter.ImageSize))
			}
			resp := make([]seasonResponse, 0, len(seasons))
			for _, s := range seasons {
				episodeCount := episodeCounts[s.SeasonNumber]
				if episodeCount == 0 {
					continue
				}
				// Construct metadata without resolving each season again.
				season := *s
				season.PosterPath = ""
				if !includeArtwork {
					season.PosterThumbhash = ""
				}
				sr := h.items.localizedSeasonResponse(ctx, v, &season, episodeCount, userData[s.SeasonNumber], filter.ImageSize)
				sr.PosterURL = posterURLs[sizedPosterPath(s.PosterPath, filter.ImageSize)].URL
				resp = append(resp, sr)
			}
			h.items.enrichSeasonPlayTargets(ctx, v, id, resp)
			return resp, nil
		}
	}
	summaries, err := h.items.episodeRepo.ListSeasons(ctx, id)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	resp := make([]seasonResponse, 0, len(summaries))
	for _, s := range summaries {
		title := "Season " + strconv.Itoa(s.SeasonNumber)
		if s.SeasonNumber == 0 {
			title = specialsSeasonTitle
		}
		episodes, _ := h.items.episodeRepo.ListBySeason(ctx, id, s.SeasonNumber)
		resp = append(resp, seasonResponse{
			ContentID:    fmt.Sprintf("%s-S%02d", id, s.SeasonNumber),
			SeasonNumber: s.SeasonNumber,
			IsSpecials:   s.SeasonNumber == 0,
			EpisodeCount: s.EpisodeCount,
			Title:        title,
			UserData:     h.items.getAggregateUserData(ctx, v, episodes),
		})
	}
	h.items.enrichSeasonPlayTargets(ctx, v, id, resp)
	return resp, nil
}

// SeriesSeason answers one season of a series by number.
func (h *CatalogResourceHandler) SeriesSeason(ctx context.Context, v ItemViewer, id string, num int) (SeasonView, error) {
	const failed = "Failed to get season"
	if err := h.ensureSeriesVisible(ctx, v, id, failed); err != nil {
		return SeasonView{}, err
	}
	if h.items.seasonRepo != nil {
		season, err := h.items.seasonRepo.GetBySeriesAndNumber(ctx, id, num)
		switch {
		case err == nil:
			episodes, err := h.items.episodeRepo.ListBySeasonID(ctx, season.ContentID)
			if err != nil {
				return SeasonView{}, apiError(http.StatusInternalServerError, "internal_error", failed)
			}
			// The SQL rollup groups by season row, so it answers only when
			// the episodes are linked to this row. The by-number fallback
			// for unlinked episodes folds their progress instead.
			var userData *catalog.SeasonUserData
			rolledUp := false
			if len(episodes) > 0 {
				userData, rolledUp = h.items.parentRollupUserData(ctx, v, "season", season.ContentID)
			} else {
				episodes, err = h.items.episodeRepo.ListBySeason(ctx, id, season.SeasonNumber)
				if err != nil {
					return SeasonView{}, apiError(http.StatusInternalServerError, "internal_error", failed)
				}
			}
			if !rolledUp {
				userData = h.items.getAggregateUserData(ctx, v, episodes)
			}
			h.items.maybeRequestStaleSeasonMetadataRefresh(ctx, season.ContentID, episodes)
			resp := h.items.toSeasonResponseFromEpisodes(ctx, v, id, season, episodes, userData, v.Access.ImageSize)
			h.items.resolveSeasonPlayTarget(ctx, v, id, &resp)
			return resp, nil
		case !errors.Is(err, catalog.ErrSeasonNotFound):
			return SeasonView{}, apiError(http.StatusInternalServerError, "internal_error", failed)
		}
	}
	episodes, err := h.items.episodeRepo.ListBySeason(ctx, id, num)
	if err != nil {
		return SeasonView{}, apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	if len(episodes) == 0 {
		return SeasonView{}, apiError(http.StatusNotFound, "not_found", "Season not found")
	}
	title := "Season " + strconv.Itoa(num)
	if num == 0 {
		title = specialsSeasonTitle
	}
	resp := seasonResponse{
		ContentID:    fmt.Sprintf("%s-S%02d", id, num),
		SeasonNumber: num,
		IsSpecials:   num == 0,
		Title:        title,
		EpisodeCount: len(episodes),
		UserData:     h.items.getAggregateUserData(ctx, v, episodes),
	}
	h.items.resolveSeasonPlayTarget(ctx, v, id, &resp)
	return resp, nil
}

// SeasonEpisodes answers the episodes of a series' season by number.
func (h *CatalogResourceHandler) SeasonEpisodes(ctx context.Context, v ItemViewer, id string, num int) ([]EpisodeView, error) {
	const failed = "Failed to list episodes"
	if err := h.ensureSeriesVisible(ctx, v, id, failed); err != nil {
		return nil, err
	}
	episodes, err := h.items.episodeRepo.ListBySeason(ctx, id, num)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", failed)
	}
	if h.items.seasonRepo != nil {
		if season, seasonErr := h.items.seasonRepo.GetBySeriesAndNumber(ctx, id, num); seasonErr == nil && season != nil {
			h.items.maybeRequestStaleSeasonMetadataRefresh(ctx, season.ContentID, episodes)
		}
	}
	return h.items.buildEpisodeResponses(ctx, v, episodes), nil
}

func (h *CatalogHandler) SearchContinuationCapabilities(ctx context.Context) (catalog.SearchContinuationCapabilities, error) {
	if h == nil || h.resolver == nil {
		return catalog.SearchContinuationCapabilities{}, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Catalog is not configured")
	}
	result, err := h.resolver.SearchContinuationCapabilities(ctx)
	if err != nil {
		return result, catalogResolveError(ctx, err, false)
	}
	return result, nil
}
