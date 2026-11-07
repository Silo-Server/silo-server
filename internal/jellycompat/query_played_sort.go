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
// DatePlayed is the played list's own order. A request with no SortBy, only
// keys Silo does not map, or a key that maps to no catalog order (IndexNumber
// and the other episode-order keys) keeps that list.
//
// The media-item browse holds no episodes and only an Episode-only request
// browses episodes, so a request with no type filter, or one that mixes
// Episode with other types, keeps the played list rather than lose its
// watched episodes. ItemsHandler.browsesPlayedListByCatalog also checks that
// an Episode-only request has an episode browse to go to.
func (q itemsQuery) sortsPlayedListByCatalog() bool {
	if q.isPlayed == nil || !*q.isPlayed || q.sortByKey == sortKeyDatePlayed {
		return false
	}
	if mapped, ok := sortKey(q.sortByKey); !ok || mapped == "" {
		return false
	}
	if !q.hasItemTypeFilter || len(q.itemTypes) == 0 {
		return false
	}
	return q.episodeOnly() || !itemTypesContain(q.itemTypes, compatEpisodeType)
}

// episodeOnly reports whether the request's item types are Episode alone.
func (q itemsQuery) episodeOnly() bool {
	return slices.Equal(q.itemTypes, []string{compatEpisodeType})
}

// browsesPlayedListByCatalog is sortsPlayedListByCatalog for this handler: an
// Episode-only request goes to the catalog browse only when the handler has
// an episode browse, since the media-item browse would return no episodes.
func (h *ItemsHandler) browsesPlayedListByCatalog(query itemsQuery) bool {
	if !query.sortsPlayedListByCatalog() {
		return false
	}
	if query.episodeOnly() {
		_, ok := h.episodeOnlyBrowser(query)
		return ok
	}
	return true
}
