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
