package notifications

import (
	"testing"
)

func intPtr(v int) *int { return &v }

// following is a Next Up candidate row waiting at cursor.
func following(cursor int) SeriesInterest {
	return SeriesInterest{NextUpCandidate: true, NextExpectedEpisodeKey: intPtr(cursor)}
}

// recipientNotified runs EvaluateRecipient and checks its flags: only
// next_up, set exactly when a delivery is created.
func recipientNotified(t *testing.T, interest SeriesInterest, prefs Preferences, key int, highestBelow *int) bool {
	t.Helper()
	flags, ok := EvaluateRecipient(interest, prefs, key, highestBelow)
	if flags != (ReasonFlags{NextUp: ok}) {
		t.Fatalf("flags %+v with ok=%v; want only next_up, set exactly when ok", flags, ok)
	}
	return ok
}

// TestEvaluateRecipientOutOfOrder pins what happens when a later episode
// lands before an earlier one: an episode the catalog lists holds its place
// before its file arrives.
func TestEvaluateRecipientOutOfOrder(t *testing.T) {
	prefs := DefaultPreferences(1, "p1")
	notifies := func(interest SeriesInterest, prefs Preferences, key int, highestBelow *int) bool {
		t.Helper()
		return recipientNotified(t, interest, prefs, key, highestBelow)
	}

	t.Run("a listed episode without a file holds its place", func(t *testing.T) {
		// The catalog lists E06 before its file arrives, and E07 lands first.
		interest := following(EpisodeKey(1, 6))
		if notifies(interest, prefs, EpisodeKey(1, 7), intPtr(EpisodeKey(1, 6))) {
			t.Fatal("E07 must not notify while the catalog lists E06 ahead of it")
		}
		if !notifies(interest, prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E06 must notify when it lands, although E07 landed first")
		}
	})

	t.Run("an episode the catalog did not list arrives late", func(t *testing.T) {
		// Nothing is listed after E05, so E07 is the first listed episode at
		// the cursor when it lands.
		if !notifies(following(EpisodeKey(1, 6)), prefs, EpisodeKey(1, 7), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E07 is the first listed episode after the cursor and must notify")
		}
		// E06 then appears and lands after E07 notified.
		late := following(EpisodeKey(1, 6))
		late.LastNotifiedEpisodeKey = intPtr(EpisodeKey(1, 7))
		if notifies(late, prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E06 arriving after E07 notified must not notify")
		}
	})
}
