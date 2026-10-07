package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// The Featured adult preference (home.show_adult_in_featured) decides whether
// titles certified 18 or over may appear in Home featured sections. It is off
// by default, so they are left out unless the acting profile asked for them.
// Ordinary sections are untouched.
//
// Like the hide-watched preference, this runs per profile after the shared
// resolved-list cache, which stays unfiltered. The profile's rating limit has
// already been applied by the fetch, so the preference can only narrow what a
// featured section shows. A featured section is not refilled: one that loses
// titles is shorter.

// homeShowsAdultInFeatured resolves the acting profile's preference. It reads
// as false when the preference store is unavailable, which keeps adult titles
// out of Featured: the same result as the default.
func (h *SectionHandler) homeShowsAdultInFeatured(ctx context.Context) bool {
	userID := apimw.GetUserID(ctx)
	profileID := apimw.GetProfileID(ctx)
	if h.StoreProvider == nil || userID <= 0 || profileID == "" {
		return false
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return false
	}
	return sections.ShowAdultInFeatured(ctx, store, profileID)
}

// filterAdultFeaturedSections removes adult titles from featured sections.
func filterAdultFeaturedSections(withItems []sections.SectionWithItems) []sections.SectionWithItems {
	filtered := make([]sections.SectionWithItems, len(withItems))
	copy(filtered, withItems)
	for i := range filtered {
		section := &filtered[i]
		if !section.Featured {
			continue
		}
		items := make([]*models.MediaItem, 0, len(section.Items))
		for _, item := range section.Items {
			if item != nil && access.IsAdult(item.ContentRating) {
				continue
			}
			items = append(items, item)
		}
		section.Items = items
	}
	return filtered
}

// dropEmptyAdultFeaturedSections removes featured sections whose source has
// items but which the filter emptied, so Home never leads with an empty hero.
// The direct section endpoint still returns such a section when asked for it.
func dropEmptyAdultFeaturedSections(withItems []sections.SectionWithItems) []sections.SectionWithItems {
	out := withItems[:0]
	for _, section := range withItems {
		if section.Featured && len(section.Items) == 0 && section.TotalCount > 0 {
			continue
		}
		out = append(out, section)
	}
	return out
}
