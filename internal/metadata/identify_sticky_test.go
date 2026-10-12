package metadata

import (
	"context"
	"testing"
)

// The local provider is left out when the file names another title than the
// one the run keeps: a same-key contradiction outside a manual refresh, or any
// declared IDs that don't confirm an identity an admin chose with Identify, in
// every mode. A stored ID recorded as dead is no identity to keep.
func TestLocalIdentityConflict(t *testing.T) {
	t.Parallel()

	dead := make(providerIDValueSet)
	dead.add("tmdb", "123")
	cases := []struct {
		name     string
		mode     RefreshMode
		chosen   bool
		declared map[string]string
		stored   map[string]string
		stale    providerIDValueSet
		want     bool
	}{
		{"scheduled: same-key contradiction", ModeScheduledRefresh, false, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, nil, true},
		{"scheduled: file confirms", ModeScheduledRefresh, true, map[string]string{"tmdb": "123", "imdb": "tt0000456"}, map[string]string{"tmdb": "123"}, nil, false},
		{"scheduled: other key only, not identified", ModeScheduledRefresh, false, map[string]string{"imdb": "tt0000456"}, map[string]string{"tvdb": "789"}, nil, false},
		{"scheduled: other key only, identified", ModeScheduledRefresh, true, map[string]string{"imdb": "tt0000456"}, map[string]string{"tvdb": "789"}, nil, true},
		{"scheduled: stored ID is dead", ModeScheduledRefresh, false, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, dead, false},
		{"scheduled: file declares a dead ID", ModeScheduledRefresh, false, map[string]string{"tmdb": "123"}, map[string]string{"tmdb": "456"}, dead, false},
		{"manual: identified, file declares a dead ID", ModeManualRefresh, true, map[string]string{"tmdb": "123"}, map[string]string{"tmdb": "456"}, dead, false},
		{"initial match: same-key contradiction", ModeInitialMatch, false, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, nil, true},
		{"manual: contradiction re-matches", ModeManualRefresh, false, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, nil, false},
		{"manual: identified", ModeManualRefresh, true, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, nil, true},
		{"manual: identified ID is dead", ModeManualRefresh, true, map[string]string{"tmdb": "456"}, map[string]string{"tmdb": "123"}, dead, false},
		{"no declared IDs", ModeScheduledRefresh, true, nil, map[string]string{"tmdb": "123"}, nil, false},
		{"scheduled: stored IMDb ID in another case", ModeScheduledRefresh, false, map[string]string{"imdb": "tt0111161"}, map[string]string{"imdb": "TT0111161"}, nil, false},
		{"manual: identified, stored IMDb ID in another case", ModeManualRefresh, true, map[string]string{"imdb": "tt0111161"}, map[string]string{"imdb": "TT0111161"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, repo := newLocalPipelineHarness()
			repo.chosen["item"] = tc.chosen
			hints := []localIdentityHint{{provider: "nfo", ids: tc.declared}}
			got, err := h.service.localIdentityConflict(context.Background(), tc.mode, "item", hints, tc.stored, tc.stale)
			if err != nil {
				t.Fatalf("localIdentityConflict: %v", err)
			}
			if got != tc.want {
				t.Errorf("conflict = %v, want %v", got, tc.want)
			}
			if tc.stored["tmdb"] == "" && tc.stored["tvdb"] == "" && tc.stored["imdb"] == "" {
				t.Errorf("stored identity was modified: %v", tc.stored)
			}
		})
	}
}

// An IMDb ID stored in another case agrees with the same ID chosen in
// lowercase, so restating it is no correction of the stored match.
func TestIdentityChoiceCorrectsComparesNormalizedIDs(t *testing.T) {
	t.Parallel()

	stored := map[string]string{"tmdb": "123", "imdb": "TT0111161"}
	if identityChoiceCorrects(map[string]string{"imdb": "tt0111161"}, stored) {
		t.Error("an IMDb ID differing only in case counted as a correction")
	}
	if !identityChoiceCorrects(map[string]string{"imdb": "tt0000002"}, stored) {
		t.Error("a different IMDb ID did not count as a correction")
	}
}
