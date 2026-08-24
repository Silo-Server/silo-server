package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

type audiobookGroupResponse struct {
	Name                 string   `json:"name"`
	ItemCount            int      `json:"item_count"`
	TotalDurationSeconds int64    `json:"total_duration_seconds"`
	InProgressCount      int      `json:"in_progress_count"`
	FinishedCount        int      `json:"finished_count"`
	PosterURLs           []string `json:"poster_urls"`
}

type audiobookGroupsResponse struct {
	Total      int                      `json:"total"`
	TotalExact bool                     `json:"total_exact"`
	HasMore    bool                     `json:"has_more"`
	Groups     []audiobookGroupResponse `json:"groups"`
}

// HandleGetAudiobookGroups — GET /api/v1/catalog/audiobook-groups
//
// Grouped browse for audiobook libraries: authors, narrators, or series with
// aggregate stats (book count, total duration, per-profile progress counts)
// and up to four poster URLs for cover stacks. Query parameters:
// library_id=<id> (required), group_by=author|narrator|series (required),
// sort=name|count|duration (default name), limit, offset, include_total, q.
//
// Group names round-trip into the corresponding catalog filter fields
// (author / narrator / series), which match case-insensitively.
func (h *CatalogHandler) HandleGetAudiobookGroups(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.itemsH == nil || h.itemsH.browseRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Catalog is not configured")
		return
	}

	libraryID, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("library_id")))
	if err != nil || libraryID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id must be a positive integer")
		return
	}

	groupBy, ok := catalog.ParseAudiobookGroupBy(r.URL.Query().Get("group_by"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "group_by must be one of author, narrator, series")
		return
	}

	sort := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	switch sort {
	case "", "name", "count", "duration":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "sort must be one of name, count, duration")
		return
	}

	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		limit = n
	}
	offset := max(catalog.ParseIntParam(r.URL.Query().Get("offset")), 0)
	includeTotal := true
	if raw := strings.TrimSpace(r.URL.Query().Get("include_total")); raw != "" {
		parsed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "include_total must be true or false")
			return
		}
		includeTotal = parsed
	}

	filter, ok := h.itemsH.accessFilterOrError(w, r)
	if !ok {
		return
	}

	result, err := catalog.ListAudiobookGroups(
		r.Context(),
		h.itemsH.browseRepo.Pool(),
		catalog.AudiobookGroupsQuery{
			LibraryID:    libraryID,
			GroupBy:      groupBy,
			SearchPrefix: strings.TrimSpace(r.URL.Query().Get("q")),
			IncludeTotal: includeTotal,
			Sort:         sort,
			Limit:        limit,
			Offset:       offset,
		},
		filter,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list audiobook groups")
		return
	}

	resolvedPosters := h.resolveAudiobookGroupPosterURLs(r, result.Groups, filter.ImageSize)
	resp := audiobookGroupsResponse{
		Total:      result.Total,
		TotalExact: result.TotalExact,
		HasMore:    result.HasMore,
		Groups:     make([]audiobookGroupResponse, 0, len(result.Groups)),
	}
	for _, g := range result.Groups {
		posterURLs := make([]string, 0, len(g.PosterPaths))
		for _, path := range g.PosterPaths {
			if resolved := resolvedPosters[sizedCardPath(path, artworkkey.ImagePoster, filter.ImageSize)]; resolved.URL != "" {
				posterURLs = append(posterURLs, resolved.URL)
			}
		}
		resp.Groups = append(resp.Groups, audiobookGroupResponse{
			Name:                 g.Name,
			ItemCount:            g.ItemCount,
			TotalDurationSeconds: g.TotalDurationSeconds,
			InProgressCount:      g.InProgressCount,
			FinishedCount:        g.FinishedCount,
			PosterURLs:           posterURLs,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// resolveAudiobookGroupPosterURLs presigns the cover-stack posters for a page of
// groups. size is the already-validated size off the request's access filter, so
// the ladder rung and the plugin hint match the rest of the response instead of
// being re-derived per item.
func (h *CatalogHandler) resolveAudiobookGroupPosterURLs(r *http.Request, groups []catalog.AudiobookGroup, size imagesize.Size) map[string]catalog.ResolvedImageURL {
	if h == nil || h.itemsH == nil || h.itemsH.detailSvc == nil || len(groups) == 0 {
		return map[string]catalog.ResolvedImageURL{}
	}

	paths := make([]string, 0, len(groups)*4)
	seen := make(map[string]struct{}, len(groups)*4)
	for _, group := range groups {
		for _, path := range group.PosterPaths {
			normalized := sizedCardPath(path, artworkkey.ImagePoster, size)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			paths = append(paths, normalized)
		}
	}
	return h.itemsH.detailSvc.PresignURLsWithExpiry(r.Context(), paths, requestVariantHint("card", size))
}
