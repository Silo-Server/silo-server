package jellycompat

import (
	"net/http"
	"strings"
)

// sortKeyCriticRating is Jellyfin's Rotten Tomatoes critic score sort key,
// lowercased, and hiddenCriticRatingSortKey a key Silo does not map, which
// takes its place while that score is hidden.
const (
	sortKeyCriticRating       = "criticrating"
	hiddenCriticRatingSortKey = "HiddenCriticRating"
)

// withCriticRatingSortHidden returns r as it is while the administrator
// shows the Rotten Tomatoes critic score. While the score is hidden it
// returns a copy whose CriticRating sort key is one Silo does not map, so the
// request sorts exactly as it did before Silo knew the key and the order
// doesn't reveal the score (#4160). The key keeps its position, so each
// SortOrder entry still belongs to the same key. MinCriticRating stays in the
// request, where it counts as a filter as it always has; the caller drops the
// parsed minimum instead.
func withCriticRatingSortHidden(r *http.Request, shown bool) *http.Request {
	if shown {
		return r
	}
	values := r.URL.Query()
	changed := false
	for key, entries := range values {
		if !strings.EqualFold(strings.TrimSuffix(key, "[]"), "SortBy") {
			continue
		}
		for i, entry := range entries {
			keys := strings.Split(entry, ",")
			for j, sortBy := range keys {
				if strings.EqualFold(strings.TrimSpace(sortBy), sortKeyCriticRating) {
					keys[j], changed = hiddenCriticRatingSortKey, true
				}
			}
			entries[i] = strings.Join(keys, ",")
		}
	}
	if !changed {
		return r
	}
	u := *r.URL
	u.RawQuery = values.Encode()
	hidden := r.WithContext(r.Context())
	hidden.URL = &u
	return hidden
}
