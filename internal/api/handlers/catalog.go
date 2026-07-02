package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// audiobookGroupsCacheTTL matches the client's React Query staleTime: the
// grouped author/narrator browse is cached server-side for the same window so
// the sequential page fetches and a quick refresh reuse one aggregation.
const audiobookGroupsCacheTTL = 60 * time.Second

type CatalogHandler struct {
	resolver    *catalog.CatalogResolver
	itemsH      *ItemsHandler
	workSummary catalog.WorkSummaryProvider

	groupsCacheOnce sync.Once
	groupsCache     *catalog.AudiobookGroupsCache
}

// audiobookGroups returns the lazily-initialized grouped-browse cache. Built on
// first use (only the route-mounted singleton handler serves audiobook-groups)
// so the per-request CatalogHandler instances never spawn a cache sweeper.
func (h *CatalogHandler) audiobookGroups() *catalog.AudiobookGroupsCache {
	h.groupsCacheOnce.Do(func() {
		h.groupsCache = catalog.NewAudiobookGroupsCache(h.itemsH.browseRepo.Pool(), audiobookGroupsCacheTTL)
	})
	return h.groupsCache
}

func NewCatalogHandler(resolver *catalog.CatalogResolver, itemsH *ItemsHandler) *CatalogHandler {
	return &CatalogHandler{
		resolver: resolver,
		itemsH:   itemsH,
	}
}

func (h *CatalogHandler) SetWorkSummaryProvider(provider catalog.WorkSummaryProvider) {
	h.workSummary = provider
}

type catalogResponse struct {
	Total             int                `json:"total"`
	TotalExact        bool               `json:"total_exact"`
	HasMore           bool               `json:"has_more"`
	Items             []itemListResponse `json:"items"`
	Snapshot          string             `json:"snapshot,omitempty"`
	SearchDiagnostics *searchDiagnostics `json:"search_diagnostics,omitempty"`
}

// searchDiagnostics is an additive, per-query observability object emitted on
// /api/v1/catalog only when a relevance-sorted search actually ran through a
// CatalogSearchProvider. mode/semantic_used reflect POST-downgrade reality
// (a hybrid request that fell back to keyword reports mode="keyword",
// semantic_used=false). fallback_reason and index_pending_updates are omitted
// when empty.
type searchDiagnostics struct {
	Provider            string `json:"provider"`
	Mode                string `json:"mode"`
	SemanticUsed        bool   `json:"semantic_used"`
	FallbackReason      string `json:"fallback_reason,omitempty"`
	IndexPendingUpdates int    `json:"index_pending_updates,omitempty"`
}

type catalogFiltersResponse struct {
	Genres            []string  `json:"genres"`
	Studios           []string  `json:"studios"`
	Networks          []string  `json:"networks"`
	Countries         []string  `json:"countries"`
	OriginalLanguages []string  `json:"original_languages"`
	ContentRatings    []string  `json:"content_ratings"`
	Authors           []string  `json:"authors"`
	Narrators         []string  `json:"narrators"`
	Series            []string  `json:"series"`
	Resolutions       *[]string `json:"resolutions,omitempty"`
	AudioLanguages    *[]string `json:"audio_languages,omitempty"`
	SubtitleLanguages *[]string `json:"subtitle_languages,omitempty"`
}

func (h *CatalogHandler) HandleGetCatalog(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	req, err := catalog.ParseCatalogRequest(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	accessFilter := h.itemsH.accessFilter(r)
	groupedByWork := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("group")), "work")
	if groupedByWork {
		result, entries, err := h.resolveGroupedCatalogByWork(r, req, accessFilter)
		if err != nil {
			if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "Catalog source not found")
				return
			}
			slog.Error("catalog: resolve grouped by work failed", "err_msg", err.Error())
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve catalog")
			return
		}

		resultItems := groupedCatalogItems(entries)
		items := h.catalogItemResponses(r, resultItems, catalog.NormalizeQuerySort(req.Query.Sort).Field, accessFilter)
		for i := range items {
			if i < len(entries) && entries[i].summary != nil {
				applyWorkSummaryToCatalogItem(&items[i], entries[i].summary)
			}
		}
		h.writeCatalogResponse(w, result, items, groupedByWork)
		return
	}

	result, err := h.resolver.Resolve(r.Context(), req, accessFilter)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Catalog source not found")
			return
		}
		slog.Error("catalog: resolve failed", "err_msg", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve catalog")
		return
	}
	items := h.catalogItemResponses(r, result.Items, catalog.NormalizeQuerySort(req.Query.Sort).Field, accessFilter)
	h.writeCatalogResponse(w, result, items, groupedByWork)
}

func (h *CatalogHandler) catalogItemResponses(r *http.Request, resultItems []*models.MediaItem, sortField string, accessFilter catalog.AccessFilter) []itemListResponse {
	var (
		localizedItems   []*models.MediaItem
		overlaySummaries map[string]*models.OverlaySummary
		userStates       map[string]*itemUserStateResponse
		episodeMetadata  map[string]struct {
			SeriesTitle   string
			SeasonNumber  *int
			EpisodeNumber *int
		}
	)
	var enrichWG sync.WaitGroup
	enrichWG.Add(4)
	go func() {
		defer enrichWG.Done()
		localizedItems = h.itemsH.localizeItemListModels(r.Context(), resultItems, accessFilter)
	}()
	go func() {
		defer enrichWG.Done()
		overlaySummaries = h.itemsH.listOverlaySummaries(r.Context(), resultItems, accessFilter)
	}()
	go func() {
		defer enrichWG.Done()
		userStates = h.itemsH.listItemUserStates(r, resultItems)
	}()
	go func() {
		defer enrichWG.Done()
		episodeMetadata = h.itemsH.listEpisodeBrowseMetadata(r.Context(), resultItems)
	}()
	enrichWG.Wait()

	var (
		imageURLs   map[string]itemListImageURLs
		sortMetrics map[string]*sortMetricsResponse
	)
	store, profileID, _ := h.itemsH.userStoreForRequest(r)
	var responseWG sync.WaitGroup
	responseWG.Add(2)
	go func() {
		defer responseWG.Done()
		imageURLs = h.itemsH.itemListCardImageURLs(r.Context(), localizedItems)
	}()
	go func() {
		defer responseWG.Done()
		sortMetrics = h.itemsH.listSortMetrics(
			r.Context(),
			resultItems,
			sortField,
			accessFilter,
			overlaySummaries,
			store,
			apimw.GetUserID(r.Context()),
			profileID,
		)
	}()
	responseWG.Wait()

	items := make([]itemListResponse, 0, len(localizedItems))
	for _, item := range localizedItems {
		if item == nil {
			continue
		}
		resp := itemListResponseShell(item, overlaySummaries[item.ContentID], userStates[item.ContentID])
		if meta, ok := episodeMetadata[item.ContentID]; ok {
			resp.SeriesTitle = meta.SeriesTitle
			resp.SeasonNumber = meta.SeasonNumber
			resp.EpisodeNumber = meta.EpisodeNumber
		}
		resp.PosterURL = imageURLs[item.ContentID].posterURL
		resp.BackdropURL = imageURLs[item.ContentID].backdropURL
		resp.SortMetrics = sortMetrics[item.ContentID]
		items = append(items, resp)
	}
	return items
}

func (h *CatalogHandler) writeCatalogResponse(w http.ResponseWriter, result *catalog.CatalogResult, items []itemListResponse, groupedByWork bool) {
	var snapshot string
	if !result.SnapshotAt.IsZero() {
		snapshot = result.SnapshotAt.Format(time.RFC3339Nano)
	}

	// A non-empty Provider is the single gate: only the direct-search path sets
	// it. Browse / preview / non-relevance-sort q= (which never run a provider)
	// and group=work (fresh CatalogResult with empty Provider) all omit it.
	var diag *searchDiagnostics
	if result.Provider != "" {
		diag = &searchDiagnostics{
			Provider:            result.Provider,
			Mode:                result.Mode,
			SemanticUsed:        result.SemanticUsed,
			FallbackReason:      result.FallbackReason,
			IndexPendingUpdates: result.IndexPendingEvents,
		}
	}

	writeJSON(w, http.StatusOK, catalogResponse{
		Total:             result.Total,
		TotalExact:        result.TotalExact && !groupedByWork,
		HasMore:           result.HasMore,
		Items:             items,
		Snapshot:          snapshot,
		SearchDiagnostics: diag,
	})
}

type groupedCatalogEntry struct {
	item    *models.MediaItem
	summary *catalog.WorkSummary
}

func (h *CatalogHandler) resolveGroupedCatalogByWork(r *http.Request, req catalog.CatalogRequest, accessFilter catalog.AccessFilter) (*catalog.CatalogResult, []groupedCatalogEntry, error) {
	fetchReq := req
	fetchReq.Offset = 0
	fetchReq.Limit = groupedCatalogFetchLimit(req.Limit)
	fetchReq.SkipTotal = true

	seen := map[string]struct{}{}
	entries := make([]groupedCatalogEntry, 0, req.Limit+1)
	groupIndex := 0
	var snapshot time.Time

	for {
		result, err := h.resolver.Resolve(r.Context(), fetchReq, accessFilter)
		if err != nil {
			return nil, nil, err
		}
		if snapshot.IsZero() {
			snapshot = result.SnapshotAt
			if !snapshot.IsZero() {
				fetchReq.SnapshotAt = &snapshot
			}
		}
		summaries, err := h.workSummariesForItems(r, result.Items, accessFilter)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range result.Items {
			summary := summaries[item.ContentID]
			key := groupedCatalogEntryKey(item, summary)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if groupIndex >= req.Offset {
				entries = append(entries, groupedCatalogEntry{item: item, summary: summary})
			}
			groupIndex++
			if len(entries) > req.Limit {
				break
			}
		}
		if len(entries) > req.Limit || !result.HasMore || len(result.Items) == 0 {
			break
		}
		fetchReq.Offset += len(result.Items)
	}

	hasMore := len(entries) > req.Limit
	if hasMore {
		entries = entries[:req.Limit]
	}
	total := req.Offset + len(entries)
	if hasMore {
		total++
	}
	result := &catalog.CatalogResult{
		Items:      groupedCatalogItems(entries),
		Total:      total,
		HasMore:    hasMore,
		TotalExact: false,
		SnapshotAt: snapshot,
	}
	return result, entries, nil
}

func groupedCatalogFetchLimit(limit int) int {
	switch {
	case limit <= 0:
		return 100
	case limit >= 100:
		return 100
	default:
		return min(100, limit*4+10)
	}
}

func (h *CatalogHandler) workSummariesForItems(r *http.Request, items []*models.MediaItem, filter catalog.AccessFilter) (map[string]*catalog.WorkSummary, error) {
	summaries := map[string]*catalog.WorkSummary{}
	if h == nil || h.workSummary == nil || len(items) == 0 {
		return summaries, nil
	}
	contentIDs := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		if !catalogItemCanGroupByWork(item) {
			continue
		}
		if _, ok := seen[item.ContentID]; ok {
			continue
		}
		seen[item.ContentID] = struct{}{}
		contentIDs = append(contentIDs, item.ContentID)
	}
	if len(contentIDs) == 0 {
		return summaries, nil
	}
	if batch, ok := h.workSummary.(catalog.WorkSummaryBatchProvider); ok {
		return batch.ListSummariesForContentIDs(r.Context(), contentIDs, filter)
	}
	for _, contentID := range contentIDs {
		summary, err := h.workSummary.GetSummaryForContentID(r.Context(), contentID, filter)
		if err != nil {
			return nil, err
		}
		if summary != nil && summary.WorkID != "" {
			summaries[contentID] = summary
		}
	}
	return summaries, nil
}

func groupedCatalogItems(entries []groupedCatalogEntry) []*models.MediaItem {
	items := make([]*models.MediaItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.item)
	}
	return items
}

func groupedCatalogEntryKey(item *models.MediaItem, summary *catalog.WorkSummary) string {
	if catalogItemCanGroupByWork(item) && summary != nil && summary.WorkID != "" {
		return "work:" + summary.WorkID
	}
	if item == nil {
		return "item:"
	}
	return "item:" + item.ContentID
}

func catalogItemCanGroupByWork(item *models.MediaItem) bool {
	return item != nil && (item.Type == "ebook" || item.Type == "audiobook")
}

func applyWorkSummaryToCatalogItem(item *itemListResponse, summary *catalog.WorkSummary) {
	if item == nil || summary == nil || summary.WorkID == "" {
		return
	}
	item.Type = "work"
	item.WorkID = summary.WorkID
	item.WorkTitle = summary.Title
	item.WorkFormats = summary.Formats
	if summary.Title != "" {
		item.Title = summary.Title
	}
}

func (h *CatalogHandler) HandleGetCatalogFilters(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	req, err := catalog.ParseCatalogRequest(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	includeTechnical := parseIncludeTechnical(r.URL.Query().Get("include_technical"))
	filters, err := h.resolver.ListFiltersWithOptions(
		r.Context(),
		req,
		h.itemsH.accessFilter(r),
		catalog.CatalogFilterOptions{IncludeTechnical: includeTechnical},
	)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list catalog filters")
		return
	}

	var resolutions *[]string
	var audioLanguages *[]string
	var subtitleLanguages *[]string
	if includeTechnical {
		resolutions = &filters.Resolutions
		audioLanguages = &filters.AudioLanguages
		subtitleLanguages = &filters.SubtitleLanguages
	}

	writeJSON(w, http.StatusOK, catalogFiltersResponse{
		Genres:            filters.Genres,
		Studios:           filters.Studios,
		Networks:          filters.Networks,
		Countries:         filters.Countries,
		OriginalLanguages: filters.OriginalLanguages,
		ContentRatings:    filters.ContentRatings,
		Authors:           filters.Authors,
		Narrators:         filters.Narrators,
		Series:            filters.Series,
		Resolutions:       resolutions,
		AudioLanguages:    audioLanguages,
		SubtitleLanguages: subtitleLanguages,
	})
}

// catalogFacetSearchResponse mirrors catalog.CatalogFacetSearchResult on
// the wire. matches[] is always present (empty when no hits); has_more
// is true when the underlying result set held more entries than the
// requested limit.
type catalogFacetSearchResponse struct {
	Matches []string `json:"matches"`
	HasMore bool     `json:"has_more"`
}

// HandleGetCatalogFacetSearch — GET /api/v1/catalog/filters/search
//
// Prefix-typeahead for the high-cardinality filter facets (authors /
// narrators / series, plus genre / studio / network / country /
// original_language / content_rating for consistency). Query
// parameters: same as /api/v1/catalog/filters for scope (source,
// library_id, etc.), plus facet=<name>, q=<prefix>, limit=<N>.
//
// The bulk /api/v1/catalog/filters endpoint stays as the source for
// the initial dropdown render (top 1000 alphabetical); this endpoint
// takes over once the user starts typing.
func (h *CatalogHandler) HandleGetCatalogFacetSearch(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.resolver == nil || h.itemsH == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	req, err := catalog.ParseCatalogRequest(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	facet := strings.TrimSpace(r.URL.Query().Get("facet"))
	if facet == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "facet parameter is required")
		return
	}
	prefix := r.URL.Query().Get("q")

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		if n > 100 {
			n = 100
		}
		limit = n
	}

	result, err := h.resolver.SearchFacet(
		r.Context(),
		req,
		h.itemsH.accessFilter(r),
		facet,
		prefix,
		limit,
	)
	if err != nil {
		if errors.Is(err, catalog.ErrInvalidCatalogRequest) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to search catalog facet")
		return
	}

	matches := result.Matches
	if matches == nil {
		matches = []string{}
	}
	writeJSON(w, http.StatusOK, catalogFacetSearchResponse{
		Matches: matches,
		HasMore: result.HasMore,
	})
}

func parseIncludeTechnical(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	includeTechnical, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return includeTechnical
}

func (h *CatalogHandler) HandlePostCatalogQuery(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.itemsH == nil || h.itemsH.browseRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	var req filterItemsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	fb := sections.NewFilterBuilder("mi")
	filterWhere, filterArgs, err := fb.Build(req.FilterConfig)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid filter: "+err.Error())
		return
	}

	accessFilter := h.itemsH.accessFilter(r)
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

	disabledLibraryIDs := accessFilter.DisabledLibraryIDs
	fromClause := "media_items mi"
	if libraryIDs != nil || req.LibraryID > 0 || len(disabledLibraryIDs) > 0 {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}

	if req.LibraryID > 0 {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, req.LibraryID)
		argIdx++
	}

	if libraryIDs != nil {
		if len(libraryIDs) == 0 {
			writeJSON(w, http.StatusOK, browseResponse{Items: []itemListResponse{}, Total: 0})
			return
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, libraryIDs)
		argIdx++
	}
	if len(disabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, disabledLibraryIDs)
		argIdx++
	}
	catalog.ApplySectionAccessFilter("mi", catalog.AccessFilter{MaxContentRating: accessFilter.MaxContentRating}, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	sortClause := "ORDER BY mi.created_at DESC"
	if req.Sort != "" {
		sortClause = filterSortClause(req.Sort, req.Order)
	}

	// Single-pass query: COUNT(*) OVER () returns the same total on every
	// row of the filtered set, so we read it once from the first scanned row
	// instead of firing a separate SELECT COUNT(*).
	query := fmt.Sprintf(
		`SELECT %s, COUNT(*) OVER () AS total_count FROM %s %s %s LIMIT $%d OFFSET $%d`,
		filterItemColumns("mi"), fromClause, whereClause, sortClause, argIdx, argIdx+1,
	)
	args = append(args, req.Limit, req.Offset)

	rows, err := h.itemsH.browseRepo.Pool().Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to query items")
		return
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
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan item")
			return
		}
		modelItems = append(modelItems, &item)
		total = rowTotal
	}

	// COUNT(*) OVER () emits no rows when the data SELECT is empty, so total
	// stays 0 even when the broader result set has matching rows (e.g. OFFSET
	// past the last page). Re-query the count to give callers the real total.
	// Skip when offset == 0 because in that case an empty page genuinely means
	// total = 0. Mirrors the fallback in browse.go / query_executor.go /
	// favorites_browse.go / item_repo.go Search.
	if len(modelItems) == 0 && req.Offset > 0 {
		countQuery := fmt.Sprintf(
			"SELECT COUNT(*) FROM (SELECT 1 FROM %s %s) sub",
			fromClause, whereClause,
		)
		// Drop the trailing limit, offset args from the data query.
		countArgs := args[:len(args)-2]
		if err := h.itemsH.browseRepo.Pool().QueryRow(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count items")
			return
		}
	}

	userStates := h.itemsH.listItemUserStates(r, modelItems)
	items := make([]itemListResponse, 0, len(modelItems))
	for _, item := range modelItems {
		if h.itemsH.detailSvc != nil {
			if localized, locErr := h.itemsH.detailSvc.LocalizeItemModel(r.Context(), item, accessFilter); locErr == nil && localized != nil {
				item = localized
			}
		}
		items = append(items, h.itemsH.toItemListResponseWithOverlay(r, item, nil, userStates[item.ContentID]))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   total,
		HasMore: req.Offset+req.Limit < total,
		Items:   items,
	})
}

func (h *CatalogHandler) HandleLegacySearch(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.itemsH == nil || h.itemsH.itemRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	if values, ok := buildLegacySearchCatalogValues(r.URL.Query()); ok && h.resolver != nil {
		h.itemsH.writeCatalogBrowseResponse(w, r, values)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Search query 'q' is required")
		return
	}

	limit := catalog.ParseIntParam(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := max(catalog.ParseIntParam(r.URL.Query().Get("offset")), 0)

	items, total, err := h.itemsH.itemRepo.Search(r.Context(), query, parseSearchTypes(r.URL.Query()["type"]), limit, offset, h.itemsH.accessFilter(r))
	if err != nil {
		slog.Error("search failed", "query", query, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Search failed")
		return
	}

	userStates := h.itemsH.listItemUserStates(r, items)
	resp := make([]itemListResponse, 0, len(items))
	for _, item := range items {
		resp = append(resp, h.itemsH.toItemListResponseWithOverlay(r, item, nil, userStates[item.ContentID]))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   total,
		HasMore: offset+len(resp) < total,
		Items:   resp,
	})
}
