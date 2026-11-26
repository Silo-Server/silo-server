package metadata

import (
	"testing"
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

	seasons, episodes := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

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

	seasons, episodes := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

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

	seasons, episodes := mergeSeriesStructure([]providerStructure{tvdb, tmdb}, "tvdb", "en")

	if len(seasons) != 0 || len(episodes) != 0 {
		t.Fatalf("seasons = %+v, episodes = %+v; want none while TVDB is failing", seasons, episodes)
	}
}
