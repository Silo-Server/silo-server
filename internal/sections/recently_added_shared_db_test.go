package sections

import (
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestHasEnabledHomeRecentlyAddedCovering(t *testing.T) {
	row := func(scope string, enabled bool) *PageSection {
		return &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig(scope), Enabled: enabled}
	}
	shared := func(enabled bool) *PageSection { return row(catalog.MediaScopeVideo, enabled) }
	movieOnly := row("movie", true)
	seriesOnly := row("series", true)
	tests := []struct {
		name        string
		home        []*PageSection
		libraryType string
		want        bool
	}{
		{"enabled shared row covers movies", []*PageSection{movieOnly, shared(true)}, "movies", true},
		{"enabled shared row covers mixed", []*PageSection{shared(true)}, "mixed", true},
		{"disabled shared row covers nothing", []*PageSection{shared(false)}, "series", false},
		{"movie row covers movies", []*PageSection{movieOnly}, "movies", true},
		{"movie row misses series", []*PageSection{movieOnly}, "series", false},
		{"split rows cover mixed", []*PageSection{movieOnly, seriesOnly}, "mixed", true},
		{"movie row alone misses mixed", []*PageSection{movieOnly}, "mixed", false},
		{"disabled split row misses mixed", []*PageSection{movieOnly, row("series", false)}, "mixed", false},
		{"no rows", nil, "movies", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasEnabledHomeRecentlyAddedCovering(tt.home, tt.libraryType); got != tt.want {
				t.Fatalf("hasEnabledHomeRecentlyAddedCovering(%q) = %v, want %v", tt.libraryType, got, tt.want)
			}
		})
	}
}

func TestMergeRecentlyAddedInterleavesAndReportsLeftover(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tvCard := func(id string, minutes int) *models.MediaItem {
		added := base.Add(time.Duration(minutes) * time.Minute)
		return &models.MediaItem{ContentID: id, AddedAt: &added}
	}
	otherCard := func(id string, minutes int) *models.MediaItem {
		return &models.MediaItem{ContentID: id, CreatedAt: base.Add(time.Duration(minutes) * time.Minute)}
	}
	ids := func(items []*models.MediaItem) []string {
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = item.ContentID
		}
		return out
	}
	tv := []*models.MediaItem{tvCard("show-a", 5), tvCard("shared", 3), tvCard("show-b", 1)}
	other := []*models.MediaItem{otherCard("movie-a", 4), otherCard("shared", 2)}

	merged, leftover := mergeRecentlyAdded(tv, other, 10)
	if want := []string{"show-a", "movie-a", "shared", "show-b"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("merged = %v, want %v", ids(merged), want)
	}
	if leftover {
		t.Fatal("a merge that consumed both lists reported leftover cards")
	}

	merged, leftover = mergeRecentlyAdded(tv, other, 2)
	if want := []string{"show-a", "movie-a"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("limited merge = %v, want %v", ids(merged), want)
	}
	if !leftover {
		t.Fatal("a merge cut by the limit reported no leftover cards")
	}

	// A series in both a TV library and a mixed library fills both sides with
	// the same cards. Each side read one past a page of 2; with only those
	// cards, nothing exists beyond the page.
	tv = []*models.MediaItem{tvCard("show-a", 5), tvCard("show-b", 3)}
	other = []*models.MediaItem{otherCard("show-b", 4), otherCard("show-a", 2)}
	merged, leftover = mergeRecentlyAdded(tv, other, 2)
	if want := []string{"show-a", "show-b"}; !slices.Equal(ids(merged), want) {
		t.Fatalf("duplicate merge = %v, want %v", ids(merged), want)
	}
	if leftover {
		t.Fatal("cards left only as duplicates of merged cards reported leftover")
	}
}

func TestScopeTVLibraries(t *testing.T) {
	tests := []struct {
		name  string
		tv    []int
		scope []int
		want  []int
	}{
		{"every library", []int{3, 5}, nil, []int{3, 5}},
		{"narrowed by caller", []int{3, 5, 7}, []int{5, 9}, []int{5}},
		{"disjoint", []int{3}, []int{5}, []int{}},
		{"caller sees nothing", []int{3}, []int{}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeTVLibraries(tt.tv, tt.scope); !slices.Equal(got, tt.want) {
				t.Fatalf("scopeTVLibraries = %v, want %v", got, tt.want)
			}
		})
	}
}
