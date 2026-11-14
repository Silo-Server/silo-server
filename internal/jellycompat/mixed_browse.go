package jellycompat

// Mixed-type item browse. A query may name Episode beside other item types
// (IncludeItemTypes=Episode,Movie); the media_items browse holds no episodes,
// so serving only it would silently drop them.

import (
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// mixedEpisodeBrowser returns the episode browse when query lists Episode
// beside other types (e.g. IncludeItemTypes=Episode,Movie) and no Ids. The
// media_items browse holds no episodes, so on its own it silently dropped them.
func (h *ItemsHandler) mixedEpisodeBrowser(query itemsQuery) (episodeBrowser, bool) {
	if len(query.specificIDs) > 0 || len(query.itemTypes) < 2 || !itemTypesContain(query.itemTypes, compatEpisodeType) || !query.allowsVideo() {
		return nil, false
	}
	repo, ok := h.episodeRepo.(episodeBrowser)
	return repo, ok
}

// handleMixedEpisodeBrowse serves a recursive /Items browse that names Episode
// beside other types. The result is the media_items browse of the other types
// followed by the episode browse, each in the requested sort, and paged as one
// sequence: TotalRecordCount is their sum and StartIndex walks the other types
// first, then the episodes. Jellyfin interleaves the types by the sort key; the
// concatenation keeps paging stable and complete without merging two sorted
// sources at every offset.
func (h *ItemsHandler) handleMixedEpisodeBrowse(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, repo episodeBrowser) {
	other := query
	other.itemTypes = slices.DeleteFunc(slices.Clone(query.itemTypes), func(t string) bool { return t == compatEpisodeType })
	other.itemType = other.itemTypes[0]
	other.countOnly = false
	other.enableTotalRecordCount = true // the episode offset depends on it
	if query.countOnly {
		other.limit = 1
	}
	result, err := h.content.BrowseItems(r.Context(), session, buildBrowseParams(other))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	listItems := result.Items
	if query.countOnly {
		listItems = nil
	}

	access, filters := h.episodeBrowseFilters(r.Context(), session, query)
	episodeTotal := 0
	var episodeItems []upstreamListItem
	var episodeTargets map[string]compatEpisodeTarget
	// Episodes are access-checked through their series, so a ParentId library
	// is intersected with the session's libraries first, as handleEpisodeBrowse does.
	if query.parentLibraryID <= 0 || narrowAccessToLibrary(&access, query.parentLibraryID) {
		filters.LibraryID = query.parentLibraryID
		if filters.Sort == catalog.BrowseSortTitle && sortsBySortName(newCaseInsensitiveQuery(r.URL.Query()).Get("SortBy")) {
			filters.Sort = ""
		}
		filters.Offset = max(query.startIndex-result.Total, 0)
		filters.Limit = 0 // only count unless the page reaches past the other types
		if !query.countOnly && query.startIndex+len(listItems) >= result.Total {
			filters.Limit = query.limit - len(listItems)
		}
		episodes, total, err := h.browseEpisodes(r.Context(), session, repo, "", "", nil, "", filters, access, true)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		episodeTotal = total
		if len(episodes) > 0 {
			contentIDs := contentIDsFromEpisodes(episodes)
			episodeTargets, err = h.fetchCompatEpisodeTargetsByContentIDsWithDurations(r.Context(), session, contentIDs, nil)
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
			for _, contentID := range contentIDs {
				if target, ok := episodeTargets[contentID]; ok {
					episodeItems = append(episodeItems, target.Item)
				}
			}
		}
	}
	total := result.Total + episodeTotal
	if !query.enableTotalRecordCount {
		total = 0
	}
	if query.countOnly {
		writeJSON(w, http.StatusOK, queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: total, StartIndex: query.startIndex})
		return
	}
	h.writeListItemsPage(w, r, session, query, "", append(slices.Clip(listItems), episodeItems...), episodeTargets, total)
}
