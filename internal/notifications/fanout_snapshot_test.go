package notifications

import "testing"

func TestCapturedFanoutCandidatesPreserveCursorAdvances(t *testing.T) {
	captured := map[string]struct{}{"captured": {}}
	refreshed := []SeriesInterest{
		{ProfileID: "new-profile", Favorite: true},
		{ProfileID: "captured", Favorite: true, LastNotifiedEpisodeKey: new(20)},
	}
	candidates := capturedFanoutCandidates(refreshed, captured)
	if len(candidates) != 1 || candidates[0].ProfileID != "captured" {
		t.Fatalf("refreshed recipients escaped captured profile set: %+v", candidates)
	}
	prefs := Preferences{Enabled: true, NotifyFavorites: true}
	for _, episode := range []int{19, 20} {
		if _, eligible := EvaluateRecipient(candidates[0], prefs, episode); eligible {
			t.Fatalf("episode %d ignored earlier event's refreshed cursor", episode)
		}
	}
	if _, eligible := EvaluateRecipient(candidates[0], prefs, 21); !eligible {
		t.Fatal("later episode should remain eligible")
	}
}

func TestCapturedFanoutCandidatesDoNotRestoreRemovedInterest(t *testing.T) {
	candidates := capturedFanoutCandidates([]SeriesInterest{{ProfileID: "new-profile", Favorite: true}}, map[string]struct{}{"removed": {}})
	if len(candidates) != 0 {
		t.Fatalf("new or removed interest entered batch: %+v", candidates)
	}
}
