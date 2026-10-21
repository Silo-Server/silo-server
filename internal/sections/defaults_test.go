package sections

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// sharedHomeDefaultTail is every default home row after the library rows.
var sharedHomeDefaultTail = []string{
	"default-recommended-for-you",
	"default-because-you-watched",
	"default-returning-shows",
	"default-top-rated",
	"default-hidden-gems",
	"default-seasonal",
}

// splitVideoHomeRows are the default home rows below Continue Watching:
// Trending This Week, then the split Recently Added rows.
var splitVideoHomeRows = []string{
	"default-trending-discover",
	"default-recently-added-movies",
	"default-recently-added-tv",
}

func assertHomeSectionIDs(t *testing.T, got []*PageSection, want []string) {
	t.Helper()
	ids := make([]string, len(got))
	for i, section := range got {
		ids[i] = section.ID
		if section.Position != i {
			t.Errorf("section %q position = %d, want %d", section.ID, section.Position, i)
		}
		if section.Scope != "home" || section.LibraryID != nil {
			t.Errorf("section %q scope = %q library = %v, want home with no library", section.ID, section.Scope, section.LibraryID)
		}
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("home section ids = %v, want %v", ids, want)
	}
}

func TestDefaultHomeSectionsSplitRecentlyAddedAcrossVideoLibraries(t *testing.T) {
	libraries := []*models.MediaFolder{
		{ID: 7, Name: "Movies", Type: "movies", SortOrder: 1},
		{ID: 9, Name: "Shows", Type: "series", SortOrder: 2},
		{ID: 11, Name: "Everything", Type: "mixed", SortOrder: 3},
	}

	got := DefaultHomeSections(libraries)
	assertHomeSectionIDs(t, got, append(append([]string{"default-continue-watching"}, splitVideoHomeRows...), sharedHomeDefaultTail...))

	recentMovies, recentTV := got[2], got[3]
	if recentMovies.SectionType != SectionRecentlyAdded || recentMovies.Title != "Recently Added Movies" {
		t.Fatalf("movies row = %s %q, want recently_added \"Recently Added Movies\"", recentMovies.SectionType, recentMovies.Title)
	}
	if recentTV.SectionType != SectionRecentlyAdded || recentTV.Title != "Recently Added TV" {
		t.Fatalf("TV row = %s %q, want recently_added \"Recently Added TV\"", recentTV.SectionType, recentTV.Title)
	}
	if !homeRecentlyAddedCoversItemType(recentMovies, "movie") || homeRecentlyAddedCoversItemType(recentMovies, "series") {
		t.Fatal("movies row does not cover movies only")
	}
	if !homeRecentlyAddedCoversItemType(recentTV, "series") || homeRecentlyAddedCoversItemType(recentTV, "movie") {
		t.Fatal("TV row does not cover series only")
	}
	assertQueryDefinition(t, recentMovies.Config, catalog.QueryDefinition{
		MediaScope: "movie",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, recentTV.Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})

	trending := got[1]
	if trending.SectionType != SectionTrendingDiscover {
		t.Fatalf("trending type = %s, want %s", trending.SectionType, SectionTrendingDiscover)
	}
	if got := CanonicalTrendingConfigKey(trending.Config); got != CanonicalTrendingConfigKey(json.RawMessage(`{"source":"tmdb","window":"week"}`)) {
		t.Fatalf("trending config key = %q, want the TMDB weekly feed", got)
	}
}

func TestDefaultHomeSectionsKeepBookLibraryRows(t *testing.T) {
	libraries := []*models.MediaFolder{
		{ID: 7, Name: "Movies", Type: "movies", SortOrder: 1},
		{ID: 10, Name: "Books", Type: "audiobooks", SortOrder: 2},
	}

	got := DefaultHomeSections(libraries)
	assertHomeSectionIDs(t, got, append(append([]string{
		"default-continue-watching",
		"default-continue-listening",
	}, splitVideoHomeRows...), append([]string{
		"default-home-recently_added-library-10",
		"default-home-recently_released-library-10",
	}, sharedHomeDefaultTail...)...))
	assertContinueType(t, got[0].Config, ContinueTypeWatching)
	assertContinueType(t, got[1].Config, ContinueTypeListening)

	if got[5].Title != "Recently Added in Books" {
		t.Fatalf("book row title = %q, want %q", got[5].Title, "Recently Added in Books")
	}
	if libraryID, ok := ParseGeneratedHomeLibraryRecentConfig(got[5].Config); !ok || libraryID != 10 {
		t.Fatalf("book row generated library = %d ok = %v, want 10 true", libraryID, ok)
	}
}

func TestHomeRecentlyAddedCoversItemType(t *testing.T) {
	seven := 7
	tests := []struct {
		name     string
		section  *PageSection
		itemType string
		want     bool
	}{
		{"video scope covers movie", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("video")}, "movie", true},
		{"video scope covers series", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("video")}, "series", true},
		{"unscoped covers movie", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: json.RawMessage(`{}`)}, "movie", true},
		{"movie scope covers movie", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("movie")}, "movie", true},
		{"movie scope misses series", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("movie")}, "series", false},
		{"series scope covers series", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("series")}, "series", true},
		{"series scope misses movie", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: defaultMediaScopeConfig("series")}, "movie", false},
		{"one library", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: json.RawMessage(`{"filter_library_ids":[7]}`)}, "movie", false},
		{"generated", &PageSection{Scope: "home", SectionType: SectionRecentlyAdded, Config: GeneratedHomeLibraryRecentConfig(7)}, "movie", false},
		{"library page", &PageSection{Scope: "library", LibraryID: &seven, SectionType: SectionRecentlyAdded, Config: json.RawMessage(`{}`)}, "movie", false},
		{"other type", &PageSection{Scope: "home", SectionType: SectionRecentlyReleased, Config: json.RawMessage(`{}`)}, "movie", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := homeRecentlyAddedCoversItemType(tt.section, tt.itemType); got != tt.want {
				t.Fatalf("homeRecentlyAddedCoversItemType(%q) = %v, want %v", tt.itemType, got, tt.want)
			}
		})
	}
}

func TestDefaultLibrarySectionsForTypeManga(t *testing.T) {
	libraryID := 13
	got := DefaultLibrarySectionsForType(&libraryID, "manga")

	if len(got) != 5 {
		t.Fatalf("expected 5 manga default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
	}{
		{index: 0, id: "default-continue-reading", sectionType: SectionContinueWatching, title: "Continue Reading", position: 0},
		{index: 1, id: "default-recently-added-manga", sectionType: SectionRecentlyAdded, title: "Recently Added Manga", position: 1},
		{index: 2, id: "default-recently-released-manga", sectionType: SectionRecentlyReleased, title: "Recently Released Manga", position: 2},
		{index: 3, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 3},
		{index: 4, id: "default-random-manga", sectionType: SectionRandom, title: "Random Picks", position: 4},
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

	// The manga library browses only its series items: every query section is
	// scoped to media_items.type='manga', so the per-chapter ebook items are
	// excluded from the library feed.
	assertContinueType(t, got[0].Config, ContinueTypeReading)
	mangaScope := catalog.QueryDefinition{
		MediaScope: "manga",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	}
	assertQueryDefinition(t, got[1].Config, mangaScope)
	assertQueryDefinition(t, got[2].Config, mangaScope)
	assertEmptyJSON(t, got[3].Config)
	assertQueryDefinition(t, got[4].Config, mangaScope)
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

func assertContinueType(t *testing.T, raw json.RawMessage, want ContinueType) {
	t.Helper()
	got, err := ParseContinueType(raw)
	if err != nil {
		t.Fatalf("ParseContinueType(%s): %v", string(raw), err)
	}
	if got != want {
		t.Fatalf("continue_type = %q, want %q (config %s)", got, want, string(raw))
	}
}

func TestGeneratedHomeLibraryRecentDefaultsMangaScope(t *testing.T) {
	got := generatedHomeLibraryRecentDefaults(7, "Manga", "manga")
	if len(got) != 2 {
		t.Fatalf("expected 2 generated manga home sections, got %d", len(got))
	}

	wantTitles := map[SectionType]string{
		SectionRecentlyAdded:    "Recently Added in Manga",
		SectionRecentlyReleased: "Recently Released in Manga",
	}
	for _, sec := range got {
		wantTitle, ok := wantTitles[sec.SectionType]
		if !ok {
			t.Fatalf("unexpected section type %s", sec.SectionType)
		}
		if sec.Title != wantTitle {
			t.Fatalf("section %s title = %q, want %q", sec.SectionType, sec.Title, wantTitle)
		}

		def, err := ParseQueryDefinition(sec.Config)
		if err != nil {
			t.Fatalf("ParseQueryDefinition(%s) error = %v", sec.SectionType, err)
		}
		if def.MediaScope != "manga" {
			t.Fatalf("section %s media_scope = %q, want manga", sec.SectionType, def.MediaScope)
		}
		if len(def.LibraryIDs) != 1 || def.LibraryIDs[0] != 7 {
			t.Fatalf("section %s library_ids = %v, want [7]", sec.SectionType, def.LibraryIDs)
		}
		if id, ok := ParseGeneratedHomeLibraryRecentConfig(sec.Config); !ok || id != 7 {
			t.Fatalf("section %s generated config id = %d ok = %v, want 7 true", sec.SectionType, id, ok)
		}
	}
}
