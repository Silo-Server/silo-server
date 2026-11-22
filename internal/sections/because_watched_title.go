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
	if len(result.Items) > 0 {
		f.nameBecauseWatchedAnchor(ctx, &result, sourceID, libraryID, libraryIDs, filter)
	}
	return result, nil
}

// nameBecauseWatchedAnchor sets the heading of a filled Because You Watched
// row to name its anchor.
func (f *Fetcher) nameBecauseWatchedAnchor(ctx context.Context, result *SectionWithItems, sourceID string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) {
	var sources []*models.MediaItem
	if sourceID != "" {
		// Apply the same library and access restrictions to the heading as the cards.
		// The heading is optional, so a failed lookup keeps the cards.
		var lookupErr error
		sources, lookupErr = f.fetchItemsByContentIDs(ctx, []string{sourceID}, libraryID, libraryIDs, filter)
		if lookupErr != nil {
			slog.WarnContext(ctx, "because-you-watched: source title lookup failed", "component", "sections", "section_id", result.ID, "error", lookupErr)
		}
	}
	configured := result.Title
	var anchor string
	result.Title, anchor = becauseWatchedTitle(configured, sourceID, sources)
	// Only the default heading has a translated form; a name an admin typed
	// is shown as typed, with the anchor after it.
	if anchor != "" && hasDefaultBecauseWatchedTitle(configured) {
		result.TitleKey, result.TitleArgs = titleKeyBecauseYouWatchedTitle, map[string]string{titleArgTitle: anchor}
	}
}

const defaultBecauseWatchedTitle = "Because You Watched"

func hasDefaultBecauseWatchedTitle(title string) bool {
	trimmed := strings.TrimSpace(title)
	return trimmed == "" || strings.EqualFold(trimmed, defaultBecauseWatchedTitle)
}

// becauseWatchedTitle names the anchor in the heading: after the default
// heading ("Because You Watched Dune"), or after a name an admin chose, which
// is kept as typed ("More Like This: Dune", #4141). It also returns the
// anchor's title when the heading names it, else "".
func becauseWatchedTitle(fallback, sourceID string, sources []*models.MediaItem) (title, anchor string) {
	for _, item := range sources {
		if item != nil && item.ContentID == sourceID && strings.TrimSpace(item.Title) != "" {
			anchor = strings.TrimSpace(item.Title)
			if !hasDefaultBecauseWatchedTitle(fallback) {
				// A name typed as "More Like:" gets one colon, not two.
				name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(fallback), ":"))
				return name + ": " + anchor, anchor
			}
			return defaultBecauseWatchedTitle + " " + anchor, anchor
		}
	}
	if !hasDefaultBecauseWatchedTitle(fallback) {
		return fallback, ""
	}
	if strings.TrimSpace(fallback) == "" {
		return defaultBecauseWatchedTitle, ""
	}
	return fallback, ""
}
