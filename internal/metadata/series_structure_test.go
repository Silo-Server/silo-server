package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func structureEpisode(season, number int, title string, ids map[string]string) EpisodeResult {
	return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, Title: title, ProviderIDs: ids}
}

type structureKey struct{ season, number int }

func episodesByKey(episodes []EpisodeResult) map[structureKey]EpisodeResult {
	out := make(map[structureKey]EpisodeResult, len(episodes))
	for _, e := range episodes {
		out[structureKey{e.SeasonNumber, e.EpisodeNumber}] = e
	}
	return out
}

// Bleach: TVDB splits the show into arcs, TMDB keeps one 366-episode season.
// Merging by number used to append TMDB's episodes 21..366 to TVDB's season 1.
func TestMergeSeriesStructure_OneProviderNumbersTheShow(t *testing.T) {
	tvdb := providerStructure{
		slug:    "tvdb",
		seasons: []SeasonResult{{SeasonNumber: 1, Title: "The Substitute"}, {SeasonNumber: 2, Title: "The Entry"}},
		episodes: []EpisodeResult{
			structureEpisode(1, 20, "", map[string]string{"tvdb": "t20", "tmdb": "m20"}),
			structureEpisode(2, 1, "Enter! The World of the Shinigami", map[string]string{"tvdb": "t21"}),
		},
	}
	tmdb := providerStructure{
		slug:    "tmdb",
		seasons: []SeasonResult{{SeasonNumber: 1, Title: "Season 1"}},
		episodes: []EpisodeResult{
			structureEpisode(1, 20, "The Older Brother's Wish", map[string]string{"tmdb": "m20"}),
			structureEpisode(1, 21, "Enter! The World of the Shinigami", map[string]string{"tmdb": "m21"}),
		},
	}

	seasons, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	if len(seasons) != 2 || seasons[0].Title != "The Substitute" || seasons[1].Title != "The Entry" {
		t.Fatalf("seasons = %+v, want TVDB's two seasons only", seasons)
	}
	got := episodesByKey(episodes)
	if len(got) != 2 {
		t.Fatalf("episodes = %+v, want TVDB's S1E20 and S2E1 only", episodes)
	}
	if _, blended := got[structureKey{1, 21}]; blended {
		t.Fatal("TMDB's S1E21 was merged into TVDB's season 1")
	}
	// TMDB fills the empty title of the episode both providers identify.
	if title := got[structureKey{1, 20}].Title; title != "The Older Brother's Wish" {
		t.Fatalf("S1E20 title = %q, want TMDB's title filled in by the shared TMDB ID", title)
	}
}

// TVDB has never heard of the show; TMDB, enabled for the library's episodes,
// numbers it instead.
func TestMergeSeriesStructure_FallsBackWhenPreferredHasNoEpisodes(t *testing.T) {
	tvdb := providerStructure{slug: "tvdb"}
	tmdb := providerStructure{
		slug:     "tmdb",
		seasons:  []SeasonResult{{SeasonNumber: 1, Title: "Season 1"}},
		episodes: []EpisodeResult{structureEpisode(1, 1, "Pilot", map[string]string{"tmdb": "m1"})},
	}

	seasons, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	if len(seasons) != 1 || len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Fatalf("seasons = %+v, episodes = %+v; want TMDB's season and episode", seasons, episodes)
	}
}

// A TVDB outage must not renumber a TVDB show to TMDB's order. The refresh
// takes no remote structure, so the stored episodes stay as they are.
func TestMergeSeriesStructure_NoFallbackWhenPreferredFailed(t *testing.T) {
	tvdb := providerStructure{slug: "tvdb", failed: true}
	tmdb := providerStructure{
		slug:     "tmdb",
		seasons:  []SeasonResult{{SeasonNumber: 1}},
		episodes: []EpisodeResult{structureEpisode(1, 21, "Enter!", map[string]string{"tmdb": "m21"})},
	}

	seasons, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	if len(seasons) != 0 || len(episodes) != 0 {
		t.Fatalf("seasons = %+v, episodes = %+v; want none while TVDB is failing", seasons, episodes)
	}
}

// Every library holding a series shares its episode rows, so the order comes
// from the series' libraries, whichever library asked for the refresh: the
// first enabled one, in library order, that picks a provider.
func TestSeriesEpisodeOrderProvider(t *testing.T) {
	cases := []struct {
		name    string
		folders map[int]*models.MediaFolder
		want    string
	}{
		{"no library picks one", map[int]*models.MediaFolder{
			10: {ID: 10, Enabled: true},
		}, ""},
		{"a later library picks TMDB", map[int]*models.MediaFolder{
			10: {ID: 10, Enabled: true, SortOrder: 0},
			20: {ID: 20, Enabled: true, SortOrder: 1, EpisodeOrderProvider: "tmdb"},
		}, "tmdb"},
		{"the first library in order wins", map[int]*models.MediaFolder{
			10: {ID: 10, Enabled: true, SortOrder: 1, EpisodeOrderProvider: "tmdb"},
			20: {ID: 20, Enabled: true, SortOrder: 0, EpisodeOrderProvider: "tvdb"},
		}, "tvdb"},
		{"a disabled library has no say", map[int]*models.MediaFolder{
			10: {ID: 10, Enabled: false, EpisodeOrderProvider: "tmdb"},
			20: {ID: 20, Enabled: true},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			h.service.folderRepo = &fakeMetadataFolderRepo{folders: tc.folders}
			for id := range tc.folders {
				_ = h.libraryRepo.Upsert(context.Background(), "series-tvdb-74796", id, time.Now())
			}
			got, err := h.service.seriesEpisodeOrderProvider(context.Background(), "series-tvdb-74796")
			if err != nil {
				t.Fatalf("seriesEpisodeOrderProvider = %v", err)
			}
			if got != tc.want {
				t.Fatalf("seriesEpisodeOrderProvider = %q, want %q", got, tc.want)
			}
		})
	}
}

// Providers rarely send each other's episode IDs, so episodes are bridged by
// air date: the date both providers give exactly one episode, else the same
// numbers on the same date. A bridged episode fills empty fields and lends
// its IDs, so the stored row learns its TMDB ID too.
func TestMergeSeriesStructure_BridgesByAirDate(t *testing.T) {
	ep := func(season, number int, title, air, overview string, ids map[string]string) EpisodeResult {
		return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, Title: title, AirDate: air, Overview: overview, ProviderIDs: ids}
	}
	tvdb := providerStructure{
		slug:    "tvdb",
		seasons: []SeasonResult{{SeasonNumber: 2}, {SeasonNumber: 3}},
		episodes: []EpisodeResult{
			ep(2, 1, "Enter!", "2005-03-08", "", map[string]string{"tvdb": "t21"}),
			// A streaming drop: every episode of the season on one day.
			ep(3, 1, "Drop one", "2020-01-01", "", map[string]string{"tvdb": "t50"}),
			ep(3, 2, "Drop two", "2020-01-01", "", map[string]string{"tvdb": "t51"}),
		},
	}
	tmdb := providerStructure{
		slug: "tmdb",
		episodes: []EpisodeResult{
			ep(1, 21, "Enter! The World of the Shinigami", "2005-03-08", "Ichigo arrives.", map[string]string{"tmdb": "m21"}),
			ep(3, 2, "Drop two", "2020-01-01", "The second one.", map[string]string{"tmdb": "m51"}),
			// Same date, different numbers: ambiguous, so not bridged.
			ep(3, 9, "Drop nine", "2020-01-01", "Wrong one.", map[string]string{"tmdb": "m59"}),
		},
	}

	_, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	got := episodesByKey(episodes)
	if len(got) != 3 {
		t.Fatalf("episodes = %+v, want TVDB's three", episodes)
	}
	enter := got[structureKey{2, 1}]
	if enter.Overview != "Ichigo arrives." || enter.ProviderIDs["tmdb"] != "m21" || enter.ProviderIDs["tvdb"] != "t21" {
		t.Fatalf("S2E1 = %+v, want TMDB's S1E21 bridged by its unique air date", enter)
	}
	if two := got[structureKey{3, 2}]; two.Overview != "The second one." || two.ProviderIDs["tmdb"] != "m51" {
		t.Fatalf("S3E2 = %+v, want TMDB's S3E2 bridged by number on the same date", two)
	}
	if one := got[structureKey{3, 1}]; one.Overview != "" || one.ProviderIDs["tmdb"] != "" {
		t.Fatalf("S3E1 = %+v, want nothing bridged onto it", one)
	}
}

// Double bills air two episodes on one day; both providers listing two that
// day pair them in episode order.
func TestMergeSeriesStructure_BridgesSameDayPairsInOrder(t *testing.T) {
	ep := func(season, number int, air string, ids map[string]string) EpisodeResult {
		return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, AirDate: air, ProviderIDs: ids}
	}
	tvdb := providerStructure{slug: "tvdb", episodes: []EpisodeResult{
		ep(3, 11, "2005-10-04", map[string]string{"tvdb": "t52"}),
		ep(3, 12, "2005-10-04", map[string]string{"tvdb": "t53"}),
	}}
	tmdb := providerStructure{slug: "tmdb", episodes: []EpisodeResult{
		ep(1, 52, "2005-10-04", map[string]string{"tmdb": "m52"}),
		ep(1, 53, "2005-10-04", map[string]string{"tmdb": "m53"}),
	}}

	_, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	got := episodesByKey(episodes)
	if got[structureKey{3, 11}].ProviderIDs["tmdb"] != "m52" || got[structureKey{3, 12}].ProviderIDs["tmdb"] != "m53" {
		t.Fatalf("episodes = %+v, want TMDB's E52/E53 on TVDB's S3E11/S3E12", episodes)
	}
}

// Providers can disagree on an air date by a day (time zones). An episode
// between two bridged neighbours, with exactly as many unbridged episodes
// between the structure provider's matching neighbours, pairs in order.
func TestMergeSeriesStructure_BridgesGapsBetweenMatchedNeighbours(t *testing.T) {
	ep := func(season, number int, air string, ids map[string]string) EpisodeResult {
		return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, AirDate: air, ProviderIDs: ids}
	}
	tvdb := providerStructure{slug: "tvdb", episodes: []EpisodeResult{
		ep(2, 12, "2005-05-17", map[string]string{"tvdb": "t32"}),
		ep(2, 13, "2005-05-24", map[string]string{"tvdb": "t33"}),
		ep(2, 14, "2005-05-31", map[string]string{"tvdb": "t34"}),
		ep(2, 15, "2005-06-07", map[string]string{"tvdb": "t35"}),
	}}
	tmdb := providerStructure{slug: "tmdb", episodes: []EpisodeResult{
		ep(1, 32, "2005-05-17", map[string]string{"tmdb": "m32"}),
		ep(1, 33, "2005-05-26", map[string]string{"tmdb": "m33"}),
		ep(1, 34, "2005-06-01", map[string]string{"tmdb": "m34"}),
		ep(1, 35, "2005-06-07", map[string]string{"tmdb": "m35"}),
	}}

	_, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	got := episodesByKey(episodes)
	for number, want := range map[int]string{12: "m32", 13: "m33", 14: "m34", 15: "m35"} {
		if id := got[structureKey{2, number}].ProviderIDs["tmdb"]; id != want {
			t.Fatalf("S2E%d tmdb = %q, want %q (episodes %+v)", number, id, want, episodes)
		}
	}
}

// The newest episodes come after the last bridged neighbour, often a day
// apart across time zones. As many unmatched episodes at the end of both
// lists pair in order; the same holds at the start.
func TestMergeSeriesStructure_BridgesTheRunsAtEitherEnd(t *testing.T) {
	ep := func(season, number int, air string, ids map[string]string) EpisodeResult {
		return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, AirDate: air, ProviderIDs: ids}
	}
	tvdb := providerStructure{slug: "tvdb", episodes: []EpisodeResult{
		ep(17, 1, "2026-01-05", map[string]string{"tvdb": "t1"}),
		ep(17, 48, "2026-10-12", map[string]string{"tvdb": "t48"}),
		ep(17, 49, "2026-10-19", map[string]string{"tvdb": "t49"}),
		ep(17, 50, "2026-10-26", map[string]string{"tvdb": "t50"}),
	}}
	tmdb := providerStructure{slug: "tmdb", episodes: []EpisodeResult{
		ep(2, 1, "2026-01-06", map[string]string{"tmdb": "m1"}),
		ep(2, 48, "2026-10-12", map[string]string{"tmdb": "m48"}),
		ep(2, 49, "2026-10-20", map[string]string{"tmdb": "m49"}),
		ep(2, 50, "2026-10-27", map[string]string{"tmdb": "m50"}),
	}}

	_, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	got := episodesByKey(episodes)
	for number, want := range map[int]string{1: "m1", 48: "m48", 49: "m49", 50: "m50"} {
		if id := got[structureKey{17, number}].ProviderIDs["tmdb"]; id != want {
			t.Fatalf("S17E%d tmdb = %q, want %q", number, id, want)
		}
	}
}

// A bridged episode fills empty fields, but never the structure provider's
// numbers: season 0 (specials) is a number, not an empty field. One Piece's
// "Dream 9" special is TVDB S0E39 and TMDB S15E?; it must stay S0E39.
func TestMergeSeriesStructure_BridgeKeepsTheStructureNumbers(t *testing.T) {
	tvdb := providerStructure{slug: "tvdb", episodes: []EpisodeResult{
		{SeasonNumber: 0, EpisodeNumber: 39, AirDate: "2013-04-07", ProviderIDs: map[string]string{"tvdb": "4543896"}},
	}}
	tmdb := providerStructure{slug: "tmdb", episodes: []EpisodeResult{
		{SeasonNumber: 15, EpisodeNumber: 99, AirDate: "2013-04-07", Overview: "Crossover.", ProviderIDs: map[string]string{"tmdb": "1154317"}},
	}}

	_, episodes, _ := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	if len(episodes) != 1 || episodes[0].SeasonNumber != 0 || episodes[0].EpisodeNumber != 39 || episodes[0].Overview != "Crossover." {
		t.Fatalf("episodes = %+v, want TVDB's S0E39 with TMDB's overview", episodes)
	}
}
