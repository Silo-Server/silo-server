package abs

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) handleAuthorDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		slog.WarnContext(r.Context(), "abs author access resolution failed", "component", "audiobooks", "err", err, "id", id)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	author, err := h.deps.MediaStore.GetAuthorByID(r.Context(), id, access)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "author not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "abs author detail failed", "component", "audiobooks", "err", err, "id", id)
		http.Error(w, "author get failed", http.StatusInternalServerError)
		return
	}
	lib := h.resolveDefaultLibrary(r.Context(), access)
	writeJSON(w, http.StatusOK, authorToABS(author, lib, h.absBaseURL(r)))
}

func (h *Handler) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	idRaw := chi.URLParam(r, "id")
	id, err := url.PathUnescape(idRaw)
	if err != nil {
		id = idRaw
	}
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		slog.WarnContext(r.Context(), "abs series access resolution failed", "component", "audiobooks", "err", err, "id", id)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	series, err := h.deps.MediaStore.GetSeriesByName(r.Context(), id, access)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "abs series detail failed", "component", "audiobooks", "err", err, "id", id)
		http.Error(w, "series get failed", http.StatusInternalServerError)
		return
	}
	lib := h.resolveDefaultLibrary(r.Context(), access)
	writeJSON(w, http.StatusOK, seriesToABS(series, lib, h.absBaseURL(r)))
}

// authorObjectABS builds the real ABS Author.toOldJSON(+numBooks) shape
// (server/models/Author.js). silo does not track asin/description/timestamps,
// so those are emitted as null/0 — nullable in real ABS, and a present key
// (not its value) is what keeps strict clients from crashing. imagePath is
// a server-local filesystem path in real ABS; clients treat any non-null
// value as "this author has a photo" and fetch it via
// GET /api/authors/{id}/image rather than dereferencing the path, so a
// synthetic ABS-shaped path is emitted when silo has a photo for the person.
func authorObjectABS(id, name, libraryID string, numBooks int, hasPhoto bool) map[string]any {
	var imagePath any
	if hasPhoto {
		imagePath = "/metadata/authors/" + id + ".jpg"
	}
	return map[string]any{
		"id":          id,
		"asin":        nil,
		"name":        name,
		"description": nil,
		"imagePath":   imagePath,
		"libraryId":   libraryID,
		"addedAt":     0,
		"updatedAt":   0,
		"numBooks":    numBooks,
	}
}

func authorToABS(a Author, lib AudiobookLibrary, baseURL string) map[string]any {
	libID := audiobookLibraryID(lib)
	obj := authorObjectABS(a.ID, a.Name, libID, len(a.Books), a.PosterPath != "")
	// Author-detail books are full minified library items (not thin stubs) so
	// any strict client decodes them with its LibraryItem model.
	books := make([]MinifiedLibraryItem, 0, len(a.Books))
	for _, b := range a.Books {
		books = append(books, Minify(siloItemToLibraryItem(b, lib, baseURL)))
	}
	obj["libraryItems"] = books
	return obj
}

// seriesObjectABS builds the real ABS Series.toOldJSON shape
// (server/models/Series.js). description/timestamps are absent in silo's
// catalog → null/0.
func seriesObjectABS(id, name, libraryID string, numBooks int) map[string]any {
	return map[string]any{
		"id":               id,
		"name":             name,
		"nameIgnorePrefix": titleIgnorePrefix(name),
		"description":      nil,
		"addedAt":          0,
		"updatedAt":        0,
		"libraryId":        libraryID,
		"numBooks":         numBooks,
	}
}

func seriesToABS(s Series, lib AudiobookLibrary, baseURL string) map[string]any {
	libID := audiobookLibraryID(lib)
	obj := seriesObjectABS(s.ID, s.Name, libID, len(s.Books))
	books := make([]MinifiedLibraryItem, 0, len(s.Books))
	for _, b := range s.Books {
		books = append(books, Minify(siloItemToLibraryItem(b, lib, baseURL)))
	}
	obj["books"] = books
	return obj
}

// seriesBookMinified builds a full real-ABS minified library item from the
// limited fields the series-LIST query carries (no full MediaItem). Every
// required minified key is present with a safe placeholder so strict clients
// (Plappa) decode the series card's books[] without crashing.
func seriesBookMinified(contentID, title, libID, baseURL string, updatedAtMs int64) MinifiedLibraryItem {
	return MinifiedLibraryItem{
		ID:          contentID,
		Ino:         contentID,
		LibraryID:   libID,
		FolderID:    VirtualFolderID,
		IsFile:      true,
		MtimeMs:     updatedAtMs,
		CtimeMs:     updatedAtMs,
		BirthtimeMs: updatedAtMs,
		AddedAt:     updatedAtMs,
		UpdatedAt:   updatedAtMs,
		MediaType:   LibraryMediaType,
		Media: minifiedMedia{
			ID: contentID,
			Metadata: minifiedMetadata{
				Title:             title,
				TitleIgnorePrefix: titleIgnorePrefix(title),
				Genres:            []string{},
			},
			CoverPath:     baseURL + "/api/items/" + contentID + "/cover",
			Tags:          []string{},
			NumTracks:     1,
			NumAudioFiles: 1,
		},
		NumFiles: 1,
	}
}
