package notifications

import "testing"

func TestCapturedFanoutCandidatesPreserveCursorAdvances(t *testing.T) {
	waiting := SeriesInterest{NextUpCandidate: true, NextExpectedEpisodeKey: new(20), NextExpectedOpen: true}
	captured := map[profileKey]struct{}{{1, "captured"}: {}}
	refreshed := []SeriesInterest{
		{UserID: 1, ProfileID: "new-profile", NextUpCandidate: true, NextExpectedEpisodeKey: new(20)},
		{UserID: 1, ProfileID: "captured", NextUpCandidate: true, NextExpectedEpisodeKey: new(20), NextExpectedOpen: true, LastNotifiedEpisodeKey: new(20)},
		// The same profile ID on another account was not captured.
		{UserID: 2, ProfileID: "captured", NextUpCandidate: true, NextExpectedEpisodeKey: new(20)},
	}
	candidates := capturedFanoutCandidates(refreshed, captured)
	if len(candidates) != 1 || candidates[0].UserID != 1 || candidates[0].ProfileID != "captured" {
		t.Fatalf("refreshed recipients escaped captured profile set: %+v", candidates)
	}
	prefs := DefaultPreferences(1, "captured")
	if _, eligible := EvaluateRecipient(waiting, prefs, 21); !eligible {
		t.Fatal("before the earlier event, a later episode is the next")
	}
	for _, episode := range []int{20, 21} {
		if _, eligible := EvaluateRecipient(candidates[0], prefs, episode); eligible {
			t.Fatalf("episode %d ignored earlier event's refreshed cursor", episode)
		}
	}
}
