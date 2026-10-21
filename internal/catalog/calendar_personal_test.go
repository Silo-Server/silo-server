package catalog

import (
	"strings"
	"testing"
)

func TestFavoriteAndWatchlistQueries_ScopeToProfile(t *testing.T) {
	if !strings.Contains(favoriteItemIDsQuery, "FROM user_favorites") ||
		!strings.Contains(favoriteItemIDsQuery, "profile_id = $2") {
		t.Fatalf("favoriteItemIDsQuery wrong:\n%s", favoriteItemIDsQuery)
	}
	if !strings.Contains(watchlistItemIDsQuery, "FROM user_watchlist") ||
		!strings.Contains(watchlistItemIDsQuery, "profile_id = $2") {
		t.Fatalf("watchlistItemIDsQuery wrong:\n%s", watchlistItemIDsQuery)
	}
}
