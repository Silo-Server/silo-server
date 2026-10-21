package sections

import (
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// Section scopes and the titles and IDs of default rows, named once for the
// default sets.
const (
	scopeHome    = "home"
	scopeLibrary = "library"

	titleContinueWatching    = "Continue Watching"
	titleContinueListening   = "Continue Listening"
	titleContinueReading     = "Continue Reading"
	titleRecentlyAdded       = "Recently Added"
	titleRecentlyAddedMovies = "Recently Added Movies"
	titleRecentlyAddedTV     = "Recently Added TV"
	titleRecommendedForYou   = "Recommended for You"
	titleReturningShows      = "Returning Shows"
	titleTrendingThisWeek    = "Trending This Week"
	titleTopRated            = "Top Rated"
	titleHiddenGems          = "Hidden Gems"
	titleRandomPicks         = "Random Picks"

	idContinueWatching    = "default-continue-watching"
	idContinueListening   = "default-continue-listening"
	idContinueReading     = "default-continue-reading"
	idRecentlyAdded       = "default-recently-added"
	idRecentlyAddedMovies = "default-recently-added-movies"
	idRecentlyAddedTV     = "default-recently-added-tv"
	idRecommendedForYou   = "default-recommended-for-you"
	idReturningShows      = "default-returning-shows"
	idTrendingDiscover    = "default-trending-discover"
)

// DefaultItemLimit is the row size of default rows and of rows an admin
// creates without a limit.
const DefaultItemLimit = 100

// betaLibraryItemLimit is the row size of default rows for beta library
// types (audiobooks, ebooks, manga, podcasts). Those surfaces stay as they
// were until the Books effort replaces them, so they keep their old size.
const betaLibraryItemLimit = 20

// libraryRowItemLimit is the default row size for rows that belong to one
// library type: DefaultItemLimit for video libraries, the beta size otherwise.
func libraryRowItemLimit(libraryType string) int {
	if IsVideoLibraryType(libraryType) {
		return DefaultItemLimit
	}
	return betaLibraryItemLimit
}

// DefaultHomeSections returns the canonical default sections for the home scope.
// These are used for seeding fresh installs and the admin "Restore Defaults" action.
//
// Movies and TV get separate Recently Added rows, as on the library pages;
// only book-type libraries add rows of their own. Trending This Week sits
// directly below the continue rows. Personalized rows (Recommended, Because
// You Watched, Returning Shows) and TMDB trending stay empty, and clients hide
// them, until a profile has history or the library has a match. Recently
// Added and Top Rated need neither, so a new profile on a new server still
// has rows to browse.
func DefaultHomeSections(libraries []*models.MediaFolder) []*PageSection {
	result := []*PageSection{
		{ID: idContinueWatching, Scope: scopeHome, SectionType: SectionContinueWatching, Title: titleContinueWatching, ItemLimit: DefaultItemLimit, Config: ContinueTypeConfig(ContinueTypeWatching), Enabled: true},
	}
	if hasAudiobookLibrary(libraries) {
		result = append(result, &PageSection{ID: idContinueListening, Scope: scopeHome, SectionType: SectionContinueWatching, Title: titleContinueListening, ItemLimit: betaLibraryItemLimit, Config: ContinueTypeConfig(ContinueTypeListening), Enabled: true})
	}
	result = append(result,
		&PageSection{ID: idTrendingDiscover, Scope: scopeHome, SectionType: SectionTrendingDiscover, Title: titleTrendingThisWeek, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{"source":"tmdb","window":"week"}`), Enabled: true},
		&PageSection{ID: idRecentlyAddedMovies, Scope: scopeHome, SectionType: SectionRecentlyAdded, Title: titleRecentlyAddedMovies, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("movie"), Enabled: true},
		&PageSection{ID: idRecentlyAddedTV, Scope: scopeHome, SectionType: SectionRecentlyAdded, Title: titleRecentlyAddedTV, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("series"), Enabled: true},
	)

	for _, library := range libraries {
		if library == nil || IsVideoLibraryType(library.Type) {
			continue // video libraries are covered by the split Recently Added rows
		}
		for _, section := range generatedHomeLibraryRecentDefaults(library.ID, library.Name, library.Type) {
			section.ID = generatedHomeLibraryRecentID(section, library.ID)
			result = append(result, section)
		}
	}

	result = append(result,
		&PageSection{ID: idRecommendedForYou, Scope: scopeHome, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{}`), Enabled: true},
		&PageSection{ID: "default-because-you-watched", Scope: scopeHome, SectionType: SectionBecauseYouWatched, Title: defaultBecauseWatchedTitle, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{}`), Enabled: true},
		&PageSection{ID: idReturningShows, Scope: scopeHome, SectionType: SectionReturningShows, Title: titleReturningShows, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{}`), Enabled: true},
		&PageSection{ID: "default-top-rated", Scope: scopeHome, SectionType: SectionCriticallyAcclaimed, Title: titleTopRated, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{}`), Enabled: true},
		&PageSection{ID: "default-hidden-gems", Scope: scopeHome, SectionType: SectionHiddenGems, Title: titleHiddenGems, ItemLimit: DefaultItemLimit, Config: json.RawMessage(`{"min_rating":7.5}`), Enabled: true},
		&PageSection{
			ID:          "default-seasonal",
			Scope:       scopeHome,
			SectionType: SectionSeasonalThemed,
			Title:       "Seasonal Picks",
			ItemLimit:   DefaultItemLimit,
			Config:      json.RawMessage(recipes.DefaultSeasonalConfig),
			Enabled:     true,
		},
	)

	for i, section := range result {
		section.Position = i
	}
	return result
}

// IsVideoLibraryType reports whether a library holds movies or TV, the
// libraries the split home Recently Added rows cover.
func IsVideoLibraryType(libraryType string) bool {
	return librarykind.IsMovie(libraryType) || librarykind.IsTV(libraryType) || librarykind.IsMixed(libraryType)
}

func hasAudiobookLibrary(libraries []*models.MediaFolder) bool {
	for _, library := range libraries {
		if library != nil && IsAudiobookLibraryType(library.Type) {
			return true
		}
	}
	return false
}

func generatedHomeLibraryRecentID(section *PageSection, libraryID int) string {
	kind := generatedHomeLibraryRecentKindForSection(section)
	if kind == generatedHomeLibraryRecentKindReleasedEpisodes {
		return fmt.Sprintf("default-home-%s-library-%d", kind, libraryID)
	}
	return fmt.Sprintf("default-home-%s-library-%d", section.SectionType, libraryID)
}

func generatedHomeLibraryRecentDefaults(libraryID int, libraryName, libraryType string) []*PageSection {
	// addedConfig/releasedConfig default to the library-scoped (no media_scope)
	// generated config. A manga library mixes type='manga' series with
	// type='ebook' chapters, so we scope its generated home rows to the series
	// only — otherwise the chapter junk filenames leak into the home page.
	addedConfig := GeneratedHomeLibraryRecentConfig(libraryID)
	releasedConfig := GeneratedHomeLibraryRecentConfig(libraryID)
	if libraryType == "manga" {
		addedConfig = GeneratedHomeLibraryRecentConfigScoped(libraryID, "manga")
		releasedConfig = GeneratedHomeLibraryRecentConfigScoped(libraryID, "manga")
	}

	itemLimit := libraryRowItemLimit(libraryType)
	sections := []*PageSection{
		{
			Scope:       scopeHome,
			SectionType: SectionRecentlyAdded,
			Title:       GeneratedHomeLibraryRecentTitle(SectionRecentlyAdded, libraryName),
			ItemLimit:   itemLimit,
			Config:      addedConfig,
			Enabled:     true,
		},
	}

	switch libraryType {
	case "series":
		sections = append(sections, &PageSection{
			Scope:       scopeHome,
			SectionType: SectionCustomFilter,
			Title:       fmt.Sprintf("Recently Released Episodes in %s", libraryName),
			ItemLimit:   itemLimit,
			Config:      GeneratedHomeLibraryRecentEpisodesConfig(libraryID),
			Enabled:     true,
		})
	default:
		sections = append(sections, &PageSection{
			Scope:       scopeHome,
			SectionType: SectionRecentlyReleased,
			Title:       GeneratedHomeLibraryRecentTitle(SectionRecentlyReleased, libraryName),
			ItemLimit:   itemLimit,
			Config:      releasedConfig,
			Enabled:     true,
		})
	}

	return sections
}

func defaultQueryConfig(def catalog.QueryDefinition) json.RawMessage {
	config, err := json.Marshal(def.Normalize())
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return config
}

func defaultMediaScopeConfig(mediaScope string) json.RawMessage {
	return defaultQueryConfig(catalog.QueryDefinition{
		MediaScope: mediaScope,
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "added_at", Order: "desc"},
	})
}

func defaultTopRatedConfig(mediaScope string) json.RawMessage {
	return defaultQueryConfig(catalog.QueryDefinition{
		MediaScope: mediaScope,
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "rating_imdb", Order: "desc"},
	})
}

func defaultRecentEpisodesConfig() json.RawMessage {
	return defaultQueryConfig(catalog.QueryDefinition{
		MediaScope: "episode",
		Match:      "all",
		Groups:     []catalog.QueryGroup{},
		Sort:       catalog.QuerySort{Field: "release_date", Order: "desc"},
	})
}

// DefaultLibrarySectionsForType returns the canonical default sections for a
// library type. These are used when seeding new libraries and restoring
// library defaults.
func DefaultLibrarySectionsForType(libraryID *int, libraryType string) []*PageSection {
	emptyCfg := json.RawMessage(`{}`)
	continueWatchingCfg := ContinueTypeConfig(ContinueTypeWatching)

	switch libraryType {
	case "movies", "movie":
		return []*PageSection{
			{ID: idContinueWatching, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueWatching, ItemLimit: DefaultItemLimit, Config: continueWatchingCfg, Enabled: true},
			{ID: idRecentlyAddedMovies, Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: titleRecentlyAddedMovies, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-recently-released-movies", Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Movies", ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-top-rated-movies", Scope: scopeLibrary, LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Top Rated Movies", ItemLimit: DefaultItemLimit, Config: defaultTopRatedConfig("movie"), Enabled: true},
			{ID: idRecommendedForYou, Scope: scopeLibrary, LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: DefaultItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-random-movies", Scope: scopeLibrary, LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: titleRandomPicks, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("movie"), Enabled: true},
		}
	case "series":
		return []*PageSection{
			{ID: idContinueWatching, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueWatching, ItemLimit: DefaultItemLimit, Config: continueWatchingCfg, Enabled: true},
			{ID: idRecentlyAddedTV, Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: titleRecentlyAddedTV, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("series"), Enabled: true},
			{ID: idReturningShows, Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionReturningShows, Title: titleReturningShows, ItemLimit: DefaultItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-recently-released-episodes", Scope: scopeLibrary, LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Recently Released Episodes", ItemLimit: DefaultItemLimit, Config: defaultRecentEpisodesConfig(), Enabled: true},
			{ID: "default-top-rated-tv", Scope: scopeLibrary, LibraryID: libraryID, Position: 4, SectionType: SectionCustomFilter, Title: "Top Rated TV", ItemLimit: DefaultItemLimit, Config: defaultTopRatedConfig("series"), Enabled: true},
			{ID: idRecommendedForYou, Scope: scopeLibrary, LibraryID: libraryID, Position: 5, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: DefaultItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-random-tv", Scope: scopeLibrary, LibraryID: libraryID, Position: 6, SectionType: SectionRandom, Title: titleRandomPicks, ItemLimit: DefaultItemLimit, Config: defaultMediaScopeConfig("series"), Enabled: true},
		}
	case "audiobooks", "audiobook":
		// Continue Listening is featured: the library Home tab renders it as
		// the "Now Listening" resume hero instead of a backdrop carousel.
		return []*PageSection{
			{ID: idContinueListening, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueListening, Featured: true, ItemLimit: betaLibraryItemLimit, Config: ContinueTypeConfig(ContinueTypeListening), Enabled: true},
			{ID: "default-next-in-series", Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionNextInSeries, Title: "Next in Your Series", ItemLimit: betaLibraryItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-recently-added-audiobooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyAdded, Title: "Recently Added Audiobooks", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
			{ID: "default-recently-released-audiobooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 3, SectionType: SectionRecentlyReleased, Title: "Recently Released Audiobooks", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
			{ID: idRecommendedForYou, Scope: scopeLibrary, LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: betaLibraryItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-random-audiobooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: titleRandomPicks, ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
		}
	case "ebooks", "ebook":
		return []*PageSection{
			{ID: idContinueReading, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueReading, ItemLimit: betaLibraryItemLimit, Config: ContinueTypeConfig(ContinueTypeReading), Enabled: true},
			{ID: "default-recently-added-ebooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Ebooks", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
			{ID: "default-recently-released-ebooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Ebooks", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
			{ID: idRecommendedForYou, Scope: scopeLibrary, LibraryID: libraryID, Position: 3, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: betaLibraryItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-random-ebooks", Scope: scopeLibrary, LibraryID: libraryID, Position: 4, SectionType: SectionRandom, Title: titleRandomPicks, ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
		}
	case "manga":
		// Manga libraries browse the series items (media_items.type='manga');
		// the per-chapter ebook items are scoped out by the "manga" media scope.
		return []*PageSection{
			{ID: idContinueReading, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueReading, ItemLimit: betaLibraryItemLimit, Config: ContinueTypeConfig(ContinueTypeReading), Enabled: true},
			{ID: "default-recently-added-manga", Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Manga", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("manga"), Enabled: true},
			{ID: "default-recently-released-manga", Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Manga", ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("manga"), Enabled: true},
			{ID: idRecommendedForYou, Scope: scopeLibrary, LibraryID: libraryID, Position: 3, SectionType: SectionRecommendedForYou, Title: titleRecommendedForYou, ItemLimit: betaLibraryItemLimit, Config: emptyCfg, Enabled: true},
			{ID: "default-random-manga", Scope: scopeLibrary, LibraryID: libraryID, Position: 4, SectionType: SectionRandom, Title: titleRandomPicks, ItemLimit: betaLibraryItemLimit, Config: defaultMediaScopeConfig("manga"), Enabled: true},
		}
	default:
		return defaultLibrarySections(libraryID, libraryRowItemLimit(libraryType))
	}
}

// DefaultLibrarySections returns the minimal fallback sections for libraries
// with no admin-configured sections. This remains available as a degraded-path
// fallback when typed defaults cannot be resolved safely.
func DefaultLibrarySections(libraryID *int) []*PageSection {
	return defaultLibrarySections(libraryID, DefaultItemLimit)
}

func defaultLibrarySections(libraryID *int, itemLimit int) []*PageSection {
	emptyCfg := json.RawMessage(`{}`)
	return []*PageSection{
		{ID: idContinueWatching, Scope: scopeLibrary, LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: titleContinueWatching, ItemLimit: itemLimit, Config: ContinueTypeConfig(ContinueTypeWatching), Enabled: true},
		{ID: idRecentlyAdded, Scope: scopeLibrary, LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: titleRecentlyAdded, ItemLimit: itemLimit, Config: emptyCfg, Enabled: true},
		{ID: "default-recently-released", Scope: scopeLibrary, LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released", ItemLimit: itemLimit, Config: emptyCfg, Enabled: true},
	}
}
