package sections

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
)

// HidesNotInterested identifies the sections that suggest titles the profile
// did not pick: discovery rows, where a title it marked Not interested stays
// out. Rows of what is new, what the profile watches, saved or chose, and
// rows an admin or the profile built, keep it. The recommendation rows leave
// marked titles out as they are read, so they are not listed here.
func HidesNotInterested(sectionType SectionType) bool {
	switch sectionType {
	case SectionHiddenGems, SectionCriticallyAcclaimed, SectionAwardWinners, SectionForgottenFavorites,
		SectionFormatShowcase, SectionEditorialSpotlight, SectionSeasonalThemed, SectionMoodCollection, SectionTrendingOnServer,
		SectionMostWatched, SectionTrendingDiscover, SectionGenreRoulette, SectionAnniversaries, SectionShortWatches:
		return true
	default:
		return false
	}
}

// withoutNotInterested drops the titles the profile marked Not interested
// from a fetched row, which may leave it shorter. The row is not fetched
// again wider: several discovery rows draw a daily sample whose size follows
// the row's limit, so a wider fetch would swap unrelated titles too. A
// failed lookup leaves the row as fetched, like the Home dismissals.
func (f *Fetcher) withoutNotInterested(ctx context.Context, resolved ResolvedSection, userID int, profileID string, items []*models.MediaItem, total int) ([]*models.MediaItem, int) {
	if f.StoreProvider == nil || userID <= 0 || profileID == "" || len(items) == 0 {
		return items, total
	}
	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		if err != nil {
			slog.ErrorContext(ctx, "opening the user store for Not interested marks", "component", "sections", "user_id", userID, "error", err)
		}
		return items, total
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	marked, err := store.ListNotInterestedByMediaItems(ctx, profileID, ids)
	if err != nil {
		slog.ErrorContext(ctx, "listing Not interested marks", "component", "sections", "profile_id", profileID, "section_id", resolved.ID, "error", err)
		return items, total
	}
	if len(marked) == 0 {
		return items, total
	}
	kept := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil || !marked[item.ContentID] {
			kept = append(kept, item)
		}
	}
	return kept, max(0, total-(len(items)-len(kept)))
}
