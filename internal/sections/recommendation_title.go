package sections

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// A For You or taste-match section is not always served the profile's own
// row: a new profile's For You row is Popular or Recently Added, and a
// profile without a matching taste cluster gets a genre row. The section
// then takes the served row's title, so its heading describes what it shows.
// Like the Because You Watched heading, it is resolved with the items,
// outside the shared, user-agnostic cache.
func (f *Fetcher) fetchRecommendationRowWithTitle(ctx context.Context, section ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter, reader recommendationReader) (SectionWithItems, error) {
	result := SectionWithItems{ResolvedSection: section, Items: []*models.MediaItem{}}
	var row *recommendations.ForYouRow
	var err error
	switch section.SectionType {
	case SectionRecommendedForYou:
		row, err = reader.SectionForYouMain(ctx, userID, profileID, filter)
	case SectionTasteMatch:
		// An empty genre is valid: the reader auto-picks the profile's
		// strongest taste cluster (falling back to the server top genre).
		row, err = reader.SectionTasteMatchRow(ctx, userID, profileID, strings.TrimSpace(parseRecommendationSectionConfig(section.Config).Genre), filter)
	}
	if err != nil {
		return SectionWithItems{}, err
	}
	if row == nil {
		return result, nil
	}
	items, err := f.scopeRecommendationItems(ctx, row.Items, libraryID, libraryIDs, filter)
	if err != nil {
		return SectionWithItems{}, err
	}
	result.Items = limitRecommendationItems(items, section.ItemLimit)
	result.TotalCount = len(result.Items)
	if len(result.Items) > 0 {
		result.Title = recommendationSectionTitle(section.Title, row)
	}
	return result, nil
}

// The headings For You and taste-match sections are created with.
const (
	defaultRecommendedForYouTitle = "Recommended for You"
	defaultTasteMatchTitle        = "Top Picks Today"
)

var defaultRecommendationTitles = []string{defaultRecommendedForYouTitle, defaultTasteMatchTitle}

func hasDefaultRecommendationTitle(title string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return true
	}
	for _, t := range defaultRecommendationTitles {
		if strings.EqualFold(trimmed, t) {
			return true
		}
	}
	return false
}

// recommendationSectionTitle is the heading of a section titled title that
// serves row: the row's title when the row is one every profile is offered
// and the section still has a default heading, else title. A row built from
// the profile's history keeps the section's heading, and a heading an admin
// chose is never replaced.
func recommendationSectionTitle(title string, row *recommendations.ForYouRow) string {
	if row == nil || !row.Global() || row.Label == "" || !hasDefaultRecommendationTitle(title) {
		return title
	}
	return row.Label
}
