package abs

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ---------------------------------------------------------------------------
// /libraries list + single-library detail
// ---------------------------------------------------------------------------

// handleLibraries — GET /abs/api/libraries (and /api/libraries)
//
// Returns the list of audiobook media_folders. ABS clients call this to
// populate the library picker and to know which library IDs are valid.
func (h *Handler) handleLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
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
	resp := map[string]any{
		"library": audiobookLibraryMap(lib),
	}
	if includeHas(r.URL.Query().Get("include"), "filterdata") {
		resp["filterdata"] = h.buildFilterData(r, lib)
		resp["issues"] = 0
		resp["numUserPlaylists"] = 0
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildFilterData populates the filter sheet payload from the same store
// queries /libraries/{id}/authors and /libraries/{id}/series use. Caps at
// 5000 per kind to keep the response bounded; libraries larger than that
// will paginate via the dedicated /authors and /series endpoints.
//
// Narrators / genres / publishers / languages / tags are left as empty
// arrays for now — Phase 1 will populate them once the catalog has the
// aggregations indexed. iOS tolerates empty filter dropdowns gracefully.
func (h *Handler) buildFilterData(r *http.Request, lib AudiobookLibrary) map[string]any {
	ctx := r.Context()
	const fetchCap = 5000

	authorObjs := []AuthorObj{}
	if h.deps.MediaStore != nil {
		if rows, err := h.deps.MediaStore.ListLibraryAuthors(ctx, lib.ID, fetchCap); err == nil {
			for _, a := range rows {
				authorObjs = append(authorObjs, AuthorObj{ID: a.ID, Name: a.Name})
			}
		}
	}

	seriesObjs := []SeriesObj{}
	if h.deps.MediaStore != nil {
		if rows, err := h.deps.MediaStore.ListLibrarySeries(ctx, lib.ID, fetchCap); err == nil {
			for _, s := range rows {
				seriesObjs = append(seriesObjs, SeriesObj{ID: s.ID, Name: s.Name})
			}
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
	minified := q.Get("minified") == "1"
	collapseSeries := q.Get("collapseseries") == "1"
	include := q.Get("include")

	filter, hasFilter := ParseFilter(filterBy)

	// Over-fetch when filtering so local post-filter has enough rows.
	fetchLimit := limit
	if hasFilter || limit == 0 {
		fetchLimit = 5000
	}
	fetchOffset := 0
	if !hasFilter && limit > 0 {
		fetchOffset = page * limit
	}

	items, total, err := h.deps.MediaStore.ListAudiobooks(r.Context(), lib.ID, fetchLimit, fetchOffset)
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

	// Local filter (post-fetch).
	if hasFilter {
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

	// Slice for page/limit.
	pageStart, pageEnd := 0, len(collapsed)
	if limit > 0 && (hasFilter || collapseSeries) {
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
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID)
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

// handleAuthorImage — GET /abs/api/authors/{id}/image (unauthenticated)
//
// Author images are not yet stored in silo; return a clean 404 so the
// ABS client renders the placeholder.
func (h *Handler) handleAuthorImage(w http.ResponseWriter, _ *http.Request) {
	http.NotFound(w, nil)
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
	limit, page := readPagedQuery(r, 50)
	// Fetch the full list (capped at 5000) and paginate locally so the
	// envelope's total reflects real DB count, not the page slice length.
	// ABS clients use total to decide whether to fetch page 2.
	const fetchCap = 5000
	authors, err := h.deps.MediaStore.ListLibraryAuthors(r.Context(), lib.ID, fetchCap)
	if err != nil {
		http.Error(w, "list authors: "+err.Error(), http.StatusInternalServerError)
		return
	}
	libID := audiobookLibraryID(lib)
	total := len(authors)
	// Local slice for the requested page.
	// ABS contract: limit=0 means "return all".
	var pageAuthors []AuthorSummary
	if limit == 0 {
		pageAuthors = authors
	} else {
		start := page * limit
		end := start + limit
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		pageAuthors = authors[start:end]
	}
	results := make([]map[string]any, 0, len(pageAuthors))
	for _, a := range pageAuthors {
		results = append(results, map[string]any{
			"id":        a.ID,
			"name":      a.Name,
			"numBooks":  a.NumBooks,
			"libraryId": libID,
		})
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, "name", false, "", false, ""))
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
	limit, page := readPagedQuery(r, 25)
	const fetchCap = 5000
	series, err := h.deps.MediaStore.ListLibrarySeries(r.Context(), lib.ID, fetchCap)
	if err != nil {
		http.Error(w, "list series: "+err.Error(), http.StatusInternalServerError)
		return
	}
	libID := audiobookLibraryID(lib)
	total := len(series)
	// ABS contract: limit=0 means "return all".
	var pageSeries []SeriesSummary
	if limit == 0 {
		pageSeries = series
	} else {
		start := page * limit
		end := start + limit
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		pageSeries = series[start:end]
	}
	results := make([]map[string]any, 0, len(pageSeries))
	for _, s := range pageSeries {
		results = append(results, map[string]any{
			"id":        s.ID,
			"name":      s.Name,
			"numBooks":  s.NumBooks,
			"libraryId": libID,
			"addedAt":   0,
		})
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, "name", false, "", false, ""))
}

// handleLibrarySearch — GET /abs/api/libraries/{id}/search?q=…&limit=…
// Returns matching books grouped under "book", with empty arrays for the
// other ABS-standard buckets (podcast, series, authors, tags). Bucket
// names match continuum-plugin-audiobooks exactly: note "authors" plural,
// not "author" — ABS mobile clients key off the plural form and a
// singular bucket is silently dropped.
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
		"book":    []any{},
		"podcast": []any{},
		"series":  []any{},
		"authors": []any{},
		"tags":    []any{},
	}
	if q == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	items, err := h.deps.MediaStore.SearchAudiobooks(r.Context(), lib.ID, q, limit)
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	baseURL := h.absBaseURL(r)
	books := make([]map[string]any, 0, len(items))
	for _, it := range items {
		books = append(books, map[string]any{
			"libraryItem": siloItemToLibraryItem(it, lib, baseURL),
			"matchKey":    "title",
			"matchText":   it.Title,
		})
	}
	out := empty
	out["book"] = books
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

	shelves := []map[string]any{
		{"id": "continue-listening", "label": "Continue Listening", "labelStringKey": "LabelContinueListening", "type": "book", "entities": []any{}, "total": 0},
		{"id": "continue-series", "label": "Continue Series", "labelStringKey": "LabelContinueSeries", "type": "book", "entities": []any{}, "total": 0},
		{"id": "newest", "label": "Newest", "labelStringKey": "LabelNewest", "type": "book", "entities": []any{}, "total": 0},
		{"id": "recent-series", "label": "Recent Series", "labelStringKey": "LabelRecentSeries", "type": "series", "entities": []any{}, "total": 0},
		{"id": "discover", "label": "Discover", "labelStringKey": "LabelDiscover", "type": "book", "entities": []any{}, "total": 0},
		{"id": "listen-again", "label": "Listen Again", "labelStringKey": "LabelListenAgain", "type": "book", "entities": []any{}, "total": 0},
	}

	if items, err := h.deps.MediaStore.ListContinueListening(r.Context(), a.UserID, a.ProfileID, lib.ID, shelfLimit); err == nil && len(items) > 0 {
		shelves[0]["entities"] = minifiedSlice(items, lib, baseURL)
		shelves[0]["total"] = len(items)
	}

	if items, err := h.deps.MediaStore.ListRecentlyAdded(r.Context(), lib.ID, shelfLimit); err == nil && len(items) > 0 {
		shelves[2]["entities"] = minifiedSlice(items, lib, baseURL)
		shelves[2]["total"] = len(items)
	}

	libID := audiobookLibraryID(lib)
	if series, err := h.deps.MediaStore.ListLibrarySeries(r.Context(), lib.ID, shelfLimit); err == nil && len(series) > 0 {
		recent := make([]map[string]any, 0, len(series))
		for _, s := range series {
			recent = append(recent, map[string]any{
				"id":        s.ID,
				"name":      s.Name,
				"numBooks":  s.NumBooks,
				"libraryId": libID,
				"books":     []any{},
			})
		}
		shelves[3]["entities"] = recent
		shelves[3]["total"] = len(recent)
	}

	if items, err := h.deps.MediaStore.ListDiscover(r.Context(), lib.ID, shelfLimit); err == nil && len(items) > 0 {
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

	libs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
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
		ID:        item.ContentID,
		LibraryID: libID,
		FolderID:  VirtualFolderID,
		MediaType: LibraryMediaType,
		Media: LibraryItemMedia{
			Metadata:   meta,
			Duration:   duration,
			CoverPath:  coverPath,
			AudioFiles: []AudioTrack{},
			Tracks:     []AudioTrack{},
			Chapters:   []ChapterABS{},
			NumTracks:  0, // populated by item-detail handler
		},
		AddedAt:   addedAtMs,
		UpdatedAt: updatedAtMs,
	}
}

// siloItemToMetadata extracts the ABS Metadata block from a silo MediaItem.
// Authors and narrators are sourced from item.People; series from Studios
// (silo stores the series name in Studios for audiobooks until a proper
// series table lands — see scanner Stage 2 notes).
//
// Strict 3rd-party clients (Plappa, AudioBookShelfFully) require id on
// every author/series entry and non-nil tags/genres arrays. We surface
// IDs from item_people.id (authors) and slugify(name) (series).
func siloItemToMetadata(item *models.MediaItem) Metadata {
	authors := make([]AuthorObj, 0)
	narrators := make([]string, 0)

	for _, p := range item.People {
		switch p.Kind {
		case models.PersonKindAuthor:
			authors = append(authors, AuthorObj{
				ID:   strconv.FormatInt(p.ID, 10),
				Name: p.Name,
			})
		case models.PersonKindNarrator:
			narrators = append(narrators, p.Name)
		}
	}

	// Series: silo's audiobook scanner stores series name in the Studios
	// field until a dedicated series table is added. Derive an ID by
	// slugifying the name (same convention as the plugin's translate.go).
	// Authoritative series IDs will replace these slugs when a series
	// table lands; client-stored references survive the change because the
	// slug is stable for a given name.
	series := make([]SeriesObj, 0, len(item.Studios))
	for _, s := range item.Studios {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		series = append(series, SeriesObj{
			ID:   slugify(s),
			Name: s,
		})
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

	return Metadata{
		Title:         item.Title,
		Authors:       authors,
		Narrators:     narrators,
		Series:        series,
		Description:   item.Overview,
		PublishedYear: publishedYear,
		Genres:        genres,
		Tags:          tags,
	}
}

// siloItemToLibraryItemDetail converts a silo MediaItem + its media files into
// a full ABS LibraryItem with audio track details populated. Called by
// handleItem (single-item GET).
func siloItemToLibraryItemDetail(item *models.MediaItem, files []*models.MediaFile, lib AudiobookLibrary, baseURL string) LibraryItem {
	base := siloItemToLibraryItem(item, lib, baseURL)

	tracks := siloFilesToAudioTracks(item.ContentID, files, baseURL, "")

	// Recompute duration from files if the item's Runtime is zero.
	totalDuration := base.Media.Duration
	if totalDuration == 0 {
		for _, t := range tracks {
			totalDuration += t.Duration
		}
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

	base.Media.AudioFiles = tracks
	base.Media.Tracks = tracks
	base.Media.Chapters = chapters
	base.Media.NumTracks = len(tracks)
	base.Media.Duration = totalDuration
	base.NumTracks = len(tracks)
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
