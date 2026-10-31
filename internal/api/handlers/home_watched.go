package handlers

import (
	"context"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// The Home hide-watched preference (home.hide_watched_items) removes items the
// acting profile has watched from ordinary Home sections. Featured sections
// and sections whose meaning depends on watch history keep them.
//
// The shared resolved-list cache stays unfiltered: filtered sections fetch a
// bounded larger candidate window, and watched items are removed per profile
// after the cache, then the row is cut back to its display limit. A profile
// that has watched most of a window sees a shorter row rather than a
// per-profile query on every Home load.

const (
	homeWatchedCandidateMultiplier = 5
	// Series and season watched state needs episode lookups, so the refill
	// window stays bounded instead of scanning the whole section.
	homeWatchedMaxExpandedCandidates = 200
)

// homeHidesWatchedItems resolves the acting profile's preference. It fails
// open: an unavailable preference store must not make media disappear.
func (h *SectionHandler) homeHidesWatchedItems(ctx context.Context) bool {
	userID := apimw.GetUserID(ctx)
	profileID := apimw.GetProfileID(ctx)
	if h.StoreProvider == nil || userID <= 0 || profileID == "" {
		return false
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return false
	}
	return sections.HideWatchedItemsFromHome(ctx, store, profileID)
}

func keepsWatchedItemsOnHome(section sections.ResolvedSection) bool {
	return section.Featured || sections.PreserveWatchedItemsOnHome(section.SectionType)
}

// homeSectionsForFetch widens the fetch window of filtered sections so rows can
// refill after watched items are removed.
func homeSectionsForFetch(resolved []sections.ResolvedSection, hideWatched bool) []sections.ResolvedSection {
	if !hideWatched {
		return resolved
	}
	fetch := make([]sections.ResolvedSection, len(resolved))
	copy(fetch, resolved)
	for i := range fetch {
		if keepsWatchedItemsOnHome(fetch[i]) {
			continue
		}
		fetch[i].ItemLimit = homeWatchedCandidateLimit(fetch[i].ItemLimit)
	}
	return fetch
}

// homeSectionDefaultItemLimit is the row size sections.Fetcher uses when a
// section sets no item limit.
const homeSectionDefaultItemLimit = 20

// effectiveHomeItemLimit is the number of items a section row shows: its
// configured limit, or the fetcher's default when none is set.
func effectiveHomeItemLimit(itemLimit int) int {
	if itemLimit <= 0 {
		return homeSectionDefaultItemLimit
	}
	return itemLimit
}

// homeWatchedCandidateLimit widens a filtered row's fetch window so it can
// refill after watched items are removed. It is a bound, not a guarantee: a
// profile that has watched most of the window gets a shorter row rather than a
// second fetch. The advertised item_limit stays as configured, as on the
// unfiltered path.
func homeWatchedCandidateLimit(displayLimit int) int {
	displayLimit = effectiveHomeItemLimit(displayLimit)
	if displayLimit >= homeWatchedMaxExpandedCandidates {
		return displayLimit
	}
	if displayLimit > homeWatchedMaxExpandedCandidates/homeWatchedCandidateMultiplier {
		return homeWatchedMaxExpandedCandidates
	}
	return displayLimit * homeWatchedCandidateMultiplier
}

// filterWatchedHomeSections restores each section's display limit, loads the
// profile's item states once, and removes watched items from filtered
// sections, cutting each back to its display limit. The loaded states are
// returned so building the response doesn't read them again.
func (h *SectionHandler) filterWatchedHomeSections(
	ctx context.Context,
	withItems []sections.SectionWithItems,
	resolved []sections.ResolvedSection,
) ([]sections.SectionWithItems, map[string]*itemUserStateResponse) {
	withItems = restoreHomeSectionDisplayLimits(withItems, resolved)
	userStates := h.listSectionItemUserStates(ctx, sectionMediaItems(withItems))
	return filterWatchedHomeSectionItems(withItems, userStates), userStates
}

func restoreHomeSectionDisplayLimits(withItems []sections.SectionWithItems, resolved []sections.ResolvedSection) []sections.SectionWithItems {
	displayLimits := make(map[string]int, len(resolved))
	for _, section := range resolved {
		displayLimits[section.ID] = section.ItemLimit
	}
	for i := range withItems {
		if displayLimit, ok := displayLimits[withItems[i].ID]; ok {
			withItems[i].ItemLimit = displayLimit
		}
	}
	return withItems
}

func sectionMediaItems(withItems []sections.SectionWithItems) []*models.MediaItem {
	items := make([]*models.MediaItem, 0)
	for _, section := range withItems {
		items = append(items, section.Items...)
	}
	return items
}

func filterWatchedHomeSectionItems(
	withItems []sections.SectionWithItems,
	userStates map[string]*itemUserStateResponse,
) []sections.SectionWithItems {
	filtered := make([]sections.SectionWithItems, len(withItems))
	copy(filtered, withItems)
	for i := range filtered {
		section := &filtered[i]
		limit := effectiveHomeItemLimit(section.ItemLimit)
		if keepsWatchedItemsOnHome(section.ResolvedSection) {
			if len(section.Items) > limit {
				section.Items = section.Items[:limit]
			}
			continue
		}
		items := make([]*models.MediaItem, 0, len(section.Items))
		for _, item := range section.Items {
			if item != nil && userStates[item.ContentID] != nil && userStates[item.ContentID].Played {
				continue
			}
			items = append(items, item)
			if len(items) == limit {
				break
			}
		}
		section.Items = items
	}
	return filtered
}

// dropEmptyWatchedHomeSections removes ordinary sections whose source has
// items but whose candidate window the filter emptied. The direct section
// endpoint still returns such a section when asked for it.
func dropEmptyWatchedHomeSections(withItems []sections.SectionWithItems) []sections.SectionWithItems {
	out := withItems[:0]
	for _, section := range withItems {
		if len(section.Items) == 0 && section.TotalCount > 0 && !keepsWatchedItemsOnHome(section.ResolvedSection) {
			continue
		}
		out = append(out, section)
	}
	return out
}
