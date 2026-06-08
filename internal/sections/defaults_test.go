package sections

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDefaultHomeSectionsWithoutLibraries(t *testing.T) {
	sections := DefaultHomeSections(nil)
	// 1 continue-watching + 3 recipe-rich defaults (hidden_gems, trending, seasonal-auto-cycle)
	if len(sections) != 4 {
		t.Fatalf("expected 4 default home sections, got %d", len(sections))
	}
	if sections[0].SectionType != SectionContinueWatching {
		t.Fatalf("expected continue watching, got %s", sections[0].SectionType)
	}
}

func TestDefaultHomeSectionsWithLibraries(t *testing.T) {
	libraries := []*models.MediaFolder{
		{ID: 7, Name: "Movies", Type: "movies", SortOrder: 1},
		{ID: 9, Name: "Shows", Type: "series", SortOrder: 2},
	}

	got := DefaultHomeSections(libraries)
	// 1 continue-watching + 2 libraries × 2 types + 3 recipe-rich defaults
	if len(got) != 8 {
		t.Fatalf("expected 8 default home sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
		libraryID   int
	}{
		{index: 0, id: "default-continue-watching", sectionType: SectionContinueWatching, title: "Continue Watching", position: 0},
		{index: 1, id: "default-home-recently_added-library-7", sectionType: SectionRecentlyAdded, title: "Recently Added in Movies", position: 1, libraryID: 7},
		{index: 2, id: "default-home-recently_released-library-7", sectionType: SectionRecentlyReleased, title: "Recently Released in Movies", position: 2, libraryID: 7},
		{index: 3, id: "default-home-recently_added-library-9", sectionType: SectionRecentlyAdded, title: "Recently Added in Shows", position: 3, libraryID: 9},
		{index: 4, id: "default-home-recently_released_episodes-library-9", sectionType: SectionCustomFilter, title: "Recently Released Episodes in Shows", position: 4, libraryID: 9},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
		if tt.libraryID == 0 {
			continue
		}
		libraryID, ok := ParseGeneratedHomeLibraryRecentConfig(section.Config)
		if !ok {
			t.Fatalf("section %d expected generated home config", tt.index)
		}
		if libraryID != tt.libraryID {
			t.Fatalf("section %d config library id = %d, want %d", tt.index, libraryID, tt.libraryID)
		}
	}

	assertQueryDefinition(t, got[4].Config, catalog.QueryDefinition{
		LibraryIDs: []int{9},
		MediaScope: "episode",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "release_date", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeMovies(t *testing.T) {
	libraryID := 42
	got := DefaultLibrarySectionsForType(&libraryID, "movies")

	if len(got) != 6 {
		t.Fatalf("expected 6 movie default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-watching", sectionType: SectionContinueWatching, title: "Continue Watching", position: 0},
		{index: 1, id: "default-recently-added-movies", sectionType: SectionRecentlyAdded, title: "Recently Added Movies", position: 1},
		{index: 2, id: "default-recently-released-movies", sectionType: SectionRecentlyReleased, title: "Recently Released Movies", position: 2},
		{index: 3, id: "default-top-rated-movies", sectionType: SectionCustomFilter, title: "Top Rated Movies", position: 3},
		{index: 4, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 4},
		{index: 5, id: "default-random-movies", sectionType: SectionRandom, title: "Random Picks", position: 5},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
		if section.Featured {
			t.Fatalf("section %d featured = true, want false", tt.index)
		}
	}

	assertQueryDefinition(t, got[1].Config, catalog.QueryDefinition{
		MediaScope: "movie",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, got[3].Config, catalog.QueryDefinition{
		MediaScope: "movie",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "rating_imdb", Order: "desc"},
	})
	assertEmptyJSON(t, got[4].Config)
	assertQueryDefinition(t, got[5].Config, catalog.QueryDefinition{
		MediaScope: "movie",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeSeries(t *testing.T) {
	libraryID := 17
	got := DefaultLibrarySectionsForType(&libraryID, "series")

	if len(got) != 6 {
		t.Fatalf("expected 6 series default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-watching", sectionType: SectionContinueWatching, title: "Continue Watching", position: 0},
		{index: 1, id: "default-recently-added-tv", sectionType: SectionRecentlyAdded, title: "Recently Added TV", position: 1},
		{index: 2, id: "default-recently-released-episodes", sectionType: SectionCustomFilter, title: "Recently Released Episodes", position: 2},
		{index: 3, id: "default-top-rated-tv", sectionType: SectionCustomFilter, title: "Top Rated TV", position: 3},
		{index: 4, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 4},
		{index: 5, id: "default-random-tv", sectionType: SectionRandom, title: "Random Picks", position: 5},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
		if section.Featured {
			t.Fatalf("section %d featured = true, want false", tt.index)
		}
	}

	assertQueryDefinition(t, got[1].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, got[2].Config, catalog.QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "release_date", Order: "desc"},
	})
	assertQueryDefinition(t, got[3].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "rating_imdb", Order: "desc"},
	})
	assertEmptyJSON(t, got[4].Config)
	assertQueryDefinition(t, got[5].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeAudiobooks(t *testing.T) {
	libraryID := 10
	got := DefaultLibrarySectionsForType(&libraryID, "audiobooks")

	if len(got) != 5 {
		t.Fatalf("expected 5 audiobook default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-listening", sectionType: SectionContinueWatching, title: "Continue Listening", position: 0},
		{index: 1, id: "default-recently-added-audiobooks", sectionType: SectionRecentlyAdded, title: "Recently Added Audiobooks", position: 1},
		{index: 2, id: "default-recently-released-audiobooks", sectionType: SectionRecentlyReleased, title: "Recently Released Audiobooks", position: 2},
		{index: 3, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 3},
		{index: 4, id: "default-random-audiobooks", sectionType: SectionRandom, title: "Random Picks", position: 4},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
		if section.Featured {
			t.Fatalf("section %d featured = true, want false", tt.index)
		}
	}

	assertQueryDefinition(t, got[1].Config, catalog.QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, got[2].Config, catalog.QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertEmptyJSON(t, got[3].Config)
	assertQueryDefinition(t, got[4].Config, catalog.QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeEbooks(t *testing.T) {
	libraryID := 11
	got := DefaultLibrarySectionsForType(&libraryID, "ebooks")

	if len(got) != 5 {
		t.Fatalf("expected 5 ebook default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-reading", sectionType: SectionContinueWatching, title: "Continue Reading", position: 0},
		{index: 1, id: "default-recently-added-ebooks", sectionType: SectionRecentlyAdded, title: "Recently Added Ebooks", position: 1},
		{index: 2, id: "default-recently-released-ebooks", sectionType: SectionRecentlyReleased, title: "Recently Released Ebooks", position: 2},
		{index: 3, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 3},
		{index: 4, id: "default-random-ebooks", sectionType: SectionRandom, title: "Random Picks", position: 4},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
		if section.Featured {
			t.Fatalf("section %d featured = true, want false", tt.index)
		}
	}

	assertEmptyJSON(t, got[0].Config)
	assertQueryDefinition(t, got[1].Config, catalog.QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, got[2].Config, catalog.QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertEmptyJSON(t, got[3].Config)
	assertQueryDefinition(t, got[4].Config, catalog.QueryDefinition{
		MediaScope: "ebook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeMixed(t *testing.T) {
	libraryID := 99
	got := DefaultLibrarySectionsForType(&libraryID, "mixed")

	if len(got) != 3 {
		t.Fatalf("expected 3 mixed default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-watching", sectionType: SectionContinueWatching, title: "Continue Watching", position: 0},
		{index: 1, id: "default-recently-added", sectionType: SectionRecentlyAdded, title: "Recently Added", position: 1},
		{index: 2, id: "default-recently-released", sectionType: SectionRecentlyReleased, title: "Recently Released", position: 2},
	}

	for _, tt := range tests {
		section := got[tt.index]
		if section.ID != tt.id {
			t.Fatalf("section %d id = %q, want %q", tt.index, section.ID, tt.id)
		}
		if section.SectionType != tt.sectionType {
			t.Fatalf("section %d type = %q, want %q", tt.index, section.SectionType, tt.sectionType)
		}
		if section.Title != tt.title {
			t.Fatalf("section %d title = %q, want %q", tt.index, section.Title, tt.title)
		}
		if section.Position != tt.position {
			t.Fatalf("section %d position = %d, want %d", tt.index, section.Position, tt.position)
		}
	}
}

func assertQueryDefinition(t *testing.T, raw json.RawMessage, want catalog.QueryDefinition) {
	t.Helper()

	got, err := ParseQueryDefinition(raw)
	if err != nil {
		t.Fatalf("ParseQueryDefinition() error = %v", err)
	}

	got = got.Normalize()
	want = want.Normalize()

	if got.MediaScope != want.MediaScope {
		t.Fatalf("media_scope = %q, want %q", got.MediaScope, want.MediaScope)
	}
	if got.Match != want.Match {
		t.Fatalf("match = %q, want %q", got.Match, want.Match)
	}
	if got.Sort != want.Sort {
		t.Fatalf("sort = %+v, want %+v", got.Sort, want.Sort)
	}
	if len(got.Groups) != len(want.Groups) {
		t.Fatalf("groups len = %d, want %d", len(got.Groups), len(want.Groups))
	}
	for i := range want.Groups {
		if got.Groups[i].Match != want.Groups[i].Match {
			t.Fatalf("groups[%d].match = %q, want %q", i, got.Groups[i].Match, want.Groups[i].Match)
		}
		if len(got.Groups[i].Rules) != len(want.Groups[i].Rules) {
			t.Fatalf("groups[%d].rules len = %d, want %d", i, len(got.Groups[i].Rules), len(want.Groups[i].Rules))
		}
		for j := range want.Groups[i].Rules {
			if got.Groups[i].Rules[j] != want.Groups[i].Rules[j] {
				t.Fatalf("groups[%d].rules[%d] = %+v, want %+v", i, j, got.Groups[i].Rules[j], want.Groups[i].Rules[j])
			}
		}
	}
}

func assertEmptyJSON(t *testing.T, raw json.RawMessage) {
	t.Helper()
	if string(raw) != "{}" {
		t.Fatalf("config = %s, want {}", string(raw))
	}
}

func TestHomeDefaultsIncludeRecipeRichSet(t *testing.T) {
	defs := DefaultHomeSections(nil)
	want := []SectionType{
		SectionContinueWatching,
		SectionHiddenGems,
		SectionTrendingOnServer,
		SectionSeasonalThemed,
	}
	for _, w := range want {
		found := false
		for _, d := range defs {
			if d.SectionType == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default home sections missing %s", w)
		}
	}
}
