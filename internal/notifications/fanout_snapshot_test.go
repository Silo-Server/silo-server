package notifications

import "testing"

func TestCapturedFanoutCandidatesPreserveCursorAdvances(t *testing.T) {
	captured := map[profileKey]struct{}{{1, "captured"}: {}}
	refreshed := []SeriesInterest{
		{UserID: 1, ProfileID: "new-profile", Favorite: true},
		{UserID: 1, ProfileID: "captured", Favorite: true, LastNotifiedEpisodeKey: new(20)},
		// The same profile ID on another account was not captured.
		{UserID: 2, ProfileID: "captured", Favorite: true},
	}
	candidates := capturedFanoutCandidates(refreshed, captured)
	if len(candidates) != 1 || candidates[0].UserID != 1 || candidates[0].ProfileID != "captured" {
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
	candidates := capturedFanoutCandidates([]SeriesInterest{{UserID: 1, ProfileID: "new-profile", Favorite: true}}, map[profileKey]struct{}{{1, "removed"}: {}})
	if len(candidates) != 0 {
		t.Fatalf("new or removed interest entered batch: %+v", candidates)
	}
}
