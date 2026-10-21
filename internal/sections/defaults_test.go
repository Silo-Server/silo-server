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

func TestDefaultHomeSectionsWithoutLibraries(t *testing.T) {
	got := DefaultHomeSections(nil)
	assertHomeSectionIDs(t, got, append(append([]string{"default-continue-watching"}, splitVideoHomeRows...), sharedHomeDefaultTail...))
	assertContinueType(t, got[0].Config, ContinueTypeWatching)
	for _, section := range got {
		if section.ItemLimit != DefaultItemLimit {
			t.Errorf("section %q limit = %d, want %d", section.ID, section.ItemLimit, DefaultItemLimit)
		}
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

// Beta library types (audiobooks, ebooks, manga, podcasts) keep their old row
// size; only video rows and the shared home rows use DefaultItemLimit.
func TestDefaultRowLimitsKeepBetaLibrariesAtOldSize(t *testing.T) {
	assertLimits := func(t *testing.T, label string, got []*PageSection, want int) {
		t.Helper()
		if len(got) == 0 {
			t.Fatalf("%s: no sections", label)
		}
		for _, section := range got {
			if section.ItemLimit != want {
				t.Errorf("%s: section %q limit = %d, want %d", label, section.ID, section.ItemLimit, want)
			}
		}
	}

	home := DefaultHomeSections([]*models.MediaFolder{
		{ID: 7, Name: "Movies", Type: "movies"},
		{ID: 10, Name: "Books", Type: "audiobooks"},
	})
	for _, section := range home {
		want := DefaultItemLimit
		if section.ID == "default-continue-listening" {
			want = betaLibraryItemLimit
		}
		if libraryID, ok := ParseGeneratedHomeLibraryRecentConfig(section.Config); ok && libraryID == 10 {
			want = betaLibraryItemLimit
		}
		if section.ItemLimit != want {
			t.Errorf("home section %q limit = %d, want %d", section.ID, section.ItemLimit, want)
		}
	}

	libraryID := 3
	for _, libraryType := range []string{"audiobooks", "ebooks", "manga", "podcasts"} {
		assertLimits(t, libraryType+" library page", DefaultLibrarySectionsForType(&libraryID, libraryType), betaLibraryItemLimit)
		assertLimits(t, libraryType+" generated home rows", generatedHomeLibraryRecentDefaults(libraryID, "Lib", libraryType), betaLibraryItemLimit)
	}
	for _, libraryType := range []string{"movies", "series", "mixed"} {
		assertLimits(t, libraryType+" library page", DefaultLibrarySectionsForType(&libraryID, libraryType), DefaultItemLimit)
		assertLimits(t, libraryType+" generated home rows", generatedHomeLibraryRecentDefaults(libraryID, "Lib", libraryType), DefaultItemLimit)
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
	assertContinueType(t, got[0].Config, ContinueTypeWatching)

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

	if len(got) != 7 {
		t.Fatalf("expected 7 series default sections, got %d", len(got))
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
		{index: 2, id: "default-returning-shows", sectionType: SectionReturningShows, title: "Returning Shows", position: 2},
		{index: 3, id: "default-recently-released-episodes", sectionType: SectionCustomFilter, title: "Recently Released Episodes", position: 3},
		{index: 4, id: "default-top-rated-tv", sectionType: SectionCustomFilter, title: "Top Rated TV", position: 4},
		{index: 5, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 5},
		{index: 6, id: "default-random-tv", sectionType: SectionRandom, title: "Random Picks", position: 6},
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
	assertContinueType(t, got[0].Config, ContinueTypeWatching)

	assertQueryDefinition(t, got[1].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertEmptyJSON(t, got[2].Config)
	assertQueryDefinition(t, got[3].Config, catalog.QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "release_date", Order: "desc"},
	})
	assertQueryDefinition(t, got[4].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "rating_imdb", Order: "desc"},
	})
	assertEmptyJSON(t, got[5].Config)
	assertQueryDefinition(t, got[6].Config, catalog.QueryDefinition{
		MediaScope: "series",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func TestDefaultLibrarySectionsForTypeAudiobooks(t *testing.T) {
	libraryID := 10
	got := DefaultLibrarySectionsForType(&libraryID, "audiobooks")

	if len(got) != 6 {
		t.Fatalf("expected 6 audiobook default sections, got %d", len(got))
	}

	tests := []struct {
		index       int
		id          string
		sectionType SectionType
		title       string
		position    int
		featured    bool
	}{
		{index: 0, id: "default-continue-listening", sectionType: SectionContinueWatching, title: "Continue Listening", position: 0, featured: true},
		{index: 1, id: "default-next-in-series", sectionType: SectionNextInSeries, title: "Next in Your Series", position: 1},
		{index: 2, id: "default-recently-added-audiobooks", sectionType: SectionRecentlyAdded, title: "Recently Added Audiobooks", position: 2},
		{index: 3, id: "default-recently-released-audiobooks", sectionType: SectionRecentlyReleased, title: "Recently Released Audiobooks", position: 3},
		{index: 4, id: "default-recommended-for-you", sectionType: SectionRecommendedForYou, title: "Recommended for You", position: 4},
		{index: 5, id: "default-random-audiobooks", sectionType: SectionRandom, title: "Random Picks", position: 5},
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
		if section.Featured != tt.featured {
			t.Fatalf("section %d featured = %v, want %v", tt.index, section.Featured, tt.featured)
		}
	}
	assertContinueType(t, got[0].Config, ContinueTypeListening)

	assertEmptyJSON(t, got[1].Config)
	assertQueryDefinition(t, got[2].Config, catalog.QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertQueryDefinition(t, got[3].Config, catalog.QueryDefinition{
		MediaScope: "audiobook",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
	assertEmptyJSON(t, got[4].Config)
	assertQueryDefinition(t, got[5].Config, catalog.QueryDefinition{
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

	assertContinueType(t, got[0].Config, ContinueTypeReading)
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
	assertContinueType(t, got[0].Config, ContinueTypeWatching)
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

func TestHomeDefaultsIncludeRecipeRichSet(t *testing.T) {
	defs := DefaultHomeSections(nil)
	want := []SectionType{
		SectionContinueWatching,
		SectionRecentlyAdded,
		SectionRecommendedForYou,
		SectionBecauseYouWatched,
		SectionReturningShows,
		SectionTrendingDiscover,
		SectionCriticallyAcclaimed,
		SectionHiddenGems,
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

func TestGeneratedHomeLibraryRecentDefaultsNonMangaNoScope(t *testing.T) {
	got := generatedHomeLibraryRecentDefaults(7, "Movies", "movies")
	if len(got) != 2 {
		t.Fatalf("expected 2 generated movies home sections, got %d", len(got))
	}
	for _, sec := range got {
		def, err := ParseQueryDefinition(sec.Config)
		if err != nil {
			t.Fatalf("ParseQueryDefinition(%s) error = %v", sec.SectionType, err)
		}
		if def.MediaScope == "manga" {
			t.Fatalf("section %s unexpectedly carries manga media_scope", sec.SectionType)
		}
	}
}
