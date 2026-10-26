package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

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

func TestBrowseJellyfinGridFiltersBindEveryParameter(t *testing.T) {
	filters := BrowseFilters{
		Type:                    "movie",
		SearchTerm:              "100%",
		NameLessThan:            "M",
		NameStartsWithOrGreater: "C",
		ExcludeContentIDs:       []string{"movie-9"},
		Studios:                 []string{"A24"},
		OfficialRatings:         []string{"PG-13", "R"},
		MinCommunityRating:      7.5,
		MinPremiereDate:         "2020-01-02",
		MaxPremiereDate:         "2024-12-31",
		Limit:                   1,
	}
	plan, empty, err := (&BrowseRepository{}).buildBrowsePlan(filters)
	if err != nil || empty {
		t.Fatalf("build plan: empty=%v err=%v", empty, err)
	}
	sql, args := plan.pagedSQL(false)
	if !slices.Contains(args, any(`%100\%%`)) {
		t.Fatalf("literal search wildcard was not escaped: %v", args)
	}
	for _, predicate := range []string{
		sortTitleKeyExpr + " < LOWER(", sortTitleKeyExpr + " >= LOWER(",
		"NOT (mi.content_id = ANY(", "mi.studios &&", "mi.content_rating = ANY(", "mi.rating_imdb >=",
		premiereDateKeyExpr + " >=", premiereDateKeyExpr + " <=",
	} {
		if !strings.Contains(sql, predicate) {
			t.Errorf("missing %q in %s", predicate, sql)
		}
	}
	for i := range args {
		if !regexp.MustCompile(fmt.Sprintf(`\$%d\b`, i+1)).MatchString(sql) {
			t.Fatalf("parameter $%d has no SQL reference: %s", i+1, sql)
		}
	}
}
