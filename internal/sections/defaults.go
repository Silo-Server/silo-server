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
	emptyCfg := json.RawMessage(`{}`)
	result := []*PageSection{
		{ID: "default-continue-watching", Scope: "home", Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: emptyCfg, Enabled: true},
	}

	position := 1
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

func generatedHomeLibraryRecentID(section *PageSection, libraryID int) string {
	kind := generatedHomeLibraryRecentKindForSection(section)
	if kind == generatedHomeLibraryRecentKindReleasedEpisodes {
		return fmt.Sprintf("default-home-%s-library-%d", kind, libraryID)
	}
	return fmt.Sprintf("default-home-%s-library-%d", section.SectionType, libraryID)
}

func generatedHomeLibraryRecentDefaults(libraryID int, libraryName, libraryType string) []*PageSection {
	sections := []*PageSection{
		{
			Scope:       "home",
			SectionType: SectionRecentlyAdded,
			Title:       GeneratedHomeLibraryRecentTitle(SectionRecentlyAdded, libraryName),
			ItemLimit:   20,
			Config:      GeneratedHomeLibraryRecentConfig(libraryID),
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
			Config:      GeneratedHomeLibraryRecentConfig(libraryID),
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

	switch libraryType {
	case "movies", "movie":
		return []*PageSection{
			{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-recently-added-movies", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added Movies", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-recently-released-movies", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released Movies", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
			{ID: "default-top-rated-movies", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Top Rated Movies", ItemLimit: 20, Config: defaultTopRatedConfig("movie"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-movies", Scope: "library", LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("movie"), Enabled: true},
		}
	case "series":
		return []*PageSection{
			{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-recently-added-tv", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added TV", ItemLimit: 20, Config: defaultMediaScopeConfig("series"), Enabled: true},
			{ID: "default-recently-released-episodes", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionCustomFilter, Title: "Recently Released Episodes", ItemLimit: 20, Config: defaultRecentEpisodesConfig(), Enabled: true},
			{ID: "default-top-rated-tv", Scope: "library", LibraryID: libraryID, Position: 3, SectionType: SectionCustomFilter, Title: "Top Rated TV", ItemLimit: 20, Config: defaultTopRatedConfig("series"), Enabled: true},
			{ID: "default-recommended-for-you", Scope: "library", LibraryID: libraryID, Position: 4, SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20, Config: emptyCfg, Enabled: true},
			{ID: "default-random-tv", Scope: "library", LibraryID: libraryID, Position: 5, SectionType: SectionRandom, Title: "Random Picks", ItemLimit: 20, Config: defaultMediaScopeConfig("series"), Enabled: true},
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
		{ID: "default-continue-watching", Scope: "library", LibraryID: libraryID, Position: 0, SectionType: SectionContinueWatching, Title: "Continue Watching", ItemLimit: 20, Config: emptyCfg, Enabled: true},
		{ID: "default-recently-added", Scope: "library", LibraryID: libraryID, Position: 1, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: emptyCfg, Enabled: true},
		{ID: "default-recently-released", Scope: "library", LibraryID: libraryID, Position: 2, SectionType: SectionRecentlyReleased, Title: "Recently Released", ItemLimit: 20, Config: emptyCfg, Enabled: true},
	}
}
