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
		resp["filterdata"] = emptyFilterData()
		resp["issues"] = 0
		resp["numUserPlaylists"] = 0
	}
	writeJSON(w, http.StatusOK, resp)
}

// emptyFilterData returns a zero-entry filter-data block. Populating authors /
// series / narrators from silo's catalog is deferred: for now ABS clients
// will show empty filter dropdowns rather than crash.
func emptyFilterData() map[string]any {
	return map[string]any{
		"authors":    []AuthorObj{},
		"series":     []SeriesObj{},
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
	// Redirect to silo's native image endpoint rather than proxying bytes.
	http.Redirect(w, r, item.PosterPath, http.StatusFound)
}

// handleAuthorImage — GET /abs/api/authors/{id}/image (unauthenticated)
//
// Author images are not yet stored in silo; return a clean 404 so the
// ABS client renders the placeholder.
func (h *Handler) handleAuthorImage(w http.ResponseWriter, _ *http.Request) {
	http.NotFound(w, nil)
}

// handleLibraryAuthors — GET /abs/api/libraries/{id}/authors
// Stubbed: silo does not yet maintain a normalised author table.
func (h *Handler) handleLibraryAuthors(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveLibrary(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": []any{}})
}

// handleLibrarySeries — GET /abs/api/libraries/{id}/series
// Stubbed: silo does not yet maintain a normalised series table.
func (h *Handler) handleLibrarySeries(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveLibrary(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": []any{}, "total": 0})
}

// handleLibrarySearch — GET /abs/api/libraries/{id}/search
// Stubbed: returns empty results for all categories.
func (h *Handler) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveLibrary(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"book":   []any{},
		"podcast": []any{},
		"author": []any{},
		"series": []any{},
		"tags":   []any{},
	})
}

// handlePersonalized — GET /abs/api/libraries/{id}/personalized
// Stubbed: returns an empty shelf list. Continue Listening is served via
// /me/items-in-progress; this endpoint feeds the home-tab "shelves".
// A full implementation would build Recently Added, Continue Listening,
// Newest Authors, etc. from silo's catalog + progress tables.
func (h *Handler) handlePersonalized(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveLibrary(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
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

	coverPath := item.PosterPath
	if coverPath == "" {
		coverPath = baseURL + "/abs/api/items/" + item.ContentID + "/cover"
	}

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

	return Metadata{
		Title:         item.Title,
		Authors:       authors,
		Narrators:     narrators,
		Series:        series,
		Description:   item.Overview,
		PublishedYear: publishedYear,
		Genres:        genres,
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
