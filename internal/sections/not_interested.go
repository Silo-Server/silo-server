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
		SectionEditorialSpotlight, SectionSeasonalThemed, SectionMoodCollection, SectionTrendingOnServer,
		SectionMostWatched, SectionTrendingDiscover, SectionGenreRoulette, SectionAnniversaries, SectionShortWatches:
		return true
	default:
		return false
	}
}

// notInterestedRefill is how many more titles a section fetches to refill
// a row of limit after marked titles leave it: half the row, at least 20.
func notInterestedRefill(limit int) int {
	return max(limit/2, 20)
}

// withoutNotInterested drops the titles the profile marked Not interested
// from a fetched row. With refetch, a row that loses any is fetched again
// with notInterestedRefill more titles, filtered and cut back to its limit,
// so it keeps its length unless the profile marked more than the refill;
// without, the row is only filtered. A failed lookup leaves the row as
// fetched, like the Home dismissals.
func (f *Fetcher) withoutNotInterested(
	ctx context.Context,
	resolved ResolvedSection,
	userID int,
	profileID string,
	items []*models.MediaItem,
	total int,
	refetch func(ResolvedSection) ([]*models.MediaItem, int, error),
) ([]*models.MediaItem, int, error) {
	if f.StoreProvider == nil || userID <= 0 || profileID == "" || len(items) == 0 {
		return items, total, nil
	}
	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		if err != nil {
			slog.ErrorContext(ctx, "opening the user store for Not interested marks", "component", "sections", "user_id", userID, "error", err)
		}
		return items, total, nil
	}
	marked := func(items []*models.MediaItem) (map[string]bool, bool) {
		ids := make([]string, 0, len(items))
		for _, item := range items {
			if item != nil {
				ids = append(ids, item.ContentID)
			}
		}
		set, err := store.ListNotInterestedByMediaItems(ctx, profileID, ids)
		if err != nil {
			slog.ErrorContext(ctx, "listing Not interested marks", "component", "sections", "profile_id", profileID, "section_id", resolved.ID, "error", err)
			return nil, false
		}
		return set, true
	}
	set, ok := marked(items)
	if !ok || len(set) == 0 {
		return items, total, nil
	}
	limit := resolved.ItemLimit
	if limit > 0 && refetch != nil {
		wider := resolved
		wider.ItemLimit = limit + notInterestedRefill(limit)
		if more, moreTotal, err := refetch(wider); err == nil {
			if moreSet, ok := marked(more); ok {
				items, total, set = more, moreTotal, moreSet
			}
		} else {
			slog.WarnContext(ctx, "refilling a section after Not interested marks failed", "component", "sections", "section_id", resolved.ID, "error", err)
		}
	}
	kept := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item != nil && set[item.ContentID] {
			continue
		}
		if limit > 0 && len(kept) == limit {
			break
		}
		kept = append(kept, item)
	}
	return kept, max(0, total-len(set)), nil
}
