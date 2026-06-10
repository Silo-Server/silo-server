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

// An AI-created localization row carries only overview/tagline; everything
// else must fall back to the base item rather than be blanked.
func TestApplyItemLocalizationPartialRowFallsBackToBase(t *testing.T) {
	loc := &models.MediaItemLocalization{
		ContentID: "c1",
		Language:  "fr",
		Overview:  "Résumé traduit.",
		Tagline:   "Slogan traduit.",
	}
	got := applyItemLocalization(baseItem(), loc)

	if got.Overview != "Résumé traduit." || got.Tagline != "Slogan traduit." {
		t.Errorf("translated fields not applied: %q / %q", got.Overview, got.Tagline)
	}
	if got.Title != "Base Title" || got.SortTitle != "Base Title, The" {
		t.Errorf("empty localized titles blanked the base: %q / %q", got.Title, got.SortTitle)
	}
	if got.PosterPath != "posters/base.jpg" || got.PosterThumbhash != "hash-poster" {
		t.Errorf("empty localized poster blanked the base: %q / %q", got.PosterPath, got.PosterThumbhash)
	}
	if got.BackdropPath != "backdrops/base.jpg" || got.LogoPath != "logos/base.png" {
		t.Errorf("empty localized artwork blanked the base: %q / %q", got.BackdropPath, got.LogoPath)
	}
}

func TestApplyItemLocalizationFullRowOverridesEverything(t *testing.T) {
	loc := &models.MediaItemLocalization{
		ContentID:         "c1",
		Language:          "fr",
		Title:             "Titre",
		SortTitle:         "Titre, Le",
		Overview:          "Résumé.",
		Tagline:           "Slogan.",
		PosterPath:        "posters/fr.jpg",
		PosterThumbhash:   "hash-fr",
		BackdropPath:      "backdrops/fr.jpg",
		BackdropThumbhash: "hash-fr-bd",
		LogoPath:          "logos/fr.png",
	}
	got := applyItemLocalization(baseItem(), loc)
	if got.Title != "Titre" || got.Overview != "Résumé." || got.PosterPath != "posters/fr.jpg" ||
		got.PosterThumbhash != "hash-fr" || got.LogoPath != "logos/fr.png" {
		t.Errorf("full localization not applied: %+v", got)
	}
}

func TestApplyItemLocalizationDoesNotMutateBase(t *testing.T) {
	item := baseItem()
	_ = applyItemLocalization(item, &models.MediaItemLocalization{Title: "Titre"})
	if item.Title != "Base Title" {
		t.Errorf("base item mutated: %q", item.Title)
	}
}

func TestApplyItemLocalizationNilLocalizationClones(t *testing.T) {
	item := baseItem()
	got := applyItemLocalization(item, nil)
	if got == item {
		t.Fatal("expected a clone, got the same pointer")
	}
	if got.Title != item.Title {
		t.Errorf("clone differs: %q", got.Title)
	}
}

func TestApplySeasonLocalizationPartialRow(t *testing.T) {
	season := &models.Season{ContentID: "s1", Title: "Season 1", Overview: "Base.", PosterPath: "p.jpg", PosterThumbhash: "h"}
	got := applySeasonLocalization(season, &models.SeasonLocalization{Overview: "Saison résumé."})
	if got.Overview != "Saison résumé." {
		t.Errorf("overview not applied: %q", got.Overview)
	}
	if got.Title != "Season 1" || got.PosterPath != "p.jpg" || got.PosterThumbhash != "h" {
		t.Errorf("empty fields blanked the base: %+v", got)
	}
}

func TestApplyEpisodeLocalizationPartialRow(t *testing.T) {
	ep := &models.Episode{ContentID: "e1", Title: "Pilot", Overview: "Base."}
	got := applyEpisodeLocalization(ep, &models.EpisodeLocalization{Overview: "Épisode résumé."})
	if got.Overview != "Épisode résumé." {
		t.Errorf("overview not applied: %q", got.Overview)
	}
	if got.Title != "Pilot" {
		t.Errorf("empty localized title blanked the base: %q", got.Title)
	}
}
