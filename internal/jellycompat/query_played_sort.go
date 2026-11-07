package jellycompat

import (
	"slices"
	"strings"
)

const (
	// sortKeySortName and sortKeyDatePlayed are lowercased Jellyfin SortBy
	// keys, as selectedSortKey returns them.
	sortKeySortName   = "sortname"
	sortKeyDatePlayed = "dateplayed"
	// sortOrderAscending is the browse order mapSortOrder returns for
	// SortOrder=Ascending.
	sortOrderAscending = "asc"
	// progressStatusCompleted selects watched rows from the user-data service.
	progressStatusCompleted = "completed"
)

// selectedSortKey returns the SortBy key parseSort selects, lowercased, or ""
// when no key maps. DatePlayed and DateCreated map to the same browse sort, so
// callers that must tell them apart read the key itself.
func selectedSortKey(sortBy string) string {
	for _, key := range strings.Split(sortBy, ",") {
		if _, ok := sortKey(key); ok {
			return strings.ToLower(strings.TrimSpace(key))
		}
	}
	return ""
}

// sortsPlayedListByCatalog reports whether a Filters=IsPlayed request asks for
// a catalog order (SortName, DateCreated, ...) that the played list, read from
// progress rows, cannot apply, and that the catalog browse can serve whole.
// DatePlayed is the played list's own order, and a request with no SortBy, or
// only keys Silo does not map, keeps that list.
//
// The media-item browse holds no episodes and only an Episode-only request
// browses episodes, so a request with no type filter, or one that mixes
// Episode with other types, keeps the played list rather than lose its
// watched episodes.
func (q itemsQuery) sortsPlayedListByCatalog() bool {
	if q.isPlayed == nil || !*q.isPlayed || q.sortByKey == "" || q.sortByKey == sortKeyDatePlayed {
		return false
	}
	if !q.hasItemTypeFilter || len(q.itemTypes) == 0 {
		return false
	}
	return slices.Equal(q.itemTypes, []string{compatEpisodeType}) || !itemTypesContain(q.itemTypes, compatEpisodeType)
}
