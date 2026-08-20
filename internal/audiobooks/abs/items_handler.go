package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// resolveDefaultLibrary returns the first audiobook library (the canonical
// default for legacy response surfaces) or a virtual fallback when the store
// is empty or errors.
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

// resolveLibrariesForItems keeps global item-shaped responses attached to an
// actual source library. The production batch capability prevents N+1
// membership lookups; test doubles without it retain a type-based fallback.
func (h *Handler) resolveLibrariesForItems(ctx context.Context, items []*models.MediaItem, access catalog.AccessFilter) map[string]AudiobookLibrary {
	resolved := make(map[string]AudiobookLibrary, len(items))
	libs, err := h.deps.MediaStore.ListAudiobookLibraries(ctx, access)
	if err != nil {
		libs = nil
	}
	libsByID := make(map[int64]AudiobookLibrary, len(libs))
	for _, lib := range libs {
		libsByID[lib.ID] = lib
	}
	memberships := map[string]int64{}
	if store, ok := h.deps.MediaStore.(itemLibraryBatchStore); ok {
		ids := make([]string, 0, len(items))
		for _, item := range items {
			if item != nil {
				ids = append(ids, item.ContentID)
			}
		}
		if batch, batchErr := store.GetItemLibraryIDs(ctx, ids, access); batchErr == nil {
			memberships = batch
		}
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		if lib, ok := libsByID[memberships[item.ContentID]]; ok {
			resolved[item.ContentID] = lib
			continue
		}
		lib := resolveFallbackLibrary(item.Type)
		for _, candidate := range libs {
			if (item.Type == mediaTypeEbook && (candidate.Type == mediaTypeEbook || candidate.Type == libraryTypeEbooks)) ||
				(item.Type == mediaTypeAudiobook && candidate.Type != mediaTypeEbook && candidate.Type != libraryTypeEbooks) {
				lib = candidate
				break
			}
		}
		resolved[item.ContentID] = lib
	}
	return resolved
}

func resolveFallbackLibrary(itemType string) AudiobookLibrary {
	if itemType == mediaTypeEbook {
		return AudiobookLibrary{ID: 0, Name: "Books", Type: mediaTypeEbook}
	}
	return AudiobookLibrary{ID: 0, Name: VirtualLibraryName, Type: "audiobooks"}
}

// mapLibraryItems attaches every item to its actual source library and fills
// ebookFile/ebookFormat for reader-capable entries. Global aggregate surfaces
// can contain both audiobooks and ebooks, so one default library is not a safe
// mapping input.
func (h *Handler) mapLibraryItems(ctx context.Context, items []*models.MediaItem, baseURL string, access catalog.AccessFilter) []LibraryItem {
	libraries := h.resolveLibrariesForItems(ctx, items, access)
	entries := make([]LibraryItem, 0, len(items))
	ebookIndexes := make([]int, 0, len(items))
	ebookEntries := make([]LibraryItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		entry := siloItemToLibraryItem(item, libraries[item.ContentID], baseURL)
		entries = append(entries, entry)
		if item.Type == mediaTypeEbook {
			ebookIndexes = append(ebookIndexes, len(entries)-1)
			ebookEntries = append(ebookEntries, entry)
		}
	}
	ebookEntries = h.enrichEbookLibraryItems(ctx, ebookEntries, access)
	for i, entryIndex := range ebookIndexes {
		entries[entryIndex] = ebookEntries[i]
	}
	return entries
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
		h.writeAccessResolutionError(w, r, err)
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

	lib := h.resolveLibrariesForItems(r.Context(), []*models.MediaItem{item}, access)[item.ContentID]
	baseURL := h.absBaseURL(r)
	result := h.siloItemToLibraryItemDetail(r.Context(), item, files, lib, baseURL)
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) siloItemToLibraryItemDetail(ctx context.Context, item *models.MediaItem, files []*models.MediaFile, lib AudiobookLibrary, baseURL string) LibraryItem {
	base := siloItemToLibraryItem(item, lib, baseURL)
	if item.Type != mediaTypeEbook {
		return siloItemToLibraryItemDetail(item, files, lib, baseURL)
	}
	primaryID, configured, hasPrimary, err := h.ebookPrimary(ctx, item.ContentID)
	if err != nil {
		return siloEbookToLibraryItemDetail(base, files, 0, false, false)
	}
	return siloEbookToLibraryItemDetail(base, files, primaryID, configured, hasPrimary)
}

func (h *Handler) ebookPrimary(ctx context.Context, contentID string) (int, bool, bool, error) {
	store, ok := h.deps.MediaStore.(ebookPrimaryStore)
	if !ok {
		return 0, false, false, nil
	}
	return store.GetPrimaryEbookFileID(ctx, contentID)
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
		h.writeAccessResolutionError(w, r, err)
		return
	}
	baseURL := h.absBaseURL(r)
	byID, err := h.deps.MediaStore.GetAudiobooksByIDs(r.Context(), ids, access)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ordered := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids { // preserve recommender order
		si := byID[id]
		if si == nil {
			continue
		}
		ordered = append(ordered, si)
	}
	out := h.mapLibraryItems(r.Context(), ordered, baseURL, access)
	writeJSON(w, http.StatusOK, pagedEnvelope(out, len(out), limit, 0, "relevance", true, "", false, ""))
}

// handleItemsInProgress — GET /abs/api/me/items-in-progress
//
// Matches server/controllers/MeController.js `getAllLibraryItemsInProgress`:
// the envelope is `{ libraryItems: [...] }` and each entry is the item's
// `toOldJSONMinified()` shape spread with a flat `progressLastUpdate` (ms)
// field — real ABS does NOT wrap progress in a nested `userMediaProgress`
// object for this endpoint (that shape belongs to other responses, e.g.
// item-detail). Queries the ProgressStore for in-progress rows, then
// hydrates each with a minified LibraryItem from the catalog. Items
// without a matching catalog entry are skipped silently.
func (h *Handler) handleItemsInProgress(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if h.deps.ProgressStore == nil && h.deps.EbookProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"libraryItems": []any{}})
		return
	}

	var audioRows []ProgressRow
	var ebookRows []EbookProgress
	var err error
	if h.deps.ProgressStore != nil {
		audioRows, err = h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 25)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if h.deps.EbookProgressStore != nil {
		ebookRows, err = h.deps.EbookProgressStore.ListEbookProgress(r.Context(), a.UserID, a.ProfileID, 25)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		h.writeAccessResolutionError(w, r, err)
		return
	}
	baseURL := h.absBaseURL(r)
	type candidate struct {
		contentID string
		updatedAt int64
	}
	candidates := make([]candidate, 0, len(audioRows)+len(ebookRows))
	for _, p := range audioRows {
		if !p.IsFinished && p.CurrentSeconds > 0 {
			candidates = append(candidates, candidate{contentID: p.ContentID, updatedAt: p.UpdatedAt.UnixMilli()})
		}
	}
	for _, p := range ebookRows {
		if p.Progress > 0 && p.Progress < models.EbookFinishedProgressThreshold {
			candidates = append(candidates, candidate{contentID: p.ContentID, updatedAt: p.UpdatedAt.UnixMilli()})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].updatedAt > candidates[j].updatedAt })
	if len(candidates) > 25 {
		candidates = candidates[:25]
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.contentID)
	}
	byID, err := h.deps.MediaStore.GetAudiobooksByIDs(r.Context(), ids, access)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type candidateItem struct {
		candidate candidate
		entry     LibraryItem
		isEbook   bool
	}
	candidateItems := make([]candidateItem, 0, len(candidates))
	itemsForLibraries := make([]*models.MediaItem, 0, len(candidates))
	for _, candidate := range candidates {
		if si := byID[candidate.contentID]; si != nil {
			itemsForLibraries = append(itemsForLibraries, si)
		}
	}
	libraries := h.resolveLibrariesForItems(r.Context(), itemsForLibraries, access)
	for _, candidate := range candidates {
		si := byID[candidate.contentID]
		if si == nil {
			continue
		}
		candidateItems = append(candidateItems, candidateItem{
			candidate: candidate,
			entry:     siloItemToLibraryItem(si, libraries[si.ContentID], baseURL),
			isEbook:   si.Type == mediaTypeEbook,
		})
	}
	ebookEntries := make([]LibraryItem, 0, len(candidateItems))
	for _, item := range candidateItems {
		if item.isEbook {
			ebookEntries = append(ebookEntries, item.entry)
		}
	}
	ebookEntries = h.enrichEbookLibraryItems(r.Context(), ebookEntries, access)
	ebookIndex := 0
	items := make([]any, 0, len(candidateItems))
	for _, item := range candidateItems {
		entry := item.entry
		if item.isEbook {
			entry = ebookEntries[ebookIndex]
			ebookIndex++
		}
		mli := Minify(entry)
		wire := minifiedItemToWireMap(mli)
		wire["progressLastUpdate"] = item.candidate.updatedAt
		items = append(items, wire)
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraryItems": items})
}

// minifiedItemToWireMap reuses the json tags on MinifiedLibraryItem so a
// caller can merge extra keys (e.g. progressLastUpdate) into it inside a
// heterogeneous map[string]any envelope, mirroring the spread-operator
// pattern real ABS uses (`{ ...libraryItem.toOldJSONMinified(), ... }`).
func minifiedItemToWireMap(mli MinifiedLibraryItem) map[string]any {
	b, _ := json.Marshal(mli)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
