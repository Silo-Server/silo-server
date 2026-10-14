package sections

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ListOverlaySummaries returns each card's best accessible file badges without
// transferring every episode's track metadata for series cards.
func (f *Fetcher) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	return catalog.ListOverlaySummaries(ctx, f.pool, contentIDs, filter)
}

// SeriesSeasonAvailability counts episodes per season for the poster badge.
func (f *Fetcher) SeriesSeasonAvailability(ctx context.Context, seriesContentIDs []string) (map[string]map[int]catalog.SeasonAvailability, error) {
	if f == nil || f.pool == nil {
		return map[string]map[int]catalog.SeasonAvailability{}, nil
	}
	return catalog.NewItemRepository(f.pool).SeriesSeasonAvailability(ctx, seriesContentIDs)
}

// EpisodeMetadataIncomplete reports series whose episode list cannot back a badge.
func (f *Fetcher) EpisodeMetadataIncomplete(ctx context.Context, seriesContentIDs []string) (map[string]bool, error) {
	if f == nil || f.pool == nil {
		return map[string]bool{}, nil
	}
	return catalog.NewItemRepository(f.pool).EpisodeMetadataIncomplete(ctx, seriesContentIDs)
}
