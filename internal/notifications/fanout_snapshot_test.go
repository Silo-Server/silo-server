package notifications

import "testing"

func TestCapturedFanoutCandidatesPreserveCursorAdvances(t *testing.T) {
	captured := map[profileKey]struct{}{{1, "captured"}: {}}
	refreshed := []SeriesInterest{
		{UserID: 1, ProfileID: "new-profile", NextUpCandidate: true, NextExpectedEpisodeKey: new(19)},
		{UserID: 1, ProfileID: "captured", NextUpCandidate: true, NextExpectedEpisodeKey: new(19), LastNotifiedEpisodeKey: new(20)},
		// The same profile ID on another account was not captured.
		{UserID: 2, ProfileID: "captured", NextUpCandidate: true, NextExpectedEpisodeKey: new(19)},
	}
	candidates := capturedFanoutCandidates(refreshed, captured)
	if len(candidates) != 1 || candidates[0].UserID != 1 || candidates[0].ProfileID != "captured" {
		t.Fatalf("refreshed recipients escaped captured profile set: %+v", candidates)
	}
	prefs := Preferences{Enabled: true, NotifyNextUp: true}
	for _, episode := range []int{19, 20} {
		if _, eligible := EvaluateRecipient(candidates[0], prefs, episode, nil); eligible {
			t.Fatalf("episode %d ignored earlier event's refreshed cursor", episode)
		}
	}
	if _, eligible := EvaluateRecipient(candidates[0], prefs, 21, nil); !eligible {
		t.Fatal("later episode should remain eligible")
	}
}
