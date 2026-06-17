package sections

import (
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DefaultHomeSections returns the canonical default sections for the home scope.
// These are used for seeding fresh installs and the admin "Restore Defaults" action.
func DefaultHomeSections(libraries []*models.MediaFolder) []*PageSection {
	result := []*PageSection{
		{ID: "default-continue-watching", Scope: "home", Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: ContinueTypeConfig(ContinueTypeWatching), Enabled: true},
	}
	if hasAudiobookLibrary(libraries) {
		result = append(result, &PageSection{
			ID:          "default-continue-listening",
			Scope:       "home",
			Position:    1,
			SectionType: SectionContinueWatching,
			Title:       "Continue Listening",
			ItemLimit:   20,
			Config:      ContinueTypeConfig(ContinueTypeListening),
			Enabled:     true,
		})
	}

	position := len(result)
	for _, library := range libraries {
		if library == nil {
			continue
		}
		for _, section := range generatedHomeLibraryRecentDefaults(library.ID, library.Name, library.Type) {
			section.Position = position
			result = append(result, &PageSection{
				ID:          generatedHomeLibraryRecentID(section, library.ID),
				Scope:       section.Scope,
				Position:    section.Position,
				SectionType: section.SectionType,
				Title:       section.Title,
				ItemLimit:   section.ItemLimit,
				Config:      section.Config,
				Enabled:     section.Enabled,
			})
			position++
		}
	}

	result = append(result,
		&PageSection{ID: "default-hidden-gems", Scope: "home", Position: position, SectionType: SectionHiddenGems, Title: "Hidden Gems", ItemLimit: 15, Config: json.RawMessage(`{"min_rating":7.5}`), Enabled: true},
	)
	position++
	result = append(result,
		&PageSection{ID: "default-trending", Scope: "home", Position: position, SectionType: SectionTrendingOnServer, Title: "Trending on Server", ItemLimit: 15, Config: json.RawMessage(`{"window":"7d"}`), Enabled: true},
	)
	position++
	result = append(result,
		&PageSection{
			ID:          "default-seasonal",
			Scope:       "home",
			Position:    position,
			SectionType: SectionSeasonalThemed,
			Title:       "Seasonal Picks",
			ItemLimit:   15,
			Config: json.RawMessage(
				`{"enabled_themes":["halloween","christmas","valentines","st_patricks","thanksgiving","summer_blockbuster","saturday_morning"]}`,
			),
			Enabled: true,
		},
	)

	return result
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

	sections := []*PageSection{
		{
			Scope:       "home",
			SectionType: SectionRecentlyAdded,
			Title:       GeneratedHomeLibraryRecentTitle(SectionRecentlyAdded, libraryName),
			ItemLimit:   20,
			Config:      addedConfig,
			Enabled:     true,
		},
	}

	switch libraryType {
	case "series":
		sections = append(sections, &PageSection{
			Scope:       "home",
			SectionType: SectionCustomFilter,
			Title:       fmt.Sprintf("Recently Released Episodes in %s", libraryName),
			ItemLimit:   20,
			Config:      GeneratedHomeLibraryRecentEpisodesConfig(libraryID),
			Enabled:     true,
		})
	default:
		sections = append(sections, &PageSection{
			Scope:       "home",
			SectionType: SectionRecentlyReleased,
			Title:       GeneratedHomeLibraryRecentTitle(SectionRecentlyReleased, libraryName),
			ItemLimit:   20,
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
			{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: continueWatchingCfg, Enabled: true},
			{ID: "default-recently-added-movies", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Movies", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-recently-released-movies", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Movies", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-top-rated-movies", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Top Rated Movies", ItemLimit: 20, Config: defaultTopRatedConfig("movie"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-movies", Scope: "library", LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
		}
	case "series":
		return []*PageSection{
			{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: continueWatchingCfg, Enabled: true},
			{ID: "default-recently-added-tv", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added TV", ItemLimit: 20, Config: defaultMediaScopeConfig("series"), Enabled: true},
			{ID: "default-recently-released-episodes", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionCustomFilter, Title: "Recently Released Episodes", ItemLimit: 20, Config: defaultRecentEpisodesConfig(), Enabled: true},
			{ID: "default-top-rated-tv", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Top Rated TV", ItemLimit: 20, Config: defaultTopRatedConfig("series"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-tv", Scope: "library", LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("series"), Enabled: true},
		}
	case "audiobooks", "audiobook":
		// Continue Listening is featured: the library Home tab renders it as
		// the "Now Listening" resume hero instead of a backdrop carousel.
		return []*PageSection{
			{ID: "default-continue-listening", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Listening", Featured: true, ItemLimit: 20, Config: ContinueTypeConfig(ContinueTypeListening), Enabled: true},
			{ID: "default-next-in-series", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionNextInSeries, Title: "Next in Your Series", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-recently-added-audiobooks", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyAdded, Title: "Recently Added Audiobooks", ItemLimit: 20, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
			{ID: "default-recently-released-audiobooks", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionRecentlyReleased, Title: "Recently Released Audiobooks", ItemLimit: 20, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-audiobooks", Scope: "library", LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("audiobook"), Enabled: true},
		}
	case "ebooks", "ebook":
		return []*PageSection{
			{ID: "default-continue-reading", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Reading", ItemLimit: 20, Config: ContinueTypeConfig(ContinueTypeReading), Enabled: true},
			{ID: "default-recently-added-ebooks", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Ebooks", ItemLimit: 20, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
			{ID: "default-recently-released-ebooks", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Ebooks", ItemLimit: 20, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-ebooks", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("ebook"), Enabled: true},
		}
	case "manga":
		// Manga libraries browse the series items (media_items.type='manga');
		// the per-chapter ebook items are scoped out by the "manga" media scope.
		return []*PageSection{
			{ID: "default-continue-reading", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Reading", ItemLimit: 20, Config: ContinueTypeConfig(ContinueTypeReading), Enabled: true},
			{ID: "default-recently-added-manga", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Manga", ItemLimit: 20, Config: defaultMediaScopeConfig("manga"), Enabled: true},
			{ID: "default-recently-released-manga", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Manga", ItemLimit: 20, Config: defaultMediaScopeConfig("manga"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-manga", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("manga"), Enabled: true},
		}
	default:
		return DefaultLibrarySections(libraryID)
	}
}

// DefaultLibrarySections returns the minimal fallback sections for libraries
// with no admin-configured sections. This remains available as a degraded-path
// fallback when typed defaults cannot be resolved safely.
func DefaultLibrarySections(libraryID *int) []*PageSection {
	emptyCfg := json.RawMessage(`{}`)
	return []*PageSection{
		{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: ContinueTypeConfig(ContinueTypeWatching), Enabled: true},
		{ID: "default-recently-added", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: emptyCfg, Enabled: true},
		{ID: "default-recently-released", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released", ItemLimit: 20, Config: emptyCfg, Enabled: true},
	}
}
