package handlers

import (
	"context"

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
	Next       *catalog.AudiobookGroupCursor `json:"-"`
	Total      int                           `json:"total"`
	TotalExact bool                          `json:"total_exact"`
	HasMore    bool                          `json:"has_more"`
	Groups     []audiobookGroupResponse      `json:"groups"`
}

// audiobookGroupsView renders a page of groups with presigned cover stacks.
func (h *CatalogHandler) audiobookGroupsView(ctx context.Context, result catalog.AudiobookGroupsResult, filter catalog.AccessFilter) audiobookGroupsResponse {
	resolvedPosters := h.resolveAudiobookGroupPosterURLs(ctx, result.Groups, filter.ImageSizeFor(imagesize.SlotPoster))
	resp := audiobookGroupsResponse{
		Total:      result.Total,
		Next:       result.Next,
		TotalExact: result.TotalExact,
		HasMore:    result.HasMore,
		Groups:     make([]audiobookGroupResponse, 0, len(result.Groups)),
	}
	for _, g := range result.Groups {
		posterURLs := make([]string, 0, len(g.PosterPaths))
		for _, path := range g.PosterPaths {
			if resolved := resolvedPosters[sizedCardPath(path, artworkkey.ImagePoster, filter.ImageSizeFor(imagesize.SlotPoster))]; resolved.URL != "" {
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
	return resp
}

// resolveAudiobookGroupPosterURLs presigns the cover-stack posters for a page of
// groups. size is the already-validated size off the request's access filter, so
// the ladder rung and the plugin hint match the rest of the response instead of
// being re-derived per item.
func (h *CatalogHandler) resolveAudiobookGroupPosterURLs(ctx context.Context, groups []catalog.AudiobookGroup, size imagesize.Size) map[string]catalog.ResolvedImageURL {
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
	return h.itemsH.detailSvc.PresignURLsWithExpiry(ctx, paths, requestVariantHint("card", size))
}
