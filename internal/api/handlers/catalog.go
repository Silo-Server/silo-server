package handlers

import (
	"context"
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

type CatalogHandler struct {
	resolver    *catalog.CatalogResolver
	itemsH      *ItemsHandler
	workSummary catalog.WorkSummaryProvider
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
	ResolvedSort      *catalog.QuerySort   `json:"-"`
	CursorScope       *catalog.QueryCursor `json:"-"`
	Next              *catalog.QueryCursor `json:"-"`
	Total             int                  `json:"total"`
	TotalExact        bool                 `json:"total_exact"`
	HasMore           bool                 `json:"has_more"`
	Items             []itemListResponse   `json:"items"`
	Snapshot          string               `json:"snapshot,omitempty"`
	SearchDiagnostics *searchDiagnostics   `json:"search_diagnostics,omitempty"`
	// EffectiveSort reports the order a collection or personal-list source
	// actually resolved in after saved/default precedence was applied. Omitted
	// for other sources and when the source kept its own order.
	EffectiveSort *effectiveSortResponse `json:"effective_sort,omitempty"`
}

type effectiveSortResponse struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

// searchDiagnostics is an additive, per-query observability object emitted on
// /api/v1/catalog only when a relevance-sorted search actually ran through a
// CatalogSearchProvider. mode/semantic_used reflect POST-downgrade reality
// (a hybrid request that fell back to keyword reports mode="keyword",
// semantic_used=false). fallback_reason and index_pending_updates are omitted
// when empty.
type searchDiagnostics struct {
	ResultWindowLimit   int        `json:"result_window_limit,omitempty"`
	SessionExpiresAt    *time.Time `json:"session_expires_at,omitempty"`
	Provider            string     `json:"provider"`
	Mode                string     `json:"mode"`
	SemanticUsed        bool       `json:"semantic_used"`
	FallbackReason      string     `json:"fallback_reason,omitempty"`
	IndexPendingUpdates int        `json:"index_pending_updates,omitempty"`
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
	accessFilter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}
	groupedByWork := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("group")), "work")
	view, err := h.Browse(r.Context(), viewerFromRequest(r, accessFilter), req, groupedByWork)
	if err != nil {
		writeCatalogError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// writeCatalogError writes a seam failure; a canceled request has no client
// left, so nothing is written.
func writeCatalogError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	writeAPIError(w, err)
}

func handleCatalogResolveError(w http.ResponseWriter, r *http.Request, err error, groupedByWork bool) {
	writeCatalogError(w, catalogResolveError(r.Context(), err, groupedByWork))
}

// catalogSortMetricField is the field the returned items are actually ordered
// by. When the request carries no sort, the resolver may still apply a saved or
// default sort and report it as EffectiveSort; sort_metrics has to explain the
// ordering the client received, not the empty one it asked for.
func catalogSortMetricField(req catalog.CatalogRequest, result *catalog.CatalogResult) string {
	if result != nil && strings.TrimSpace(result.EffectiveSort.Field) != "" {
		return catalog.NormalizeQuerySort(result.EffectiveSort).Field
	}
	return catalog.NormalizeQuerySort(req.Query.Sort).Field
}

func playableTargetLibraryIDs(req catalog.CatalogRequest) []int {
	if req.LibraryID > 0 {
		return []int{req.LibraryID}
	}
	return req.Query.LibraryIDs
}

func (h *CatalogHandler) catalogItemResponses(ctx context.Context, v ItemViewer, resultItems []*models.MediaItem, sortField string, libraryIDs []int, accessFilter catalog.AccessFilter) []itemListResponse {
	var (
		localizedItems   []*models.MediaItem
		overlaySummaries map[string]*models.OverlaySummary
		userStates       map[string]*itemUserStateResponse
		episodeMetadata  map[string]episodeBrowseMetadata
		playTargets      map[string]string
	)
	var enrichWG sync.WaitGroup
	enrichWG.Add(5)
	go func() {
		defer enrichWG.Done()
		localizedItems = h.itemsH.localizeItemListModels(ctx, resultItems, accessFilter)
	}()
	go func() {
		defer enrichWG.Done()
		overlaySummaries = h.itemsH.listOverlaySummaries(ctx, resultItems, accessFilter)
	}()
	go func() {
		defer enrichWG.Done()
		userStates = h.itemsH.listItemUserStates(ctx, v, resultItems)
	}()
	go func() {
		defer enrichWG.Done()
		episodeMetadata = h.itemsH.listEpisodeBrowseMetadata(ctx, resultItems)
	}()
	go func() {
		defer enrichWG.Done()
		playTargets = h.itemsH.listPlayableTargets(ctx, v, resultItems, libraryIDs, accessFilter)
	}()
	enrichWG.Wait()

	var (
		imageURLs   map[string]itemListImageURLs
		sortMetrics map[string]*sortMetricsResponse
	)
	store, profileID, _ := h.itemsH.viewerUserStore(ctx, v.ProfileID)
	var responseWG sync.WaitGroup
	responseWG.Add(2)
	go func() {
		defer responseWG.Done()
		imageURLs = h.itemsH.itemListCardImageURLs(ctx, localizedItems, accessFilter.ImageSize)
	}()
	go func() {
		defer responseWG.Done()
		sortMetrics = h.itemsH.listSortMetrics(
			ctx,
			resultItems,
			sortField,
			accessFilter,
			overlaySummaries,
			store,
			apimw.GetUserID(ctx),
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
		// The resolver validated the item's own hint against this profile, so
		// its answer replaces the unvalidated one carried by the item.
		resp.PlayContentID = playTargets[playableTargetKeyForItem(item)]
		if meta, ok := episodeMetadata[item.ContentID]; ok {
			applyEpisodeBrowseMetadata(&resp, meta)
		}
		resp.PosterURL = imageURLs[item.ContentID].posterURL
		resp.BackdropURL = imageURLs[item.ContentID].backdropURL
		resp.SortMetrics = sortMetrics[item.ContentID]
		items = append(items, resp)
	}
	return items
}

func applyEpisodeBrowseMetadata(resp *itemListResponse, meta episodeBrowseMetadata) {
	if resp == nil {
		return
	}
	resp.SeriesID = meta.SeriesID
	resp.SeriesTitle = meta.SeriesTitle
	resp.SeasonNumber = meta.SeasonNumber
	resp.EpisodeNumber = meta.EpisodeNumber
}

func (h *CatalogHandler) writeCatalogResponse(w http.ResponseWriter, result *catalog.CatalogResult, items []itemListResponse, groupedByWork bool) {
	writeJSON(w, http.StatusOK, catalogBrowseView(result, items))
}

type groupedCatalogEntry struct {
	item    *models.MediaItem
	summary *catalog.WorkSummary
}

func (h *CatalogHandler) resolveGroupedCatalogByWork(ctx context.Context, req catalog.CatalogRequest, accessFilter catalog.AccessFilter) (*catalog.CatalogResult, []groupedCatalogEntry, error) {
	if req.CursorPaging {
		req.GroupByWork = true
		result, err := h.resolver.Resolve(ctx, req, accessFilter)
		if err != nil {
			return nil, nil, err
		}
		summaries, err := h.workSummariesForItems(ctx, result.Items, accessFilter)
		if err != nil {
			return nil, nil, err
		}
		entries := make([]groupedCatalogEntry, 0, len(result.Items))
		for _, item := range result.Items {
			entries = append(entries, groupedCatalogEntry{item: item, summary: summaries[item.ContentID]})
		}
		return result, entries, nil
	}
	return h.resolveGroupedCatalogByWorkUsing(ctx, req, accessFilter, h.resolver.Resolve)
}

// Grouping still rescans source rows to deduplicate works before applying the
// grouped offset. Advancing raw tuple pages fixes traversal, but does not make
// the grouped result itself a stable keyset.
func (h *CatalogHandler) resolveGroupedCatalogByWorkUsing(ctx context.Context, req catalog.CatalogRequest, accessFilter catalog.AccessFilter, resolve func(context.Context, catalog.CatalogRequest, catalog.AccessFilter) (*catalog.CatalogResult, error)) (*catalog.CatalogResult, []groupedCatalogEntry, error) {
	if req.Seek != nil {
		req.Offset = *req.Seek
	}

	fetchReq := req
	fetchReq.Offset = 0
	fetchReq.Seek = nil
	// A grouped continuation carries source scope only: restarting the raw
	// traversal is necessary to deduplicate works across previous raw pages.
	fetchReq.After = nil
	var cursorScope *catalog.QueryCursor
	if req.After != nil && req.After.Collection != nil {
		collection := *req.After.Collection
		collection.Position = nil
		cursorScope = &catalog.QueryCursor{Collection: &collection}
		fetchReq.After = cursorScope
	}
	fetchReq.Limit = groupedCatalogFetchLimit(req.Limit)
	fetchReq.SkipTotal = true

	seen := map[string]struct{}{}
	entries := make([]groupedCatalogEntry, 0, req.Limit+1)
	groupIndex := 0
	var snapshot time.Time
	// The first page resolves the saved/default sort; every later page is then
	// pinned to it, so a preference edited mid-pagination cannot order the tail
	// of this response differently than the head. Without carrying the sort onto
	// the result below, group=work would also silently drop effective_sort even
	// though the items came back in the saved order.
	var effectiveSort catalog.QuerySort
	firstPage := true

	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		result, err := resolve(ctx, fetchReq, accessFilter)
		if err != nil {
			return nil, nil, err
		}
		if result.CursorScope != nil {
			cursorScope = result.CursorScope
		}
		if firstPage {
			firstPage = false
			effectiveSort = result.EffectiveSort
			frozen := effectiveSort
			fetchReq.ResolvedSort = &frozen
		}
		if snapshot.IsZero() {
			snapshot = result.SnapshotAt
			if !snapshot.IsZero() {
				fetchReq.SnapshotAt = &snapshot
			}
		}
		summaries, err := h.workSummariesForItems(ctx, result.Items, accessFilter)
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
		if len(entries) > req.Limit || !result.HasMore || (len(result.Items) == 0 && result.Next == nil) {
			break
		}
		if result.Next != nil {
			fetchReq.After = result.Next
		} else {
			fetchReq.Offset += len(result.Items)
		}
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
		CursorScope:   cursorScope,
		Items:         groupedCatalogItems(entries),
		Total:         total,
		HasMore:       hasMore,
		TotalExact:    false,
		SnapshotAt:    snapshot,
		EffectiveSort: effectiveSort,
	}
	if hasMore {
		result.Next = cursorScope
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

func (h *CatalogHandler) workSummariesForItems(ctx context.Context, items []*models.MediaItem, filter catalog.AccessFilter) (map[string]*catalog.WorkSummary, error) {
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
		return batch.ListSummariesForContentIDs(ctx, contentIDs, filter)
	}
	for _, contentID := range contentIDs {
		summary, err := h.workSummary.GetSummaryForContentID(ctx, contentID, filter)
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

	accessFilter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}

	view, err := h.Filters(r.Context(), viewerFromRequest(r, accessFilter), req, parseIncludeTechnical(r.URL.Query().Get("include_technical")))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
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

	accessFilter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}

	view, err := h.SearchFacet(r.Context(), viewerFromRequest(r, accessFilter), req, facet, prefix, limit)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
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

	// The filter body is validated before the access filter so an invalid
	// filter and an unresolvable policy answer in the v1 order.
	if _, _, err := sections.NewFilterBuilder("mi").Build(req.FilterConfig); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid filter: "+err.Error())
		return
	}

	accessFilter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}
	view, err := h.QueryItems(r.Context(), viewerFromRequest(r, accessFilter), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func buildPostCatalogLibraryScope(
	libraryID int,
	allowedLibraryIDs []int,
	disabledLibraryIDs []int,
	argIdx int,
) (conditions []string, args []any, nextArgIdx int, earlyEmpty bool) {
	nextArgIdx = argIdx
	if allowedLibraryIDs != nil && len(allowedLibraryIDs) == 0 {
		return nil, nil, nextArgIdx, true
	}

	positiveConditions := []string{"mil_scope_in.content_id = mi.content_id"}
	hasPositiveScope := false
	if libraryID > 0 {
		positiveConditions = append(positiveConditions, fmt.Sprintf("mil_scope_in.media_folder_id = $%d", nextArgIdx))
		args = append(args, libraryID)
		nextArgIdx++
		hasPositiveScope = true
	}
	if allowedLibraryIDs != nil {
		positiveConditions = append(positiveConditions, fmt.Sprintf("mil_scope_in.media_folder_id = ANY($%d)", nextArgIdx))
		args = append(args, allowedLibraryIDs)
		nextArgIdx++
		hasPositiveScope = true
	}
	if hasPositiveScope {
		conditions = append(conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil_scope_in WHERE %s)",
			strings.Join(positiveConditions, " AND "),
		))
	} else if len(disabledLibraryIDs) > 0 {
		conditions = append(conditions,
			"EXISTS (SELECT 1 FROM media_item_libraries mil_scope_any WHERE mil_scope_any.content_id = mi.content_id)",
		)
	}

	if len(disabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM media_item_libraries mil_scope_out WHERE mil_scope_out.content_id = mi.content_id AND mil_scope_out.media_folder_id = ANY($%d))",
			nextArgIdx,
		))
		args = append(args, disabledLibraryIDs)
		nextArgIdx++
	}

	return conditions, args, nextArgIdx, false
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

	accessFilter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}

	items, total, err := h.itemsH.itemRepo.Search(r.Context(), query, parseSearchTypes(r.URL.Query()["type"]), limit, offset, accessFilter)
	if err != nil {
		slog.ErrorContext(r.Context(), "search failed", "component", "api", "query", query, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Search failed")
		return
	}

	userStates := h.itemsH.listItemUserStates(r.Context(), viewerFromRequest(r, accessFilter), items)
	resp := make([]itemListResponse, 0, len(items))
	for _, item := range items {
		resp = append(resp, h.itemsH.toItemListResponseWithOverlay(r.Context(), viewerFromRequest(r, accessFilter), item, nil, userStates[item.ContentID], accessFilter.ImageSize))
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Total:   total,
		HasMore: offset+len(resp) < total,
		Items:   resp,
	})
}
