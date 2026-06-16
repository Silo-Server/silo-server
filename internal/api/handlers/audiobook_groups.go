package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
	Total  int                      `json:"total"`
	Groups []audiobookGroupResponse `json:"groups"`
}

// HandleGetAudiobookGroups — GET /api/v1/catalog/audiobook-groups
//
// Grouped browse for audiobook libraries: authors, narrators, or series with
// aggregate stats (book count, total duration, per-profile progress counts)
// and up to four poster URLs for cover stacks. Query parameters:
// library_id=<id> (required), group_by=author|narrator|series (required),
// sort=name|count|duration (default name), limit, offset.
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

	// maxAudiobookGroupsLimit bounds a single page. Paging is now an in-memory
	// slice of the cached full list, so this is the response-size cap (the old
	// 500/page cap in ListAudiobookGroups no longer sits on this path).
	const maxAudiobookGroupsLimit = 5000
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		if n > maxAudiobookGroupsLimit {
			n = maxAudiobookGroupsLimit
		}
		limit = n
	}
	offset := max(catalog.ParseIntParam(r.URL.Query().Get("offset")), 0)

	groups, total, err := h.audiobookGroups().Page(
		r.Context(),
		catalog.AudiobookGroupsQuery{
			LibraryID: libraryID,
			GroupBy:   groupBy,
			Sort:      sort,
			Limit:     limit,
			Offset:    offset,
		},
		h.itemsH.accessFilter(r),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list audiobook groups")
		return
	}

	resp := audiobookGroupsResponse{Total: total, Groups: make([]audiobookGroupResponse, 0, len(groups))}
	for _, g := range groups {
		posterURLs := make([]string, 0, len(g.PosterPaths))
		for _, path := range g.PosterPaths {
			if url := h.itemsH.presignURL(r, cardThumbnailPath(path), "card"); url != "" {
				posterURLs = append(posterURLs, url)
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
