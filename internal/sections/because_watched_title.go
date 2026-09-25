package sections

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

type becauseWatchedSourceReader interface {
	GetBecauseYouWatchedWithSource(context.Context, int, string, string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, string, error)
}

// Resolve the title and items together, outside the shared, user-agnostic cache.
// Never guess the anchor from the latest watch: that anchor may have no cache.
func (f *Fetcher) fetchBecauseWatchedWithTitle(ctx context.Context, section ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter, reader becauseWatchedSourceReader) (SectionWithItems, error) {
	result := SectionWithItems{ResolvedSection: section, Items: []*models.MediaItem{}}
	if userID <= 0 || profileID == "" {
		return result, nil
	}
	scored, sourceID, err := reader.GetBecauseYouWatchedWithSource(ctx, userID, profileID, parseRecommendationSectionConfig(section.Config).anchor(), section.ItemLimit, filter)
	if err != nil {
		return result, err
	}
	if len(scored) == 0 {
		return result, nil
	}
	ids := make([]string, len(scored))
	for i, item := range scored {
		ids[i] = item.MediaItemID
	}
	items, err := f.fetchItemsByContentIDs(ctx, ids, libraryID, libraryIDs, filter)
	if err != nil {
		return result, err
	}
	result.Items = orderMediaItems(items, ids)
	result.TotalCount = len(result.Items)
	if len(result.Items) > 0 && hasDefaultBecauseWatchedTitle(section.Title) {
		var sources []*models.MediaItem
		if sourceID != "" {
			// Apply the same library and access restrictions to the heading as the cards.
			// The heading is optional, so a failed lookup keeps the cards.
			var lookupErr error
			sources, lookupErr = f.fetchItemsByContentIDs(ctx, []string{sourceID}, libraryID, libraryIDs, filter)
			if lookupErr != nil {
				slog.WarnContext(ctx, "because-you-watched: source title lookup failed", "component", "sections", "section_id", section.ID, "error", lookupErr)
			}
		}
		result.Title = becauseWatchedTitle(section.Title, sourceID, sources)
	}
	return result, nil
}

const defaultBecauseWatchedTitle = "Because You Watched"

func hasDefaultBecauseWatchedTitle(title string) bool {
	trimmed := strings.TrimSpace(title)
	return trimmed == "" || strings.EqualFold(trimmed, defaultBecauseWatchedTitle)
}

// becauseWatchedTitle names the anchor only when the section still has the
// default heading, so a title an admin chose is never replaced.
func becauseWatchedTitle(fallback, sourceID string, sources []*models.MediaItem) string {
	if !hasDefaultBecauseWatchedTitle(fallback) {
		return fallback
	}
	for _, item := range sources {
		if item != nil && item.ContentID == sourceID && strings.TrimSpace(item.Title) != "" {
			return defaultBecauseWatchedTitle + " " + strings.TrimSpace(item.Title)
		}
	}
	if strings.TrimSpace(fallback) == "" {
		return defaultBecauseWatchedTitle
	}
	return fallback
}
