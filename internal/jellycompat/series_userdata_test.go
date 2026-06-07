package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func ep(id string) *models.Episode {
	return &models.Episode{ContentID: id}
}

// TestSeriesUserDataFromEpisodes covers the series watch-state rollup: a
// series has no progress row of its own, so Played/UnplayedCount/
// InProgressCount are aggregated from per-episode progress.
func TestSeriesUserDataFromEpisodes(t *testing.T) {
	cases := []struct {
		name           string
		episodes       []*models.Episode
		progress       map[string]userstore.WatchProgress
		wantWatched    int
		wantUnplayed   int
		wantInProgress int
		wantPlayed     bool
	}{
		{
			name:     "no episodes",
			episodes: nil,
			progress: map[string]userstore.WatchProgress{},
			// Played must be false for an empty series — unplayed==0 alone
			// must not flip it.
			wantPlayed: false,
		},
		{
			name:     "all watched",
			episodes: []*models.Episode{ep("a"), ep("b")},
			progress: map[string]userstore.WatchProgress{
				"a": {Completed: true},
				"b": {Completed: true},
			},
			wantWatched: 2,
			wantPlayed:  true,
		},
		{
			name:     "mixed watched, in-progress, untouched",
			episodes: []*models.Episode{ep("a"), ep("b"), ep("c")},
			progress: map[string]userstore.WatchProgress{
				"a": {Completed: true},
				"b": {PositionSeconds: 120},
			},
			wantWatched:    1,
			wantUnplayed:   2,
			wantInProgress: 1,
			wantPlayed:     false,
		},
		{
			name:         "no progress at all",
			episodes:     []*models.Episode{ep("a")},
			progress:     map[string]userstore.WatchProgress{},
			wantUnplayed: 1,
			wantPlayed:   false,
		},
		{
			name:     "nil episodes skipped",
			episodes: []*models.Episode{nil, ep("a"), nil},
			progress: map[string]userstore.WatchProgress{
				"a": {Completed: true},
			},
			wantWatched: 1,
			wantPlayed:  true,
		},
		{
			name:     "zero-position progress row is not in-progress",
			episodes: []*models.Episode{ep("a")},
			progress: map[string]userstore.WatchProgress{
				"a": {PositionSeconds: 0},
			},
			wantUnplayed:   1,
			wantInProgress: 0,
			wantPlayed:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seriesUserDataFromEpisodes(tc.episodes, tc.progress)
			if got.WatchedCount != tc.wantWatched {
				t.Errorf("WatchedCount = %d, want %d", got.WatchedCount, tc.wantWatched)
			}
			if got.UnplayedCount != tc.wantUnplayed {
				t.Errorf("UnplayedCount = %d, want %d", got.UnplayedCount, tc.wantUnplayed)
			}
			if got.InProgressCount != tc.wantInProgress {
				t.Errorf("InProgressCount = %d, want %d", got.InProgressCount, tc.wantInProgress)
			}
			if got.Played != tc.wantPlayed {
				t.Errorf("Played = %v, want %v", got.Played, tc.wantPlayed)
			}
		})
	}
}

// TestModelEpisodeContentIDs verifies nil episodes and empty content ids are
// dropped — these ids feed SQL IN-lists.
func TestModelEpisodeContentIDs(t *testing.T) {
	got := modelEpisodeContentIDs([]*models.Episode{nil, ep(""), ep("a"), ep("b")})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("modelEpisodeContentIDs = %v, want [a b]", got)
	}
}
