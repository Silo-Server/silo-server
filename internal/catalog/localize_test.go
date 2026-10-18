package catalog

import (
	"slices"
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

// Base artwork served from a cached copy of a local sidecar is
// language-neutral and outranks provider art, so a viewer in another language
// keeps it. A localized slot still fills base art that is remote, including a
// slot whose local copy is not cached yet and still serves earlier remote art.
func TestApplyItemLocalizationKeepsLocalSidecarArtwork(t *testing.T) {
	item := baseItem()
	item.PosterPath = "local/movies/c1/0badf00d/poster/original.webp"
	item.PosterSourcePath = "file:///media/Film/Poster.JPG?v=12-1"
	item.BackdropPath = "tmdb/movies/1/backdrop/original.webp"
	item.BackdropSourcePath = "file:///media/Film/fanart.jpg?v=12-1"
	item.LogoSourcePath = "tmdb://logo.png"
	loc := &models.MediaItemLocalization{
		Title:              "Titre",
		PosterPath:         "posters/fr.jpg",
		PosterSourcePath:   "tmdb://fr-poster.jpg",
		PosterThumbhash:    "hash-fr",
		BackdropPath:       "backdrops/fr.jpg",
		BackdropSourcePath: "tmdb://fr-backdrop.jpg",
		LogoPath:           "logos/fr.png",
		LogoSourcePath:     "tmdb://fr-logo.png",
	}
	got := applyItemLocalization(item, loc)
	if got.Title != "Titre" {
		t.Errorf("localized title lost: %q", got.Title)
	}
	if got.PosterPath != item.PosterPath || got.PosterSourcePath != item.PosterSourcePath || got.PosterThumbhash != item.PosterThumbhash {
		t.Errorf("localized poster replaced local art: path %q source %q", got.PosterPath, got.PosterSourcePath)
	}
	if got.BackdropPath != "backdrops/fr.jpg" {
		t.Errorf("a slot still serving remote art should take the localized backdrop, got %q", got.BackdropPath)
	}
	if got.LogoPath != "logos/fr.png" {
		t.Errorf("remote base logo should take the localized logo, got %q", got.LogoPath)
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
	got := applySeasonLocalization(season, &models.SeasonLocalization{Overview: "Saison résumé."}, false)
	if got.Overview != "Saison résumé." {
		t.Errorf("overview not applied: %q", got.Overview)
	}
	if got.Title != "Season 1" || got.PosterPath != "p.jpg" || got.PosterThumbhash != "h" {
		t.Errorf("empty fields blanked the base: %+v", got)
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

// Only fields an AI job wrote are marked; provider and manual text is not.
func TestApplyLocalizationMarksMachineTranslatedFields(t *testing.T) {
	item := applyItemLocalization(baseItem(), &models.MediaItemLocalization{
		Overview: "Résumé IA.", OverviewSource: "ai",
		Tagline: "Accroche fournisseur.", TaglineSource: "provider",
	})
	if !slices.Equal(item.MachineTranslatedFields, []string{MachineTranslatedOverview}) {
		t.Errorf("item markers = %v, want [overview]", item.MachineTranslatedFields)
	}
	// An empty AI field falls back to the base text, which is not translated.
	item = applyItemLocalization(baseItem(), &models.MediaItemLocalization{TaglineSource: "ai", OverviewSource: "manual", Overview: "Saisi."})
	if len(item.MachineTranslatedFields) != 0 {
		t.Errorf("item markers = %v, want none", item.MachineTranslatedFields)
	}

	season := applySeasonLocalization(&models.Season{Overview: "Base."}, &models.SeasonLocalization{Overview: "IA.", OverviewSource: "ai"}, false)
	if !slices.Equal(season.MachineTranslatedFields, []string{MachineTranslatedOverview}) {
		t.Errorf("season markers = %v, want [overview]", season.MachineTranslatedFields)
	}

	// Re-localizing an already localized model reports only the new row.
	episode := applyEpisodeLocalization(&models.Episode{Overview: "Base."}, &models.EpisodeLocalization{Overview: "IA.", OverviewSource: "ai"})
	episode = applyEpisodeLocalization(episode, &models.EpisodeLocalization{Overview: "Anbieter.", OverviewSource: "provider"})
	if len(episode.MachineTranslatedFields) != 0 {
		t.Errorf("episode markers = %v, want none", episode.MachineTranslatedFields)
	}
}

func TestApplySeasonLocalizationKeepsLocalSidecarPoster(t *testing.T) {
	season := &models.Season{
		ContentID:        "s1",
		Title:            "Season 1",
		PosterPath:       "local/series/c1/0badf00d/poster/original.webp",
		PosterSourcePath: "file:///media/Show/season01-poster.jpg?v=12-1",
		PosterThumbhash:  "local-hash",
	}
	loc := &models.SeasonLocalization{Title: "Saison 1", PosterPath: "provider.jpg", PosterSourcePath: "tvdb://fr.jpg", PosterThumbhash: "provider-hash"}
	got := applySeasonLocalization(season, loc, false)
	if got.Title != "Saison 1" {
		t.Errorf("localized title lost: %q", got.Title)
	}
	if got.PosterPath != season.PosterPath || got.PosterSourcePath != season.PosterSourcePath || got.PosterThumbhash != season.PosterThumbhash {
		t.Errorf("localized poster replaced local art: %+v", got)
	}
}

// A cached copy of an image URL the item's NFO names is library artwork too:
// the library chose it, so a viewer in another language keeps it.
func TestApplyItemLocalizationKeepsNFONamedArtwork(t *testing.T) {
	item := baseItem()
	item.PosterPath = "nfo/movies/c1/poster/original.webp"
	item.PosterSourcePath = "nfo+https://img.example.org/poster.jpg"
	loc := &models.MediaItemLocalization{
		PosterPath:       "posters/fr.jpg",
		PosterSourcePath: "tmdb://fr-poster.jpg",
	}
	got := applyItemLocalization(item, loc)
	if got.PosterPath != item.PosterPath || got.PosterSourcePath != item.PosterSourcePath {
		t.Errorf("localized poster replaced the NFO's: path %q source %q", got.PosterPath, got.PosterSourcePath)
	}

	season := &models.Season{
		ContentID:        "s2",
		PosterPath:       "nfo/series/c1/poster/2/original.webp",
		PosterSourcePath: "nfo+https://img.example.org/season02.jpg",
	}
	seasonLoc := &models.SeasonLocalization{PosterPath: "provider.jpg", PosterSourcePath: "tvdb://fr.jpg"}
	if got := applySeasonLocalization(season, seasonLoc, false); got.PosterPath != season.PosterPath || got.PosterSourcePath != season.PosterSourcePath {
		t.Errorf("localized season poster replaced the NFO's: %+v", got)
	}

	// Until the copy lands, the slot serves earlier provider art, which the
	// localized poster still replaces.
	item.PosterPath = "tmdb/movies/1/poster/original.webp"
	if got := applyItemLocalization(item, loc); got.PosterPath != "posters/fr.jpg" {
		t.Errorf("uncached NFO poster: path %q, want the localized poster", got.PosterPath)
	}
}
