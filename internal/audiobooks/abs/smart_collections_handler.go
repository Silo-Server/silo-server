package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"
	"github.com/Silo-Server/silo-server/internal/models"
)

// smartCollectionBody is the JSON body for POST and PATCH
// /me/smart-collections[/{id}]. Pointer fields support partial PATCH.
type smartCollectionBody struct {
	Name        *string                    `json:"name"`
	Description *string                    `json:"description"`
	Color       *string                    `json:"color"`
	IsPublic    *bool                      `json:"isPublic"`
	IsPinned    *bool                      `json:"isPinned"`
	QueryDef    *smartcoll.QueryDefinition `json:"query_def"`
}

// handleCreateSmartCollection — POST /me/smart-collections.
func (h *Handler) handleCreateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body smartCollectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	c := SmartCollection{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}

	qd := smartcoll.QueryDefinition{}
	if body.QueryDef != nil {
		qd = *body.QueryDef
	}
	qd = qd.Normalize()
	if err := qd.Validate(true); err != nil {
		http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
		return
	}
	qdBytes, err := json.Marshal(qd)
	if err != nil {
		slog.Error("abs smart collection marshal query_def failed", "err", err)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	c.QueryDef = qdBytes

	if err := h.deps.SmartCollectionStore.CreateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection create failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}

func (h *Handler) handleListSmartCollections(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	rows, err := h.deps.SmartCollectionStore.ListUserSmartCollections(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs smart collection list failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, smartCollectionToABS(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) handleGetSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), chiURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && !sameABSPrincipal(a, c.UserID, c.ProfileID) && !c.IsPublic) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get failed", "err", err)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(c))
}

func (h *Handler) handleUpdateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && !sameABSPrincipal(a, c.UserID, c.ProfileID)) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-update failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}

	var body smartCollectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		c.Name = *body.Name
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}
	if body.QueryDef != nil {
		qd := body.QueryDef.Normalize()
		if err := qd.Validate(true); err != nil {
			http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
			return
		}
		qdBytes, mErr := json.Marshal(qd)
		if mErr != nil {
			slog.Error("abs smart collection marshal query_def failed", "err", mErr)
			http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
			return
		}
		c.QueryDef = qdBytes
	}
	if err := h.deps.SmartCollectionStore.UpdateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection update failed", "err", err, "id", id)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}

func (h *Handler) handleDeleteSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && !sameABSPrincipal(a, c.UserID, c.ProfileID)) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-delete failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.SmartCollectionStore.DeleteSmartCollection(r.Context(), id); err != nil {
		slog.Error("abs smart collection delete failed", "err", err, "id", id)
		http.Error(w, "smart collection delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSmartCollectionItems — GET /me/smart-collections/{id}/items.
// Evaluates the collection's query_def against the audiobook catalog
// and returns a paged envelope. When the caller is the owner, per-user
// state is hydrated; non-owner viewing a public collection sees
// personalized rules silently dropped.
func (h *Handler) handleSmartCollectionItems(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && !sameABSPrincipal(a, c.UserID, c.ProfileID) && !c.IsPublic) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection items get failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}

	var qd smartcoll.QueryDefinition
	if len(c.QueryDef) > 0 {
		if uErr := json.Unmarshal(c.QueryDef, &qd); uErr != nil {
			slog.Error("abs smart collection invalid stored query_def", "err", uErr, "id", id)
			http.Error(w, "smart collection get failed", http.StatusInternalServerError)
			return
		}
	}
	qd = qd.Normalize()

	limit, page := readPagedQuery(r, 30)
	if r.URL.Query().Get("limit") == "" && qd.Limit != nil && *qd.Limit > 0 {
		limit = *qd.Limit
	}

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	allLibs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context(), access)
	if err != nil {
		slog.Warn("abs smart collection libraries fetch failed", "err", err, "id", id)
		allLibs = nil
	}
	libByID := make(map[int64]AudiobookLibrary, len(allLibs))
	for _, lib := range allLibs {
		libByID[lib.ID] = lib
	}
	var targetLibs []AudiobookLibrary
	if len(qd.LibraryIDs) > 0 {
		for _, lid := range qd.LibraryIDs {
			if lib, ok := libByID[lid]; ok {
				targetLibs = append(targetLibs, lib)
			}
		}
	} else {
		targetLibs = allLibs
	}

	owner := sameABSPrincipal(a, c.UserID, c.ProfileID)
	progressByID := map[string]ProgressRow{}
	bookmarkCountByID := map[string]int{}
	if owner {
		if h.deps.ProgressStore != nil {
			if rows, perr := h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 10000); perr == nil {
				for _, p := range rows {
					progressByID[p.ContentID] = p
				}
			}
		}
		if h.deps.BookmarkStore != nil {
			if counts, berr := h.deps.BookmarkStore.CountByUser(r.Context(), a.UserID, a.ProfileID); berr == nil {
				bookmarkCountByID = counts
			}
		}
	}

	candidates := make([]smartcoll.Candidate, 0, 256)
	for _, lib := range targetLibs {
		items, _, lerr := h.deps.MediaStore.ListAudiobooks(r.Context(), lib.ID, 0, 0, access)
		if lerr != nil {
			slog.Warn("abs smart collection list-audiobooks failed", "err", lerr, "library", lib.ID)
			continue
		}
		for _, mi := range items {
			cand := smartcoll.Candidate{Item: siloItemToSmartcollItem(mi)}
			if owner {
				if p, ok := progressByID[mi.ContentID]; ok {
					cand.IsFinished = p.IsFinished
					cand.ProgressPct = float32(p.ProgressPct)
					cand.CurrentSeconds = int(p.CurrentSeconds)
					cand.LastPlayedAt = p.UpdatedAt
				}
				cand.BookmarkCount = bookmarkCountByID[mi.ContentID]
			}
			candidates = append(candidates, cand)
		}
	}

	matched := smartcoll.Evaluate(r.Context(), qd, candidates, smartcoll.EvaluateOptions{
		AllowPersonalized: owner,
		UserSeed:          a.UserID + ":" + c.ID,
	})

	total := len(matched)
	start := page * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	pageSlice := matched[start:end]

	libDefault := h.resolveDefaultLibrary(r.Context(), access)
	libDefaultID := audiobookLibraryID(libDefault)
	results := make([]map[string]any, 0, len(pageSlice))
	for _, cand := range pageSlice {
		entry := map[string]any{
			"id":        cand.Item.ID,
			"libraryId": libDefaultID,
			"media": map[string]any{
				"metadata": map[string]any{"title": cand.Item.Title},
			},
		}
		results = append(results, entry)
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, qd.Sort.Field, qd.Sort.Order == "desc", "", false, ""))
}

// siloItemToSmartcollItem maps a silo *models.MediaItem into the
// audiobook-domain Item shape the smartcoll evaluator walks.
func siloItemToSmartcollItem(mi *models.MediaItem) smartcoll.Item {
	if mi == nil {
		return smartcoll.Item{}
	}
	it := smartcoll.Item{
		ID:              mi.ContentID,
		Title:           mi.Title,
		Genres:          mi.Genres,
		Year:            mi.Year,
		Language:        mi.OriginalLanguage,
		DurationSeconds: mi.Runtime,
	}
	for _, p := range mi.People {
		switch p.Kind {
		case models.PersonKindAuthor:
			it.Authors = append(it.Authors, p.Name)
		case models.PersonKindNarrator:
			it.Narrators = append(it.Narrators, p.Name)
		}
	}
	for _, s := range mi.AudiobookSeries {
		it.Series = append(it.Series, s.Name)
	}
	if len(mi.Studios) > 0 {
		it.Publisher = mi.Studios[0]
	}
	if mi.RatingIMDB != nil {
		it.Rating = *mi.RatingIMDB
	}
	if mi.AddedAt != nil {
		it.AddedAt = *mi.AddedAt
	}
	return it
}
