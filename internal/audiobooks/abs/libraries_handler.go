package abs

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// maxUnpaginatedAuthors / maxUnpaginatedSeries clamp the "return all" forms
// of /libraries/{id}/authors and /libraries/{id}/series. Real ABS returns the
// complete set and typical libraries stay well under these bounds, but silo
// serves libraries real ABS never sees (250k+ books → ~98k authors, ~32k
// series) and an unbounded response crashes non-paginating mobile clients.
// Series is bounded tighter because each row embeds up to four minified book
// objects for the cover stack. Truncation is logged at Warn so it is
// observable instead of silent.
const (
	maxUnpaginatedAuthors = 10000
	maxUnpaginatedSeries  = 2000
)

// ---------------------------------------------------------------------------
// /libraries list + single-library detail
// ---------------------------------------------------------------------------

// handleLibraries — GET /abs/api/libraries (and /api/libraries)
//
// Returns the list of audiobook media_folders. ABS clients call this to
// populate the library picker and to know which library IDs are valid.
func (h *Handler) handleLibraries(w http.ResponseWriter, r *http.Request) {
	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	libs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context(), access)
	if err != nil {
		http.Error(w, "list libraries: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(libs))
	for _, lib := range libs {
		out = append(out, audiobookLibraryMap(lib))
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

// handleLibraryDetail — GET /abs/api/libraries/{libraryId}
func (h *Handler) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	library := audiobookLibraryMap(lib)
	// Real ABS LibraryController.findOne returns the library object DIRECTLY
	// when there is no ?include=filterdata; only the filterdata request wraps
	// it in { filterdata, issues, numUserPlaylists, customMetadataProviders,
	// library }. Returning the wrapped shape unconditionally breaks clients
	// that read library fields off the top level.
	if !includeHas(r.URL.Query().Get("include"), "filterdata") {
		writeJSON(w, http.StatusOK, library)
		return
	}
	// numUserPlaylists drives the bottom-nav "Playlists" tab visibility on the
	// ABS mobile client (BookshelfNavBar.vue gates the tab on it being truthy).
	writeJSON(w, http.StatusOK, map[string]any{
		"filterdata":              h.buildFilterData(r, lib),
		"issues":                  0,
		"numUserPlaylists":        h.countUserPlaylists(r),
		"customMetadataProviders": []any{},
		"library":                 library,
	})
}

// countUserPlaylists returns the playlist count for the authenticated
// caller, or 0 when no auth / no store is wired (open-mode endpoints
// still serve library detail).
func (h *Handler) countUserPlaylists(r *http.Request) int {
	if h.deps.PlaylistStore == nil {
		return 0
	}
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		return 0
	}
	rows, err := h.deps.PlaylistStore.ListUserPlaylists(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		return 0
	}
	return len(rows)
}

// buildFilterData populates the filter sheet payload from the same store
// queries /libraries/{id}/authors and /libraries/{id}/series use. Caps at
// 5000 per kind to keep the response bounded; libraries larger than that
// will paginate via the dedicated /authors and /series endpoints.
//
// Narrators / genres / publishers / languages / tags are left as empty
// arrays for now — Phase 1 will populate them once the catalog has the
// aggregations indexed. iOS tolerates empty filter dropdowns gracefully.
//
// The two fetch/convert blocks deliberately stay un-abstracted: they target
// different store methods (ListLibraryAuthors / ListLibrarySeries) and
// produce different output types (AuthorObj / SeriesObj). A generic helper
// would need closures at each call site that are longer than the inlined
// code; the structural parallelism is the cheapest form here.
func (h *Handler) buildFilterData(r *http.Request, lib AudiobookLibrary) map[string]any {
	ctx := r.Context()
	const fetchCap = 5000
	access, _, _ := h.accessFilterFromRequest(r)

	authorObjs := []AuthorObj{}
	if rows, _, err := h.deps.MediaStore.ListLibraryAuthors(ctx, lib.ID, fetchCap, 0, "name", false, access); err == nil {
		for _, a := range rows {
			authorObjs = append(authorObjs, AuthorObj{ID: a.ID, Name: a.Name})
		}
	}

	seriesObjs := []SeriesObj{}
	if rows, _, err := h.deps.MediaStore.ListLibrarySeries(ctx, lib.ID, fetchCap, 0, access); err == nil {
		for _, s := range rows {
			seriesObjs = append(seriesObjs, SeriesObj{ID: s.ID, Name: s.Name})
		}
	}

	return map[string]any{
		"authors":    authorObjs,
		"series":     seriesObjs,
		"narrators":  []string{},
		"genres":     []string{},
		"publishers": []string{},
		"languages":  []string{},
		"tags":       []string{},
	}
}

// ---------------------------------------------------------------------------
// /libraries/{libraryId}/items — paginated audiobook browse
// ---------------------------------------------------------------------------

// handleLibraryItems — GET /abs/api/libraries/{libraryId}/items
//
// Returns a paginated, optionally filtered and/or collapsed-by-series list
// of audiobook LibraryItems from silo's media_items table for the requested
// library. Supports the standard ABS query params:
//
//   - limit / page      — pagination
//   - minified=1        — slim response (no tracks, flat author/series)
//   - filter=<kind>.<b64value> — local-side filter (authors, series, narrators, progress)
//   - collapseseries=1  — fold books by series; returns one entry per series
//
// Note: sort pushdown is not yet implemented (returns insertion order from
// the store). Filter is applied locally after fetching. These limitations
// are consistent with the plugin at its initial launch.
func (h *Handler) handleLibraryItems(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	limit, page := readPagedQuery(r, 30)
	sortBy := q.Get("sort")
	sortDesc := q.Get("desc") == "1"
	filterBy := q.Get("filter")
	// Real ABS getByFilterAndSort ALWAYS serializes list items minified
	// (LibraryItem.toOldJSONMinified); the non-minified hybrid is a shape no
	// real client requests. Default to minified; only an explicit minified=0
	// opts into the full shape.
	minified := q.Get("minified") != "0"
	collapseSeries := q.Get("collapseseries") == "1"
	include := q.Get("include")

	filter, hasFilter := ParseFilter(filterBy)

	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}

	// authors/series/narrators filters push down into SQL (indexed) so we never
	// load + hydrate the whole library. This applies even with collapseseries=1
	// (the client's per-artist album sync): the SQL filter reduces to a handful
	// of rows, then collapse + paging run in Go over that small set. Only
	// progress/genre/tag/language filters still need the Go post-filter and the
	// full fetch.
	pushDown := hasFilter &&
		(filter.Kind == FilterAuthors || filter.Kind == FilterSeries || filter.Kind == FilterNarrators)
	sqlFilter := Filter{}
	if pushDown {
		sqlFilter = filter
	}
	goFilter := hasFilter && !pushDown

	// SQL paginates only when nothing is post-processed in Go (no Go filter, no
	// collapse) and the limit is positive; otherwise fetch the (now
	// SQL-filtered, hence small) candidate set in full and slice in Go.
	sqlPaginated := !goFilter && !collapseSeries && limit > 0
	fetchLimit, fetchOffset := 0, 0
	if sqlPaginated {
		fetchLimit = limit
		fetchOffset = page * limit
	}

	items, total, err := h.deps.MediaStore.ListAudiobooks(r.Context(), lib.ID, fetchLimit, fetchOffset, access, sqlFilter)
	if err != nil {
		http.Error(w, "list audiobooks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to ABS LibraryItem shape.
	baseURL := h.absBaseURL(r)
	all := make([]LibraryItem, 0, len(items))
	for _, item := range items {
		all = append(all, siloItemToLibraryItem(item, lib, baseURL))
	}

	// Local filter (post-fetch) — only for filters not pushed into SQL.
	if goFilter {
		filtered := make([]LibraryItem, 0, len(all))
		for _, it := range all {
			if filter.Matches(it, false, false, false) {
				filtered = append(filtered, it)
			}
		}
		all = filtered
		total = len(all)
	}

	// Collapse-by-series before paging.
	collapsed := all
	if collapseSeries {
		collapsed = CollapseBySeries(all)
		total = len(collapsed)
	}

	// Slice for page/limit. When SQL already paginated we serve the rows as-is.
	pageStart, pageEnd := 0, len(collapsed)
	if limit > 0 && !sqlPaginated {
		pageStart = page * limit
		if pageStart > len(collapsed) {
			pageStart = len(collapsed)
		}
		pageEnd = pageStart + limit
		if pageEnd > len(collapsed) {
			pageEnd = len(collapsed)
		}
	}
	pageSlice := collapsed[pageStart:pageEnd]

	// Serialise.
	var results any
	if minified {
		mins := make([]MinifiedLibraryItem, len(pageSlice))
		for i, it := range pageSlice {
			mins[i] = Minify(it)
		}
		results = mins
	} else {
		results = pageSlice
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, sortBy, sortDesc, filterBy, minified, include))
}

// ---------------------------------------------------------------------------
// Cover and stub endpoints for authors / series / search / personalized
// ---------------------------------------------------------------------------

// handleItemCover — GET /abs/api/items/{id}/cover (unauthenticated)
//
// Returns the audiobook's cover image. Currently redirects to silo's native
// cover endpoint; a later stage may proxy the bytes directly.
func (h *Handler) handleItemCover(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, emptyAccessFilter())
	if err != nil || item == nil {
		http.NotFound(w, r)
		return
	}
	if item.PosterPath == "" {
		http.NotFound(w, r)
		return
	}
	target := item.PosterPath
	// Raw silo paths (e.g. "local/audiobooks/.../original.webp") need to
	// be resolved into a real URL via the CoverResolver before redirect;
	// otherwise the client follows a relative path that doesn't exist on
	// the ABS listener.
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		if h.deps.CoverResolver != nil {
			if resolved := h.deps.CoverResolver(r.Context(), target, "card"); resolved != "" {
				target = resolved
			} else {
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// handleAuthorImage — GET /authors/{id}/image. Public unauthenticated
// route (mounted outside bearerAuth in handler.go). Uses MediaStore
// to resolve the people row, then CoverResolver to mint a presigned
// URL and 302-redirect to it.
func (h *Handler) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	author, err := h.deps.MediaStore.GetAuthorByID(r.Context(), id, emptyAccessFilter())
	if err != nil || author.PosterPath == "" {
		http.Error(w, "author image not found", http.StatusNotFound)
		return
	}
	if h.deps.CoverResolver == nil {
		http.Error(w, "image resolver not configured", http.StatusServiceUnavailable)
		return
	}
	url := h.deps.CoverResolver(r.Context(), author.PosterPath, "")
	if url == "" {
		http.Error(w, "image resolution failed", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// handleLibraryAuthors — GET /abs/api/libraries/{id}/authors
// Lists audiobook authors aggregated from item_people kind=7, including
// per-author book counts. Returns the canonical ABS paged envelope to
// match the continuum-plugin-audiobooks shape verbatim.
func (h *Handler) handleLibraryAuthors(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	// Real ABS returns ALL authors for a non-paginated request (no default
	// cap) — clients like the official app fetch the whole list once and
	// scroll it locally, so a small server-side default limit silently
	// truncates the Authors page. limit=0 means "return all", clamped by
	// the guardrail below: at silo scale (a 250k-book library carries ~98k
	// authors) a truly unbounded response crashes non-paginating mobile
	// clients, which real ABS never has to survive.
	limit, page := readPagedQuery(r, 0)
	if limit <= 0 || limit > maxUnpaginatedAuthors {
		limit = maxUnpaginatedAuthors
	}
	sortBy := r.URL.Query().Get("sort")
	sortDesc := r.URL.Query().Get("desc") == "1"
	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	// Reads the precomputed author materialized view: indexed paginated read +
	// trivial count, so large libraries aren't capped and full syncs don't blow
	// the client's background-task window. limit=0 means "return all".
	offset := 0
	if limit > 0 {
		offset = page * limit
	}
	pageAuthors, total, err := h.deps.MediaStore.ListLibraryAuthors(r.Context(), lib.ID, limit, offset, sortBy, sortDesc, access)
	if err != nil {
		http.Error(w, "list authors: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if total > len(pageAuthors) && r.URL.Query().Get("page") == "" {
		slog.Warn("abs authors truncated for non-paginated request",
			"library", lib.ID, "total", total, "returned", len(pageAuthors))
	}
	libID := audiobookLibraryID(lib)
	results := make([]map[string]any, 0, len(pageAuthors))
	for _, a := range pageAuthors {
		results = append(results, authorObjectABS(a.ID, a.Name, libID, a.NumBooks, a.HasPhoto))
	}
	// Real ABS LibraryController.getAuthors branches on isPaginated =
	// (limit present & numeric) && (page present & numeric): paged envelope
	// when true, else a bare { authors: [...] }. Emitting the paged shape for
	// the non-paginated request crashes clients that key on `authors`.
	q := r.URL.Query()
	if q.Get("limit") != "" && q.Get("page") != "" {
		writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, "name", false, "", false, ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": results})
}

// handleLibrarySeries — GET /abs/api/libraries/{id}/series
// Lists audiobook series. Single-book series are filtered out by the
// store query since they're not useful as series. Returns the canonical
// ABS paged envelope; addedAt is 0 because the v1 catalog has no series
// added-at column (real ABS clients tolerate the placeholder).
func (h *Handler) handleLibrarySeries(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	// Like /authors: real ABS serves the full set when the client doesn't
	// paginate; a small server-side default limit truncates the Series page.
	// Clamped by the same guardrail (see maxUnpaginatedSeries).
	limit, page := readPagedQuery(r, 0)
	if limit <= 0 || limit > maxUnpaginatedSeries {
		limit = maxUnpaginatedSeries
	}
	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	// Paginate in SQL with a separate COUNT so large libraries aren't truncated
	// at a fixed cap. limit=0 means "return all" (ABS contract).
	offset := 0
	if limit > 0 {
		offset = page * limit
	}
	pageSeries, total, err := h.deps.MediaStore.ListLibrarySeries(r.Context(), lib.ID, limit, offset, access)
	if err != nil {
		http.Error(w, "list series: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if total > len(pageSeries) && r.URL.Query().Get("page") == "" {
		slog.Warn("abs series truncated for non-paginated request",
			"library", lib.ID, "total", total, "returned", len(pageSeries))
	}
	libID := audiobookLibraryID(lib)
	baseURL := h.absBaseURL(r)
	results := make([]map[string]any, 0, len(pageSeries))
	for _, s := range pageSeries {
		// books[] is what LazySeriesCard reads to populate the GroupCover
		// stack. Real ABS emits FULL minified library items here; a thin stub
		// crashes strict clients (Plappa) on the first missing required key.
		books := make([]MinifiedLibraryItem, 0, len(s.Books))
		for _, bp := range s.Books {
			updatedMs := int64(0)
			if !bp.UpdatedAt.IsZero() {
				updatedMs = bp.UpdatedAt.UnixMilli()
			}
			books = append(books, seriesBookMinified(bp.ContentID, bp.Title, libID, baseURL, updatedMs))
		}
		obj := seriesObjectABS(s.ID, s.Name, libID, s.NumBooks)
		obj["books"] = books
		results = append(results, obj)
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, "name", false, "", false, ""))
}

// handleLibrarySearch — GET /abs/api/libraries/{id}/search?q=…&limit=…
//
// Matches server/utils/queries/libraryItemsBookFilters.js `search()` (real
// ABS branches to the book-filter search for a non-podcast library, which
// is all Silo ever serves). That function returns exactly these keys:
// book, narrators, tags, genres, series, authors — there is NO "podcast"
// key for a book-library search (that only appears from the separate
// podcast-filter branch). Each book entry is `{ libraryItem }` — real ABS
// does not include matchKey/matchText on book entries (those only exist
// on the interactive-search HTML autocomplete, not this JSON endpoint).
// We keep an extra empty "podcast" bucket anyway since an extra key never
// crashes a strict client, only a missing one does.
func (h *Handler) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 12
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 50 {
		limit = n
	}
	empty := map[string]any{
		"book":      []any{},
		"podcast":   []any{},
		"narrators": []any{},
		"tags":      []any{},
		"genres":    []any{},
		"series":    []any{},
		"authors":   []any{},
	}
	if q == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	items, err := h.deps.MediaStore.SearchAudiobooks(r.Context(), lib.ID, q, limit, access)
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	baseURL := h.absBaseURL(r)
	libID := audiobookLibraryID(lib)
	books := make([]map[string]any, 0, len(items))
	for _, it := range items {
		books = append(books, map[string]any{
			"libraryItem": siloItemToLibraryItem(it, lib, baseURL),
		})
	}

	// Best-effort author/series buckets: silo has no dedicated search-scoped
	// store query for these yet, so we reuse the existing aggregate listers
	// (capped, same pattern as buildFilterData/handleLibraryAuthors/
	// handleLibrarySeries) and filter client-side on a case-insensitive
	// substring match. narrators/tags/genres have no backing aggregation
	// query at all in silo's catalog today and stay empty-but-present.
	qLower := strings.ToLower(q)
	const fetchCap = 5000

	authorsOut := []any{}
	if rows, _, err := h.deps.MediaStore.ListLibraryAuthors(r.Context(), lib.ID, fetchCap, 0, "name", false, access); err == nil {
		for _, a := range rows {
			if !strings.Contains(strings.ToLower(a.Name), qLower) {
				continue
			}
			authorsOut = append(authorsOut, map[string]any{
				"id":        a.ID,
				"name":      a.Name,
				"numBooks":  a.NumBooks,
				"libraryId": libID,
			})
			if len(authorsOut) >= limit {
				break
			}
		}
	}

	seriesOut := []any{}
	if rows, _, err := h.deps.MediaStore.ListLibrarySeries(r.Context(), lib.ID, fetchCap, 0, access); err == nil {
		for _, s := range rows {
			if !strings.Contains(strings.ToLower(s.Name), qLower) {
				continue
			}
			// Real ABS wraps series search hits as { series, books } — the
			// series sub-object is the plain Series.toOldJSON() shape (no
			// numBooks field there; we add it anyway since an extra key is
			// harmless), matched here with the same per-book thin map
			// handleLibrarySeries above uses for its books[] entries.
			seriesBooks := make([]map[string]any, 0, len(s.Books))
			for _, bp := range s.Books {
				updatedMs := int64(0)
				if !bp.UpdatedAt.IsZero() {
					updatedMs = bp.UpdatedAt.UnixMilli()
				}
				seriesBooks = append(seriesBooks, map[string]any{
					"id":        bp.ContentID,
					"libraryId": libID,
					"mediaType": LibraryMediaType,
					"updatedAt": updatedMs,
					"media": map[string]any{
						"coverPath": baseURL + "/api/items/" + bp.ContentID + "/cover",
						"metadata":  map[string]any{"title": bp.Title},
					},
				})
			}
			seriesOut = append(seriesOut, map[string]any{
				"series": map[string]any{
					"id":        s.ID,
					"name":      s.Name,
					"numBooks":  s.NumBooks,
					"libraryId": libID,
					"addedAt":   0,
				},
				"books": seriesBooks,
			})
			if len(seriesOut) >= limit {
				break
			}
		}
	}

	out := empty
	out["book"] = books
	out["authors"] = authorsOut
	out["series"] = seriesOut
	writeJSON(w, http.StatusOK, out)
}

// handlePersonalized — GET /abs/api/libraries/{id}/personalized
//
// Emits the canonical six-shelf Home tab payload that ABS mobile clients
// expect: continue-listening, continue-series, newest, recent-series,
// discover, listen-again. Shelves we don't yet populate (continue-series,
// listen-again) ship with empty entities/total — the client iterates the
// shelf list by id and skips empties cleanly, but it crashes on a missing
// shelf id. Matches continuum-plugin-audiobooks/handlePersonalized layout.
func (h *Handler) handlePersonalized(w http.ResponseWriter, r *http.Request) {
	lib, ok := h.resolveLibrary(w, r)
	if !ok {
		return
	}
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.MediaStore == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	baseURL := h.absBaseURL(r)
	const shelfLimit = 10
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}

	shelves := []map[string]any{
		{"id": "continue-listening", "label": "Continue Listening", "labelStringKey": "LabelContinueListening", "type": "book", "entities": []any{}, "total": 0},
		{"id": "continue-series", "label": "Continue Series", "labelStringKey": "LabelContinueSeries", "type": "book", "entities": []any{}, "total": 0},
		{"id": "newest", "label": "Newest", "labelStringKey": "LabelNewest", "type": "book", "entities": []any{}, "total": 0},
		{"id": "recent-series", "label": "Recent Series", "labelStringKey": "LabelRecentSeries", "type": "series", "entities": []any{}, "total": 0},
		{"id": "discover", "label": "Discover", "labelStringKey": "LabelDiscover", "type": "book", "entities": []any{}, "total": 0},
		{"id": "listen-again", "label": "Listen Again", "labelStringKey": "LabelListenAgain", "type": "book", "entities": []any{}, "total": 0},
	}

	if items, err := h.deps.MediaStore.ListContinueListening(r.Context(), a.UserID, a.ProfileID, lib.ID, shelfLimit, access); err == nil && len(items) > 0 {
		shelves[0]["entities"] = minifiedSlice(items, lib, baseURL)
		shelves[0]["total"] = len(items)
	}

	if items, err := h.deps.MediaStore.ListRecentlyAdded(r.Context(), lib.ID, shelfLimit, access); err == nil && len(items) > 0 {
		shelves[2]["entities"] = minifiedSlice(items, lib, baseURL)
		shelves[2]["total"] = len(items)
	}

	libID := audiobookLibraryID(lib)
	if series, _, err := h.deps.MediaStore.ListLibrarySeries(r.Context(), lib.ID, shelfLimit, 0, access); err == nil && len(series) > 0 {
		recent := make([]map[string]any, 0, len(series))
		for _, s := range series {
			// Full real-ABS series object + minified books (same shape as
			// /libraries/{id}/series) so the recent-series shelf card decodes
			// identically and its cover stack has real items.
			obj := seriesObjectABS(s.ID, s.Name, libID, s.NumBooks)
			books := make([]MinifiedLibraryItem, 0, len(s.Books))
			for _, bp := range s.Books {
				updatedMs := int64(0)
				if !bp.UpdatedAt.IsZero() {
					updatedMs = bp.UpdatedAt.UnixMilli()
				}
				books = append(books, seriesBookMinified(bp.ContentID, bp.Title, libID, baseURL, updatedMs))
			}
			obj["books"] = books
			recent = append(recent, obj)
		}
		shelves[3]["entities"] = recent
		shelves[3]["total"] = len(recent)
	}

	if items, err := h.deps.MediaStore.ListDiscover(r.Context(), lib.ID, shelfLimit, access); err == nil && len(items) > 0 {
		shelves[4]["entities"] = minifiedSlice(items, lib, baseURL)
		shelves[4]["total"] = len(items)
	}

	writeJSON(w, http.StatusOK, shelves)
}

// minifiedSlice converts a batch of MediaItems into ABS Minified entries.
func minifiedSlice(items []*models.MediaItem, lib AudiobookLibrary, baseURL string) []MinifiedLibraryItem {
	out := make([]MinifiedLibraryItem, 0, len(items))
	for _, it := range items {
		out = append(out, Minify(siloItemToLibraryItem(it, lib, baseURL)))
	}
	return out
}

// ---------------------------------------------------------------------------
// Library resolver
// ---------------------------------------------------------------------------

// resolveLibrary looks up the library identified by the {libraryId} URL
// param, handling the virtual "silo-audiobooks" sentinel. Returns (lib, true)
// on success or writes a 404 and returns (zero, false) on failure.
func (h *Handler) resolveLibrary(w http.ResponseWriter, r *http.Request) (AudiobookLibrary, bool) {
	idStr := chi.URLParam(r, "libraryId")
	if idStr == "" {
		idStr = chi.URLParam(r, "id")
	}

	access, _, err := h.accessFilterFromRequest(r)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return AudiobookLibrary{}, false
	}
	libs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context(), access)
	if err != nil {
		http.Error(w, "list libraries: "+err.Error(), http.StatusInternalServerError)
		return AudiobookLibrary{}, false
	}

	// Virtual sentinel → first library.
	if idStr == "" || idStr == VirtualLibraryID {
		if len(libs) > 0 {
			return libs[0], true
		}
		// No libraries configured yet: return a virtual one so ABS clients
		// still get a sensible (empty) browse response.
		return AudiobookLibrary{ID: 0, Name: VirtualLibraryName, Type: "audiobooks"}, true
	}

	n, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "library not found", http.StatusNotFound)
		return AudiobookLibrary{}, false
	}
	for _, lib := range libs {
		if lib.ID == n {
			return lib, true
		}
	}
	http.Error(w, "library not found", http.StatusNotFound)
	return AudiobookLibrary{}, false
}

// ---------------------------------------------------------------------------
// silo MediaItem → ABS LibraryItem translation
// ---------------------------------------------------------------------------

// siloItemToLibraryItem converts a silo MediaItem (type='audiobook') into the
// ABS LibraryItem wire shape for browse-list responses (no audio tracks; only
// metadata + duration summary). File-level tracks are populated only on the
// item-detail handler (handleItem).
func siloItemToLibraryItem(item *models.MediaItem, lib AudiobookLibrary, baseURL string) LibraryItem {
	meta := siloItemToMetadata(item)
	libID := audiobookLibraryID(lib)

	// Duration: Runtime field on MediaItem is in minutes for video; for
	// audiobooks it stores the total seconds (set by the scanner Stage 2
	// extension). Convert from the int field.
	duration := float64(item.Runtime) // seconds

	// Always point coverPath at our /api/items/{id}/cover endpoint rather
	// than the raw silo PosterPath. Storage paths like
	// "local/audiobooks/.../original.webp" mean nothing to an ABS client;
	// our cover handler resolves them via the CoverResolver before
	// redirecting to the real URL.
	coverPath := baseURL + "/api/items/" + item.ContentID + "/cover"

	addedAtMs := int64(0)
	if item.AddedAt != nil {
		addedAtMs = item.AddedAt.UnixMilli()
	}
	updatedAtMs := item.UpdatedAt.UnixMilli()

	return LibraryItem{
		ID:          item.ContentID,
		Ino:         item.ContentID, // stable item-level ino; matches real-ABS shape
		LibraryID:   libID,
		FolderID:    VirtualFolderID,
		Path:        "",
		RelPath:     "",
		IsFile:      true,
		MtimeMs:     addedAtMs,
		CtimeMs:     addedAtMs,
		BirthtimeMs: addedAtMs,
		MediaType:   LibraryMediaType,
		Media: LibraryItemMedia{
			ID:            item.ContentID,
			LibraryItemID: item.ContentID,
			Metadata:      meta,
			Duration:      duration,
			CoverPath:     coverPath,
			AudioFiles:    []AudioTrack{},
			Tracks:        []AudioTrack{},
			Chapters:      []ChapterABS{},
			NumTracks:     0, // populated by item-detail handler
			Tags:          []string{},
		},
		LibraryFiles: []map[string]any{}, // populated by item-detail handler
		LastScan:     addedAtMs,
		ScanVersion:  ServerVersion,
		AddedAt:      addedAtMs,
		UpdatedAt:    updatedAtMs,
	}
}

// siloItemToMetadata extracts the ABS Metadata block from a silo MediaItem.
// Authors and narrators are sourced from item.People; series from the
// audiobook_series table hydrated onto the MediaItem; publisher from Studios.
//
// Strict 3rd-party clients (Plappa, AudioBookShelfFully) require id on
// every author/series entry and non-nil tags/genres arrays. We surface
// IDs from item_people.id (authors) and slugify(name) (series).
func siloItemToMetadata(item *models.MediaItem) Metadata {
	authors := make([]AuthorObj, 0)
	narrators := make([]string, 0)
	authorNames := make([]string, 0)
	lfNames := make([]string, 0)

	for _, p := range item.People {
		switch p.Kind {
		case models.PersonKindAuthor:
			authors = append(authors, AuthorObj{
				ID:   strconv.FormatInt(p.ID, 10),
				Name: p.Name,
			})
			authorNames = append(authorNames, p.Name)
			lfNames = append(lfNames, lastFirst(p.Name))
		case models.PersonKindNarrator:
			narrators = append(narrators, p.Name)
		}
	}

	series := make([]SeriesObj, 0, len(item.AudiobookSeries))
	seriesName := ""
	for _, membership := range item.AudiobookSeries {
		name := strings.TrimSpace(membership.Name)
		if name == "" {
			continue
		}
		obj := SeriesObj{ID: name, Name: name}
		if membership.Index != nil {
			obj.Sequence = strconv.FormatFloat(*membership.Index, 'f', -1, 64)
		}
		series = append(series, obj)
		if seriesName == "" {
			seriesName = name
			if obj.Sequence != "" {
				seriesName += " #" + obj.Sequence
			}
		}
	}

	publishedYear := ""
	if item.Year > 0 {
		publishedYear = strconv.Itoa(item.Year)
	}

	genres := item.Genres
	if genres == nil {
		genres = []string{}
	}

	// silo has no item-level tags concept today; emit an empty array so
	// clients that branch on tags[] don't see a null and crash.
	tags := []string{}

	publisher := ""
	if len(item.Studios) > 0 {
		publisher = strings.TrimSpace(item.Studios[0])
	}

	return Metadata{
		Title:             item.Title,
		TitleIgnorePrefix: titleIgnorePrefix(item.Title),
		Authors:           authors,
		AuthorName:        strings.Join(authorNames, ", "),
		AuthorNameLF:      strings.Join(lfNames, ", "),
		Narrators:         narrators,
		NarratorName:      strings.Join(narrators, ", "),
		Series:            series,
		SeriesName:        seriesName,
		Description:       item.Overview,
		DescriptionPlain:  stripHTML(item.Overview),
		PublishedYear:     publishedYear,
		Publisher:         publisher,
		Genres:            genres,
		Language:          "en",
		Tags:              tags,
	}
}

// siloItemToLibraryItemDetail converts a silo MediaItem + its media files into
// a full ABS LibraryItem with audio track details populated. Called by
// handleItem (single-item GET).
func siloItemToLibraryItemDetail(item *models.MediaItem, files []*models.MediaFile, lib AudiobookLibrary, baseURL string) LibraryItem {
	base := siloItemToLibraryItem(item, lib, baseURL)

	tracks := siloFilesToAudioTracks(item.ContentID, files, baseURL, "")

	// media.duration is the summed track duration (real ABS: sum of audio file
	// durations), NOT the item's Runtime — Runtime is often stale/mis-scanned
	// (e.g. 222s for a 3.7h book), which desyncs the player's scrubber. Fall
	// back to Runtime only when there are no tracks to sum.
	totalDuration := float64(0)
	for _, t := range tracks {
		totalDuration += t.Duration
	}
	if totalDuration == 0 {
		totalDuration = base.Media.Duration
	}

	// Chapters from the first file that has them.
	chapters := make([]ChapterABS, 0)
	for _, f := range files {
		if len(f.Chapters) > 0 {
			for i, c := range f.Chapters {
				chapters = append(chapters, ChapterABS{
					ID:    i,
					Start: c.StartSeconds,
					End:   c.EndSeconds,
					Title: c.Title,
				})
			}
			break
		}
	}

	// libraryFiles + summed size mirror real ABS toOldJSONExpanded. Each entry
	// is the real-ABS library file shape (ino + file metadata + fileType).
	nowMs := time.Now().UnixMilli()
	libraryFiles := make([]map[string]any, 0, len(tracks))
	var totalSize int64
	for _, t := range tracks {
		if t.Metadata != nil {
			totalSize += t.Metadata.Size
		}
		libraryFiles = append(libraryFiles, map[string]any{
			"ino":             t.Ino,
			"metadata":        t.Metadata,
			"isSupplementary": false,
			"addedAt":         nowMs,
			"updatedAt":       nowMs,
			"fileType":        "audio",
		})
	}

	base.Media.AudioFiles = tracks
	base.Media.Tracks = tracks
	base.Media.Chapters = chapters
	base.Media.NumTracks = len(tracks)
	base.Media.Duration = totalDuration
	base.Media.Size = totalSize
	base.NumTracks = len(tracks)
	base.LibraryFiles = libraryFiles
	base.Size = totalSize
	return base
}

// siloFilesToAudioTracks converts silo MediaFile rows into ABS AudioTrack
// entries for the item-detail response. token may be empty (item-detail
// doesn't embed auth tokens; the ABS client initiates playback via /play).
func siloFilesToAudioTracks(contentID string, files []*models.MediaFile, baseURL, token string) []AudioTrack {
	tracks := make([]AudioTrack, 0, len(files))
	startOffset := float64(0)
	nowMs := time.Now().UnixMilli()

	for i, f := range files {
		ino := trackInoFor(contentID, i)
		ext := strings.ToLower(filepath.Ext(f.FilePath))
		format := strings.TrimPrefix(ext, ".")
		mimeType := audioContentType(ext)
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}
		filename := filepath.Base(f.FilePath)
		wireIndex := i + 1

		contentURL := baseURL + "/abs/api/items/" + contentID + "/file/" + ino
		if token != "" {
			contentURL += "?token=" + token
		}

		duration := float64(f.Duration)
		bitRate := f.Bitrate * 1000
		if bitRate == 0 {
			bitRate = 128000
		}
		channels := f.AudioChannels
		if channels == 0 {
			channels = 2
		}
		channelLayout := "stereo"
		if channels > 2 {
			channelLayout = "surround"
		}

		tracks = append(tracks, AudioTrack{
			Index: wireIndex,
			Ino:   ino,
			Metadata: &AudioTrackMetadata{
				Filename:    filename,
				Ext:         ext,
				Path:        f.FilePath,
				RelPath:     filename,
				Size:        f.FileSize,
				MtimeMs:     nowMs,
				CtimeMs:     nowMs,
				BirthtimeMs: nowMs,
			},
			AddedAt:          nowMs,
			UpdatedAt:        nowMs,
			ManuallyVerified: false,
			Exclude:          false,
			Format:           format,
			Duration:         duration,
			BitRate:          bitRate,
			Language:         nil,
			Codec:            f.CodecAudio,
			TimeBase:         "1/14112000",
			Channels:         channels,
			ChannelLayout:    channelLayout,
			Chapters:         []ChapterABS{},
			EmbeddedCoverArt: nil,
			MetaTags:         map[string]string{},
			MimeType:         mimeType,
			Title:            filename,
			StartOffset:      startOffset,
			ContentURL:       contentURL,
		})
		startOffset += duration
	}
	return tracks
}

// slugify produces a stable ID-from-name, identical to the plugin's translate.go
// implementation so derived IDs round-trip consistently.
func slugify(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case isLetterOrDigit(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func isLetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// includeHas tests whether an "include" comma-separated query value contains
// the given key.
func includeHas(raw, want string) bool {
	if raw == "" {
		return false
	}
	for _, p := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(p), want) {
			return true
		}
	}
	return false
}
