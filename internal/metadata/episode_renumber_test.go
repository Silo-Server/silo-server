package metadata

import (
	"reflect"
	"testing"
)

func renumberTarget(season, number int, ids map[string]string) EpisodeResult {
	return EpisodeResult{SeasonNumber: season, EpisodeNumber: number, ProviderIDs: ids}
}

func TestPlanEpisodeRenumber(t *testing.T) {
	cases := []struct {
		name     string
		existing []existingEpisode
		targets  []EpisodeResult
		want     []episodeMove
		merges   []episodeMerge
	}{
		{
			// Same provider, another order: the provider's episode IDs say where
			// each row goes, even when two episodes trade numbers.
			name: "rows swap numbers by provider ID",
			existing: []existingEpisode{
				{ContentID: "a", Season: 1, Episode: 1, IDs: map[string]string{"tvdb": "t1"}},
				{ContentID: "b", Season: 1, Episode: 2, IDs: map[string]string{"tvdb": "t2"}},
			},
			targets: []EpisodeResult{
				renumberTarget(1, 1, map[string]string{"tvdb": "t2"}),
				renumberTarget(1, 2, map[string]string{"tvdb": "t1"}),
			},
			want: []episodeMove{
				{ContentID: "a", FromSeason: 1, FromEpisode: 1, ToSeason: 1, ToEpisode: 2},
				{ContentID: "b", FromSeason: 1, FromEpisode: 2, ToSeason: 1, ToEpisode: 1},
			},
		},
		{
			name: "rows already in place stay",
			existing: []existingEpisode{
				{ContentID: "a", Season: 1, Episode: 1, IDs: map[string]string{"tvdb": "t1"}},
			},
			targets: []EpisodeResult{renumberTarget(1, 1, map[string]string{"tvdb": "t1"})},
			want:    nil,
		},
		{
			// A row no provider identifies keeps its slot when a target without a
			// matching row lands there, as the upsert by number always did.
			name: "unidentified row at a free target slot stays",
			existing: []existingEpisode{
				{ContentID: "x", Season: 1, Episode: 3},
			},
			targets: []EpisodeResult{renumberTarget(1, 3, map[string]string{"tvdb": "t3"})},
			want:    nil,
		},
		{
			// A moving row needs the slot an unidentified row holds. That row may
			// carry files or watch history, so it is parked after the season's
			// last episode instead of being dropped.
			name: "unidentified row in a moving row's way is parked",
			existing: []existingEpisode{
				{ContentID: "x", Season: 1, Episode: 2},
				{ContentID: "c", Season: 2, Episode: 1, IDs: map[string]string{"tvdb": "t3"}},
			},
			targets: []EpisodeResult{
				renumberTarget(1, 1, map[string]string{"tvdb": "t9"}),
				renumberTarget(1, 2, map[string]string{"tvdb": "t3"}),
			},
			want: []episodeMove{
				{ContentID: "c", FromSeason: 2, FromEpisode: 1, ToSeason: 1, ToEpisode: 2},
				{ContentID: "x", FromSeason: 1, FromEpisode: 2, ToSeason: 1, ToEpisode: 3},
			},
		},
		{
			// Bleach after the blend: TVDB's S2E1 row and the old TMDB-numbered
			// S1E21 row are the same episode, as the bridged TMDB ID shows. The
			// TVDB row stays; the duplicate merges into it.
			name: "duplicate row merges into the claimed one",
			existing: []existingEpisode{
				{ContentID: "tvdb-s2e1", Season: 2, Episode: 1, IDs: map[string]string{"tvdb": "t21"}},
				{ContentID: "tmdb-s1e21", Season: 1, Episode: 21, IDs: map[string]string{"tmdb": "m21"}},
			},
			targets: []EpisodeResult{renumberTarget(2, 1, map[string]string{"tvdb": "t21", "tmdb": "m21"})},
			want:    nil,
			merges:  []episodeMerge{{From: "tmdb-s1e21", Into: "tvdb-s2e1"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planEpisodeRenumber(tc.existing, tc.targets, "tvdb")
			if !reflect.DeepEqual(got.Moves, tc.want) || !reflect.DeepEqual(got.Merges, tc.merges) {
				t.Fatalf("planEpisodeRenumber =\n%+v\nwant moves\n%+v\nmerges\n%+v", got, tc.want, tc.merges)
			}
		})
	}
}
