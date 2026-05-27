package abs

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// resolveDefaultLibrary returns the first audiobook library (the canonical
// "default" for response embedding) or a virtual fallback when the store
// is empty or errors. Centralizes the snippet that handleItem,
// handleSimilarItems, and handleItemsInProgress all need so the fallback
// shape stays consistent across all three response paths.
func (h *Handler) resolveDefaultLibrary(ctx context.Context, filters ...catalog.AccessFilter) AudiobookLibrary {
	access := emptyAccessFilter()
	if len(filters) > 0 {
		access = filters[0]
	}
	if libs, err := h.deps.MediaStore.ListAudiobookLibraries(ctx, access); err == nil && len(libs) > 0 {
		return libs[0]
	}
	return AudiobookLibrary{ID: 0, Name: VirtualLibraryName, Type: "audiobooks"}
}

// handleItem — GET /abs/api/items/{id} (and /api/items/{id})
//
// Returns the full ABS LibraryItem with audio track details for the given
// audiobook. The ABS mobile app fetches this when the user opens the
// item-detail page; it reads media.tracks.length to decide whether to render
// the play button and uses the track metadata for offline-download decisions.
func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID, access)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID, access)
	if err != nil {
		http.Error(w, "load files: "+err.Error(), http.StatusInternalServerError)
		return
	}

	lib := h.resolveDefaultLibrary(r.Context(), access)
	baseURL := h.absBaseURL(r)
	result := siloItemToLibraryItemDetail(item, files, lib, baseURL)
	writeJSON(w, http.StatusOK, result)
}

// handleSimilarItems — GET /abs/api/items/{id}/similar
//
// Returns similar audiobooks in the canonical ABS paged envelope so
// mobile clients can render the "Similar" rail. Sort metadata is
// "relevance" desc to match continuum-plugin-audiobooks; the envelope
// is emitted even when empty so clients that iterate
// `results`/`total` don't crash.
func (h *Handler) handleSimilarItems(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	const limit = 10
	emptyEnvelope := pagedEnvelope([]any{}, 0, limit, 0, "relevance", true, "", false, "")

	if h.deps.Recommender == nil {
		writeJSON(w, http.StatusOK, emptyEnvelope)
		return
	}

	ids, err := h.deps.Recommender.Similar(r.Context(), contentID, limit)
	if err != nil || len(ids) == 0 {
		writeJSON(w, http.StatusOK, emptyEnvelope)
		return
	}

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	lib := h.resolveDefaultLibrary(r.Context(), access)
	baseURL := h.absBaseURL(r)
	out := make([]LibraryItem, 0, len(ids))
	for _, id := range ids {
		si, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), id, access)
		if err != nil || si == nil {
			continue
		}
		out = append(out, siloItemToLibraryItem(si, lib, baseURL))
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(out, len(out), limit, 0, "relevance", true, "", false, ""))
}

// handleItemsInProgress — GET /abs/api/me/items-in-progress
//
// Returns the Continue Listening shelf. Queries the ProgressStore for in-
// progress rows, then hydrates each with a summary LibraryItem from the
// catalog. Items without a matching catalog entry are skipped silently.
func (h *Handler) handleItemsInProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.deps.ProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"libraryItems": []any{}})
		return
	}

	rows, err := h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 25)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	lib := h.resolveDefaultLibrary(r.Context(), access)
	baseURL := h.absBaseURL(r)
	items := make([]any, 0, len(rows))
	for _, p := range rows {
		if p.IsFinished || p.CurrentSeconds <= 0 {
			continue
		}
		si, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), p.ContentID, access)
		if err != nil || si == nil {
			continue
		}
		li := siloItemToLibraryItem(si, lib, baseURL)
		items = append(items, map[string]any{
			"id":        li.ID,
			"libraryId": li.LibraryID,
			"folderId":  li.FolderID,
			"mediaType": li.MediaType,
			"media":     li.Media,
			"numTracks": li.NumTracks,
			"addedAt":   li.AddedAt,
			"updatedAt": li.UpdatedAt,
			"userMediaProgress": map[string]any{
				"id":            a.UserID + "-" + p.ContentID,
				"libraryItemId": p.ContentID,
				"currentTime":   p.CurrentSeconds,
				"progress":      p.ProgressPct,
				"isFinished":    p.IsFinished,
				"lastUpdate":    p.UpdatedAt.UnixMilli(),
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraryItems": items})
}
