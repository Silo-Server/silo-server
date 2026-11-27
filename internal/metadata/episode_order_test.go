package metadata

import (
	"reflect"
	"testing"
)

func intRef(v int) *int { return &v }

type slotOf struct{ season, episode int }

func fileSlots(episodes []EpisodeResult) []any {
	out := make([]any, len(episodes))
	for i, ep := range episodes {
		if !ep.FileNumbered {
			out[i] = nil
			continue
		}
		out[i] = slotOf{ep.FileSeasonNumber, ep.FileEpisodeNumber}
	}
	return out
}

func TestApplyAbsoluteLayout(t *testing.T) {
	seasons := []SeasonResult{
		{SeasonNumber: 0, Title: "Specials"},
		{SeasonNumber: 1, Title: "The Substitute", PosterPath: "/s1.jpg"},
		{SeasonNumber: 2, Title: "The Entry"},
	}
	episodes := []EpisodeResult{
		{SeasonNumber: 2, EpisodeNumber: 1, Title: "c"},
		{SeasonNumber: 0, EpisodeNumber: 1, Title: "special"},
		{SeasonNumber: 1, EpisodeNumber: 2, Title: "b"},
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "a"},
	}
	gotSeasons, gotEpisodes := applyAbsoluteLayout(seasons, episodes)

	var numbers []slotOf
	titles := map[slotOf]string{}
	for _, ep := range gotEpisodes {
		s := slotOf{ep.SeasonNumber, ep.EpisodeNumber}
		numbers = append(numbers, s)
		titles[s] = ep.Title
	}
	want := map[slotOf]string{{1, 1}: "a", {1, 2}: "b", {1, 3}: "c", {0, 1}: "special"}
	if !reflect.DeepEqual(titles, want) || len(numbers) != 4 {
		t.Fatalf("absolute episodes = %v, want %v", titles, want)
	}
	if len(gotSeasons) != 2 || gotSeasons[0].SeasonNumber != 0 || gotSeasons[1].SeasonNumber != 1 ||
		gotSeasons[1].PosterPath != "/s1.jpg" || gotSeasons[1].Title != "Season 1" {
		t.Fatalf("absolute seasons = %+v, want specials and one Season 1 with the first season's poster", gotSeasons)
	}
}

func TestAssignFileNumbers(t *testing.T) {
	cases := []struct {
		name      string
		episodes  []EpisodeResult
		structure string
		fileOrder string
		providers []providerStructure
		want      []any
	}{
		{
			// Files follow the show's provider in its default order: the
			// numbers the episodes have.
			name:      "default order names files by the episode numbers",
			episodes:  []EpisodeResult{{SeasonNumber: 2, EpisodeNumber: 5}},
			structure: "tvdb",
			want:      []any{slotOf{2, 5}},
		},
		{
			// The show follows TVDB's absolute order; files are still named
			// in TVDB's aired order, which the provider gives per episode.
			name: "another order of the same provider uses its default numbers",
			episodes: []EpisodeResult{
				{SeasonNumber: 1, EpisodeNumber: 21, DefaultSeasonNumber: intRef(2), DefaultEpisodeNumber: intRef(1)},
			},
			structure: "tvdb",
			want:      []any{slotOf{2, 1}},
		},
		{
			// The show follows TMDB (one 366-episode season); the files are
			// named in TVDB's seasons. The bridge to TVDB's episodes names
			// them.
			name: "files named in the other provider's order bridge by id",
			episodes: []EpisodeResult{
				{SeasonNumber: 1, EpisodeNumber: 21, ProviderIDs: map[string]string{"tmdb": "m21", "tvdb": "t21"}},
				{SeasonNumber: 1, EpisodeNumber: 400, ProviderIDs: map[string]string{"tmdb": "m400"}},
			},
			structure: "tmdb",
			fileOrder: "tvdb",
			providers: []providerStructure{{slug: "tvdb", episodes: []EpisodeResult{
				{SeasonNumber: 2, EpisodeNumber: 1, ProviderIDs: map[string]string{"tvdb": "t21"}},
			}}},
			want: []any{slotOf{2, 1}, nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			episodes := append([]EpisodeResult(nil), tc.episodes...)
			assignFileNumbers(episodes, tc.structure, tc.fileOrder, tc.providers)
			if got := fileSlots(episodes); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("file numbers = %v, want %v", got, tc.want)
			}
		})
	}
}
