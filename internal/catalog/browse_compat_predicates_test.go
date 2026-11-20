package catalog

import (
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
