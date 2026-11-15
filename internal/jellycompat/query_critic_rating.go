package jellycompat

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// requestSortKey is sortKey for one request: CriticRating maps only while
// criticShown, and is an unknown key otherwise.
func requestSortKey(raw string, criticShown bool) (string, bool) {
	mapped, ok := sortKey(raw)
	if ok && mapped == catalog.BrowseSortRatingRTCritic && !criticShown {
		return "", false
	}
	return mapped, ok
}

// applyShownCriticRating adds the CriticRating sort and MinCriticRating
// filter that parseItemsQuery leaves out, for a request whose critic score
// criticShown reports shown. Items carry CriticRating under the same rule, so
// neither the order nor the filter reveals a hidden score. criticShown is
// called only when the request uses the score.
func (q *itemsQuery) applyShownCriticRating(r *http.Request, criticShown func() bool) {
	raw := newCaseInsensitiveQuery(r.URL.Query())
	sortBy := raw.Get("SortBy")
	// CriticRating changes the sort only as the first key Silo maps; any
	// other key keeps what parseItemsQuery derived from it.
	sortsByCritic := selectedSortKey(sortBy, true) == sortKeyCriticRating
	minimum, err := strconv.ParseFloat(strings.TrimSpace(raw.Get("MinCriticRating")), 64)
	hasMinimum := err == nil && minimum > 0
	if (!sortsByCritic && !hasMinimum) || !criticShown() {
		return
	}
	if sortsByCritic {
		q.sort, q.order = parseSort(sortBy, raw.Get("SortOrder"), true)
		q.sortByKey = sortKeyCriticRating
	}
	if hasMinimum {
		q.minCriticRating = minimum
	}
}
