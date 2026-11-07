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

func TestParseItemsQuerySort(t *testing.T) {
	tests := []struct {
		path, wantSort, wantOrder string
	}{
		// Jellyfin sorts an explicit SortBy ascending when SortOrder is absent.
		{"/Items?SortBy=SortName", "sort_title", "asc"},
		{"/Items?sortBy=PremiereDate&SortOrder=Descending", "release_date", "desc"},
		// Episode-order keys keep the natural season/episode order.
		{"/Shows/x/Episodes?sortBy=IndexNumber", "", "asc"},
		{"/Items?SortBy=ParentIndexNumber,IndexNumber&SortOrder=Descending", "", "desc"},
		// The first mapped key wins and takes the SortOrder at its position.
		{"/Items?SortBy=IsFolder,SortName&SortOrder=Descending,Ascending", "sort_title", "asc"},
		{"/Items?SortBy=SeriesSortName,DateCreated&SortOrder=Ascending,Descending", "created_at", "desc"},
		{"/Items?SortBy=PremiereDate,SortName&SortOrder=Descending,Ascending", "release_date", "desc"},
		// Unmapped keys alone keep the created_at fallback.
		{"/Items?SortBy=Runtime", "created_at", "asc"},
		// Without SortBy, Silo keeps its newest-first rail default.
		{"/Items", "created_at", "desc"},
	}
	for _, tc := range tests {
		query := parseItemsQuery(httptest.NewRequest("GET", tc.path, nil), NewResourceIDCodec())
		if query.sort != tc.wantSort || query.order != tc.wantOrder {
			t.Errorf("%s: sort=%q order=%q, want %q %q", tc.path, query.sort, query.order, tc.wantSort, tc.wantOrder)
		}
	}
}

// A Filters=IsPlayed request sorted by a catalog field goes to the catalog
// browse when the browse can return every matching type; DatePlayed, no
// SortBy, keys Silo does not map, keys with no catalog order, and lists that
// would lose their episodes keep the played list (#2978).
func TestPlayedListSortRouting(t *testing.T) {
	withEpisodes := &ItemsHandler{episodeRepo: &boundedEpisodeContractRepo{}}
	withoutEpisodes := &ItemsHandler{}
	for _, tc := range []struct {
		params      string
		wantCatalog bool
		wantKey     string
	}{
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=SortName&SortOrder=Descending", true, "sortname"},
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=DateCreated", true, "datecreated"},
		{"IncludeItemTypes=Episode&Filters=IsPlayed&SortBy=SortName", true, "sortname"},
		{"IncludeItemTypes=Movie,Episode&Filters=IsPlayed&SortBy=SortName", false, "sortname"},
		{"Filters=IsPlayed&SortBy=SortName", false, "sortname"},
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=IndexNumber", false, "indexnumber"},
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=DatePlayed&SortOrder=Descending", false, "dateplayed"},
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=PlayCount,DatePlayed", false, "dateplayed"},
		{"IncludeItemTypes=Movie&Filters=IsPlayed", false, ""},
		{"IncludeItemTypes=Movie&Filters=IsPlayed&SortBy=PlayCount", false, ""},
		{"IncludeItemTypes=Movie&IsPlayed=false&SortBy=SortName", false, "sortname"},
		{"IncludeItemTypes=Movie&SortBy=SortName", false, "sortname"},
	} {
		query := parseItemsQuery(httptest.NewRequest("GET", "/Items?Recursive=true&"+tc.params, nil), NewResourceIDCodec())
		if got := withEpisodes.browsesPlayedListByCatalog(query); got != tc.wantCatalog || query.sortByKey != tc.wantKey {
			t.Errorf("%s: catalog sort = %v, key %q; want %v, %q", tc.params, got, query.sortByKey, tc.wantCatalog, tc.wantKey)
		}
		// Without an episode browse, an Episode-only list keeps the played list.
		wantWithout := tc.wantCatalog && !query.episodeOnly()
		if got := withoutEpisodes.browsesPlayedListByCatalog(query); got != wantWithout {
			t.Errorf("%s without an episode browse: catalog sort = %v, want %v", tc.params, got, wantWithout)
		}
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
