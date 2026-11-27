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
			// A placeholder the scanner made for S03E01.mkv while no order had
			// that episode stands in for the episode at those numbers. When an
			// order brings that episode back, the placeholder merges into it
			// (files and watch state) instead of being parked.
			name: "fallback row in a moving row's way merges into it",
			existing: []existingEpisode{
				{ContentID: "fb", Season: 3, Episode: 1, Fallback: true},
				{ContentID: "c", Season: 17, Episode: 1, IDs: map[string]string{"tvdb": "t3"}},
			},
			targets: []EpisodeResult{renumberTarget(3, 1, map[string]string{"tvdb": "t3"})},
			want: []episodeMove{
				{ContentID: "c", FromSeason: 17, FromEpisode: 1, ToSeason: 3, ToEpisode: 1},
			},
			merges: []episodeMerge{{From: "fb", Into: "c"}},
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

func TestPlanFileRelinks(t *testing.T) {
	rows := map[episodeSlot]string{{1, 1}: "s1e1", {2, 1}: "s2e1", {17, 1}: "s17e1"}
	fallback := map[string]bool{"fb-s3e22": true}
	cases := []struct {
		name    string
		files   []seriesFileLink
		relinks []fileRelink
		merges  []episodeMerge
	}{
		{
			// A renumber carried the row S02E01.mkv was linked to off to S17:
			// the file goes back to the episode its name says.
			name:    "file follows its name, not its old row",
			files:   []seriesFileLink{{ID: 1, EpisodeID: "s17e1", Season: 2, Episode: 1, Numbered: true}},
			relinks: []fileRelink{{FileID: 1, From: "s17e1", To: "s2e1", Season: 2, Episode: 1}},
		},
		{
			name:  "file already on its episode stays",
			files: []seriesFileLink{{ID: 1, EpisodeID: "s1e1", Season: 1, Episode: 1, Numbered: true}},
		},
		{
			// Air-date and bare-number links were made by identity, not by the
			// numbers in the name, so a moving row keeps them.
			name:  "file linked by air date is left alone",
			files: []seriesFileLink{{ID: 1, EpisodeID: "s17e1"}},
		},
		{
			// No episode has the file's numbers yet: the link is cleared so the
			// next link pass creates or finds the row.
			name:    "file whose numbers no episode has is unlinked",
			files:   []seriesFileLink{{ID: 1, EpisodeID: "s17e1", Season: 9, Episode: 9, Numbered: true}},
			relinks: []fileRelink{{FileID: 1, From: "s17e1", To: "", Season: 9, Episode: 9}},
		},
		{
			// A placeholder made for a file no order placed is retired once its
			// only file reaches a real episode: its watch state merges there.
			name:    "emptied fallback row merges into the file's episode",
			files:   []seriesFileLink{{ID: 1, EpisodeID: "fb-s3e22", Season: 2, Episode: 1, Numbered: true}},
			relinks: []fileRelink{{FileID: 1, From: "fb-s3e22", To: "s2e1", Season: 2, Episode: 1}},
			merges:  []episodeMerge{{From: "fb-s3e22", Into: "s2e1"}},
		},
		{
			// A placeholder is the file's episode while no order has one; a
			// renumber that parked it does not unlink its file.
			name: "fallback row that keeps a file stays",
			files: []seriesFileLink{
				{ID: 1, EpisodeID: "fb-s3e22", Season: 2, Episode: 1, Numbered: true},
				{ID: 2, EpisodeID: "fb-s3e22", Season: 3, Episode: 22, Numbered: true},
			},
			relinks: []fileRelink{
				{FileID: 1, From: "fb-s3e22", To: "s2e1", Season: 2, Episode: 1},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relinks, merges := planFileRelinks(tc.files, rows, fallback)
			if !reflect.DeepEqual(relinks, tc.relinks) || !reflect.DeepEqual(merges, tc.merges) {
				t.Fatalf("planFileRelinks =\n%+v\n%+v\nwant\n%+v\n%+v", relinks, merges, tc.relinks, tc.merges)
			}
		})
	}
}
