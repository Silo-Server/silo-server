package sections

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Resolve the title and items together, outside the shared, user-agnostic cache.
// Never guess the anchor from the latest watch: that anchor may have no cache.
// The reader picks the anchor within the section's library scope, so a
// library page passes over a recent watch with nothing to recommend there.
func (f *Fetcher) fetchBecauseWatchedWithTitle(ctx context.Context, section ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter, reader recommendationReader) (SectionWithItems, error) {
	result := SectionWithItems{ResolvedSection: section, Items: []*models.MediaItem{}}
	if userID <= 0 || profileID == "" {
		return result, nil
	}
	scope := collectionRailQueryAccess(filter, libraryID, libraryIDs).AllowedLibraryIDs
	scored, sourceID, err := reader.SectionBecauseYouWatched(ctx, userID, profileID, parseRecommendationSectionConfig(section.Config).anchor(), scope, filter)
	if err != nil {
		return result, err
	}
	items, err := f.scopeRecommendationItems(ctx, scored, libraryID, libraryIDs, filter)
	if err != nil {
		return result, err
	}
	result.Items = limitRecommendationItems(items, section.ItemLimit)
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
