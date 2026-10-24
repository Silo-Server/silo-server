package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

// seasonEpisodesByNumber lists a season's episodes by series and number,
// including the ones the library lacks when the viewer asked for them.
func (h *CatalogResourceHandler) seasonEpisodesByNumber(ctx context.Context, v ItemViewer, seriesID string, num int) ([]*models.Episode, error) {
	if v.IncludeMissing {
		return h.items.episodeRepo.ListBySeasonWithMissing(ctx, seriesID, num, v.IncludeUpcoming)
	}
	return h.items.episodeRepo.ListBySeason(ctx, seriesID, num)
}

// seasonEpisodesByID is seasonEpisodesByNumber for a season row.
func (h *CatalogResourceHandler) seasonEpisodesByID(ctx context.Context, v ItemViewer, seasonID string) ([]*models.Episode, error) {
	if v.IncludeMissing {
		return h.items.episodeRepo.ListBySeasonIDWithMissing(ctx, seasonID, v.IncludeUpcoming)
	}
	return h.items.episodeRepo.ListBySeasonID(ctx, seasonID)
}

// missingEpisodeCounts counts, per numbered season, the series' episodes the
// library lacks; nil when the viewer did not ask for them.
func (h *CatalogResourceHandler) missingEpisodeCounts(ctx context.Context, v ItemViewer, seriesID string) (map[int]int, error) {
	if !v.IncludeMissing {
		return nil, nil
	}
	return h.items.episodeRepo.CountMissingBySeason(ctx, seriesID, v.IncludeUpcoming)
}

// episodeResponsesWithAvailability is buildEpisodeResponses carrying each
// episode's Availability, which localization does not preserve.
func (h *ItemsHandler) episodeResponsesWithAvailability(ctx context.Context, v ItemViewer, episodes []*models.Episode) []episodeResponse {
	availability := make(map[string]string)
	for _, ep := range episodes {
		if ep != nil && ep.Availability != "" {
			availability[ep.ContentID] = ep.Availability
		}
	}
	resp := h.buildEpisodeResponses(ctx, v, episodes)
	if len(availability) == 0 {
		return resp
	}
	for i := range resp {
		resp[i].Availability = availability[resp[i].ContentID]
	}
	return resp
}
