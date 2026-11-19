package catalog

import (
	"strings"
	"testing"
)

func TestBuildBrowseFavoritesSQL_DisabledLibrariesNotExists(t *testing.T) {
	plan, err := buildBrowseFavoritesPlan(BrowseFavoritesFilters{
		UserID:             1,
		ProfileID:          "p1",
		DisabledLibraryIDs: []int{99},
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("buildBrowseFavoritesPlan: %v", err)
	}
	sql, _ := plan.pagedSQL()
	if !strings.Contains(sql, "NOT EXISTS (") {
		t.Fatalf("expected NOT EXISTS for disabled libraries; got %s", sql)
	}
}
