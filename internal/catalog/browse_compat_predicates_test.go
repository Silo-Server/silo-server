package catalog

import (
	"strings"
	"testing"
)

func TestBrowseCombinedPredicatesPreserveProfileAndAccess(t *testing.T) {
	filters := BrowseFilters{Type: "movie", Genres: []string{"Drama", "Comedy"}, Years: []int{2020, 2024}, SearchTerm: "100%", UserID: 7, ProfileID: "child", IsFavorite: true, IsPlayed: new(false), IsResumable: true, LibraryIDs: []int{3}, DisabledLibraryIDs: []int{9}, MaxContentRating: "PG", Limit: 1, Offset: 2}
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(filters)
	if err != nil || empty {
		t.Fatalf("build plan: empty=%v err=%v", empty, err)
	}
	sql, args := plan.pagedSQL(false)
	for _, predicate := range []string{"mi.genres &&", "mi.year = ANY", "mi.title ILIKE", "user_favorites", "user_watch_progress", "profile_id", "NOT EXISTS", "content_rating", "media_folder_id", "LIMIT", "OFFSET"} {
		if !strings.Contains(sql, predicate) {
			t.Errorf("missing %q in %s", predicate, sql)
		}
	}
	hasProfile, hasLiteral := false, false
	for _, arg := range args {
		if str, ok := arg.(string); ok {
			hasProfile = hasProfile || str == "child"
			hasLiteral = hasLiteral || str == `%100\%%`
		}
	}
	if !hasProfile || !hasLiteral {
		t.Fatalf("missing bound profile or literal search pattern: %v", args)
	}
}

func TestBrowseUserStateWithoutProfileFailsClosed(t *testing.T) {
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{Type: "movie", UserID: 7, IsFavorite: true})
	if err != nil || empty {
		t.Fatalf("build: %v %v", empty, err)
	}
	sql, _ := plan.pagedSQL(false)
	if !strings.Contains(sql, "FALSE") {
		t.Fatalf("unscoped state query: %s", sql)
	}
}

func TestBrowsePlayedSeriesUsesEpisodeCompletionAndHistory(t *testing.T) {
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(BrowseFilters{Type: "series", UserID: 7, ProfileID: "child", IsPlayed: new(false)})
	if err != nil || empty {
		t.Fatalf("build: %v %v", empty, err)
	}
	sql, _ := plan.pagedSQL(false)
	for _, predicate := range []string{"episodes.series_id = mi.content_id", "user_watch_history", "user_history_hidden_items", "episode_libraries"} {
		if !strings.Contains(sql, predicate) {
			t.Fatalf("played series omits %s: %s", predicate, sql)
		}
	}
}
