package catalog

import (
	"strings"
	"testing"
)

func TestFollowedItemIDsQuery_UnionsAllSignals(t *testing.T) {
	for _, fragment := range []string{
		"FROM user_favorites",
		"FROM user_watchlist",
		"user_watch_progress wp",
		"LEFT JOIN episodes e ON e.content_id = wp.media_item_id",
		"COALESCE(e.series_id, wp.media_item_id)",
		"UNION",
	} {
		if !strings.Contains(followedItemIDsQuery, fragment) {
			t.Fatalf("followedItemIDsQuery missing %q:\n%s", fragment, followedItemIDsQuery)
		}
	}
}

func TestWatchedItemIDsQuery_FiltersCompletedWithinSet(t *testing.T) {
	for _, fragment := range []string{
		"FROM   user_watch_progress",
		"completed = true",
		"media_item_id = ANY($3)",
	} {
		if !strings.Contains(watchedItemIDsQuery, fragment) {
			t.Fatalf("watchedItemIDsQuery missing %q:\n%s", fragment, watchedItemIDsQuery)
		}
	}
}

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
