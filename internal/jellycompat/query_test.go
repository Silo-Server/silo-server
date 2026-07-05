package jellycompat

import (
	"net/http/httptest"
	"testing"
)

func TestFavoriteItemsNeedBrowseFilters(t *testing.T) {
	if favoriteItemsNeedBrowseFilters(itemsQuery{}) {
		t.Fatal("plain favorite query should keep the lightweight favorites path")
	}

	query := itemsQuery{parentLibraryID: 42}
	if !favoriteItemsNeedBrowseFilters(query) {
		t.Fatal("favorite query with a parent library should use catalog browse filters")
	}
}

func TestParseItemsQueryAcceptsIsFavoriteParam(t *testing.T) {
	req := httptest.NewRequest("GET", "/Users/user/Items?isFavorite=true&IncludeItemTypes=Series", nil)

	query := parseItemsQuery(req, NewResourceIDCodec())

	if !query.isFavorite {
		t.Fatal("expected isFavorite=true to enable favorite filtering")
	}
	if len(query.itemTypes) != 1 || query.itemTypes[0] != "series" {
		t.Fatalf("got item types %v, want [series]", query.itemTypes)
	}
}

func TestBuildBrowseParamsPropagatesEnableTotalRecordCount(t *testing.T) {
	req := httptest.NewRequest("GET", "/Items?EnableTotalRecordCount=false", nil)

	query := parseItemsQuery(req, NewResourceIDCodec())
	params := buildBrowseParams(query)

	if got := params.Get("include_total"); got != "false" {
		t.Fatalf("include_total = %q, want false", got)
	}
}

func TestParseItemsQueryAppliesExcludeItemTypesToDefaultVideoScope(t *testing.T) {
	req := httptest.NewRequest("GET", "/Items?SearchTerm=sponge+bob"+
		"&ExcludeItemTypes=Movie&ExcludeItemTypes=Episode&ExcludeItemTypes=TvChannel", nil)

	query := parseItemsQuery(req, NewResourceIDCodec())

	if !query.hasItemTypeFilter {
		t.Fatal("expected ExcludeItemTypes to count as an item type filter")
	}
	if len(query.itemTypes) != 1 || query.itemTypes[0] != "series" {
		t.Fatalf("itemTypes = %v, want [series]", query.itemTypes)
	}
}

func TestParseItemsQuerySubtractsExcludeItemTypesFromIncludeItemTypes(t *testing.T) {
	req := httptest.NewRequest("GET", "/Items?IncludeItemTypes=Movie,Series&ExcludeItemTypes=Movie", nil)

	query := parseItemsQuery(req, NewResourceIDCodec())

	if len(query.itemTypes) != 1 || query.itemTypes[0] != "series" {
		t.Fatalf("itemTypes = %v, want [series]", query.itemTypes)
	}
}

func TestMapSortByReleaseDate(t *testing.T) {
	tests := []string{
		"PremiereDate",
		"PremiereDate,SortName,ProductionYear",
		"Premiered",
	}
	for _, raw := range tests {
		if got := mapSortBy(raw); got != "release_date" {
			t.Fatalf("mapSortBy(%q) = %q, want release_date", raw, got)
		}
	}
}

func TestMapSortByDateLastContentAdded(t *testing.T) {
	// Jellyfin's standard "Latest" sort for TV libraries orders shows by
	// their most recently added episode. It must map to the
	// latest_episode_added sort (issue #202), not series creation date.
	for _, raw := range []string{"DateLastContentAdded", "DateLastContentAdded,SortName"} {
		if got := mapSortBy(raw); got != "latest_episode_added" {
			t.Fatalf("mapSortBy(%q) = %q, want latest_episode_added", raw, got)
		}
	}
	// DatePlayed used to piggyback on the same case; it must keep its old
	// created_at behavior rather than inherit the episode-added sort.
	if got := mapSortBy("DatePlayed"); got != "created_at" {
		t.Fatalf("mapSortBy(DatePlayed) = %q, want created_at", got)
	}
}

func TestParseItemsQueryDateLastContentAddedSortScope(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "series only",
			path: "/Items?IncludeItemTypes=Series&SortBy=DateLastContentAdded",
			want: "latest_episode_added",
		},
		{
			name: "movie",
			path: "/Items?IncludeItemTypes=Movie&SortBy=DateLastContentAdded",
			want: "created_at",
		},
		{
			name: "no type",
			path: "/Items?SortBy=DateLastContentAdded",
			want: "created_at",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)

			query := parseItemsQuery(req, NewResourceIDCodec())

			if query.sort != tc.want {
				t.Fatalf("sort = %q, want %q", query.sort, tc.want)
			}
		})
	}
}

func TestParseContentIDParam(t *testing.T) {
	got := parseContentIDParam(" movie-1, movie-2, movie-1 ,, ")
	want := []string{"movie-1", "movie-2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
