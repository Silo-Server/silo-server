package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func baseItem() *models.MediaItem {
	return &models.MediaItem{
		ContentID:         "c1",
		Title:             "Base Title",
		SortTitle:         "Base Title, The",
		Overview:          "Base overview.",
		Tagline:           "Base tagline.",
		PosterPath:        "posters/base.jpg",
		PosterThumbhash:   "hash-poster",
		BackdropPath:      "backdrops/base.jpg",
		BackdropThumbhash: "hash-backdrop",
		LogoPath:          "logos/base.png",
	}
}

func TestApplyItemLocalizationKeepsManuallySelectedArtwork(t *testing.T) {
	item := baseItem()
	item.LockedFields = []int{fieldImagesLocked}
	loc := &models.MediaItemLocalization{
		Title:              "Titre",
		PosterPath:         "posters/fr.jpg",
		BackdropPath:       "backdrops/fr.jpg",
		LogoPath:           "logos/fr.png",
		PosterSourcePath:   "poster-provider",
		BackdropSourcePath: "backdrop-provider",
		LogoSourcePath:     "logo-provider",
	}
	got := applyItemLocalization(item, loc)
	if got.Title != "Titre" {
		t.Errorf("localized title lost: %q", got.Title)
	}
	if got.PosterPath != item.PosterPath || got.BackdropPath != item.BackdropPath || got.LogoPath != item.LogoPath {
		t.Errorf("localized artwork replaced a manual selection: %+v", got)
	}
	if got.PosterSourcePath != item.PosterSourcePath || got.BackdropSourcePath != item.BackdropSourcePath || got.LogoSourcePath != item.LogoSourcePath {
		t.Errorf("localized artwork sources replaced manual sources: %+v", got)
	}
}

func TestApplySeasonLocalizationKeepsManuallySelectedPoster(t *testing.T) {
	season := &models.Season{ContentID: "s1", Title: "Season 1", PosterPath: "manual.jpg", PosterSourcePath: "manual-source", PosterThumbhash: "manual-hash"}
	loc := &models.SeasonLocalization{Title: "Saison 1", PosterPath: "provider.jpg", PosterSourcePath: "provider-source", PosterThumbhash: "provider-hash"}
	got := applySeasonLocalization(season, loc, true)
	if got.Title != "Saison 1" {
		t.Errorf("localized title lost: %q", got.Title)
	}
	if got.PosterPath != season.PosterPath || got.PosterSourcePath != season.PosterSourcePath || got.PosterThumbhash != season.PosterThumbhash {
		t.Errorf("localized poster replaced a manual selection: %+v", got)
	}
}

// A locked title or overview is what every viewer sees: a provider's localized
// text doesn't cover it, but a translation of the edit by hand or by AI does.
func TestSeasonEpisodeLocalizationRespectsLocks(t *testing.T) {
	locked := []int{fieldNameLocked, fieldOverviewLocked}
	for _, tc := range []struct {
		name         string
		locks        []int
		source       string
		wantTitle    string
		wantOverview string
	}{
		{"unlocked takes the localization", nil, models.LocalizationSourceProvider, "Titre", "Résumé"},
		{"locked ignores provider text", locked, models.LocalizationSourceProvider, "Edited", "Edited overview"},
		{"locked keeps a manual translation", locked, models.LocalizationSourceManual, "Edited", "Résumé"},
		{"locked keeps an AI translation", locked, models.LocalizationSourceAI, "Edited", "Résumé"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			episode := applyEpisodeLocalization(
				&models.Episode{Title: "Edited", Overview: "Edited overview", LockedFields: tc.locks},
				&models.EpisodeLocalization{Title: "Titre", Overview: "Résumé", OverviewSource: tc.source},
			)
			if episode.Title != tc.wantTitle || episode.Overview != tc.wantOverview {
				t.Errorf("episode = %q, %q, want %q, %q", episode.Title, episode.Overview, tc.wantTitle, tc.wantOverview)
			}
			season := applySeasonLocalization(
				&models.Season{Title: "Edited", Overview: "Edited overview", LockedFields: tc.locks},
				&models.SeasonLocalization{Title: "Titre", Overview: "Résumé", OverviewSource: tc.source}, false,
			)
			if season.Title != tc.wantTitle || season.Overview != tc.wantOverview {
				t.Errorf("season = %q, %q, want %q, %q", season.Title, season.Overview, tc.wantTitle, tc.wantOverview)
			}
		})
	}
}
