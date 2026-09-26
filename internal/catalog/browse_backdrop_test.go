package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestBuildBrowsePlan_RequireBackdrop asserts the ImageTypes=Backdrop filter
// (BrowseFilters.RequireBackdrop) renders the backdrop-presence predicate into
// the WHERE clause, and is absent otherwise. Guards against a future refactor
// of buildBrowsePlan silently dropping the condition.
func TestBuildBrowsePlan_RequireBackdrop(t *testing.T) {
	const predicate = "NULLIF(BTRIM(mi.backdrop_path), '') IS NOT NULL"
	repo := &BrowseRepository{}

	plan, earlyEmpty, err := repo.buildBrowsePlan(BrowseFilters{Type: "movie", RequireBackdrop: true})
	if err != nil || earlyEmpty {
		t.Fatalf("buildBrowsePlan(RequireBackdrop) err=%v earlyEmpty=%v", err, earlyEmpty)
	}
	if !strings.Contains(plan.whereClause, predicate) {
		t.Fatalf("RequireBackdrop=true: whereClause missing predicate.\ngot: %s", plan.whereClause)
	}

	plan, _, err = repo.buildBrowsePlan(BrowseFilters{Type: "movie"})
	if err != nil {
		t.Fatalf("buildBrowsePlan err=%v", err)
	}
	if strings.Contains(plan.whereClause, predicate) {
		t.Fatalf("RequireBackdrop unset: predicate should be absent.\ngot: %s", plan.whereClause)
	}
}

func TestCatalogCollectionFiltersReachBrowse(t *testing.T) {
	req := CatalogRequest{
		Source:          CatalogSourceUserCollection,
		CollectionID:    "collection-1",
		PersonID:        42,
		RequireBackdrop: true,
		BrowseOverlay: &BrowseFilters{
			Genres:            []string{"Drama", "Mystery"},
			Years:             []int{2025, 2026},
			ExcludeContentIDs: []string{"hidden"},
			AudioLanguages:    []string{"sv"},
		},
		Query: QueryDefinition{MediaScope: "episode"},
	}
	if !catalogRequestHasOverlay(req) {
		t.Fatal("person/backdrop collection filters were not recognized as an overlay")
	}

	base := catalogBaseCollectionRequest(req)
	filters, earlyEmpty, err := catalogBrowseFilters(base, AccessFilter{UserID: 7, ProfileID: "profile-1", MaxPlaybackQuality: "1080p"})
	if err != nil || earlyEmpty {
		t.Fatalf("catalogBrowseFilters err=%v earlyEmpty=%v", err, earlyEmpty)
	}
	if filters.PersonID != 42 || !filters.RequireBackdrop || filters.Type != "episode" ||
		!slices.Equal(filters.Genres, []string{"Drama", "Mystery"}) ||
		!slices.Equal(filters.Years, []int{2025, 2026}) ||
		!slices.Equal(filters.ExcludeContentIDs, []string{"hidden"}) ||
		!slices.Equal(filters.AudioLanguages, []string{"sv"}) || filters.UserID != 7 ||
		filters.ProfileID != "profile-1" || filters.MaxPlaybackQuality != "1080p" {
		t.Fatalf("browse filters did not preserve the compat overlay: %+v", filters)
	}
}

func TestResolveExactOrderedMediaItems_BackdropBeforePagination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		require bool
		offset  int
		wantID  string
		total   int
		more    bool
	}{
		{"unfiltered", false, 0, "missing", 4, true},
		{"first filtered page", true, 0, "first", 2, true},
		{"second filtered page", true, 1, "second", 2, false},
		{"past filtered end", true, 2, "", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []*models.MediaItem{
				{ContentID: "missing", Type: "episode"},
				{ContentID: "first", Type: "episode", BackdropPath: "first.jpg"},
				{ContentID: "blank", Type: "episode", BackdropPath: "   "},
				{ContentID: "second", Type: "episode", BackdropPath: "second.jpg"},
			}
			result, err := (&CatalogResolver{}).resolveExactOrderedMediaItems(context.Background(), items, CatalogRequest{
				Source: CatalogSourceUserCollection, UseSourceOrder: true,
				RequireBackdrop: tc.require, Limit: 1, Offset: tc.offset,
			}, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != tc.total || result.HasMore != tc.more || !result.TotalExact {
				t.Fatalf("incorrect filtered pagination: %+v", result)
			}
			if tc.wantID == "" {
				if len(result.Items) != 0 {
					t.Fatalf("expected empty page, got %+v", result.Items)
				}
			} else if len(result.Items) != 1 || result.Items[0].ContentID != tc.wantID {
				t.Fatalf("expected %s, got %+v", tc.wantID, result.Items)
			}
		})
	}
}
