package notifications

import (
	"context"
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestEpisodeKey(t *testing.T) {
	if got := EpisodeKey(2, 1); got != 2_000_001 {
		t.Fatalf("EpisodeKey(2,1) = %d, want 2000001", got)
	}
	if got := EpisodeKey(0, 0); got != 0 {
		t.Fatalf("EpisodeKey(0,0) = %d, want 0", got)
	}
	// Absolute-numbered anime catalogs exceed 10k episodes in one season.
	if EpisodeKey(1, 11000) <= EpisodeKey(1, 10999) {
		t.Fatal("episode keys must stay ordered for large episode numbers")
	}
	if EpisodeKey(2, 0) <= EpisodeKey(1, 999_999) {
		t.Fatal("season boundary must dominate any in-season episode number")
	}
}

// TestEvaluateRecipient pins the Next Up rule: an episode notifies only when
// it is the lowest available episode at or after the profile's cursor.
// highestBelow is the highest key below the event's with a present file.
func TestEvaluateRecipient(t *testing.T) {
	prefs := DefaultPreferences(1, "p1")
	following := func(cursor int) SeriesInterest {
		return SeriesInterest{NextUpCandidate: true, NextExpectedEpisodeKey: intPtr(cursor)}
	}
	notifies := func(interest SeriesInterest, prefs Preferences, key int, highestBelow *int) bool {
		t.Helper()
		flags, ok := EvaluateRecipient(interest, prefs, key, highestBelow)
		if flags != (ReasonFlags{NextUp: ok}) {
			t.Fatalf("flags %+v with ok=%v; want only next_up, set exactly when ok", flags, ok)
		}
		return ok
	}

	t.Run("season premiere after a finished finale", func(t *testing.T) {
		// Finished S01E10, so the cursor is S01E11, which no episode has.
		finale := EpisodeKey(1, 10)
		if !notifies(following(finale+1), prefs, EpisodeKey(2, 1), intPtr(finale)) {
			t.Fatal("S02E01 is the next episode after the finale and must notify")
		}
	})

	t.Run("of several episodes landing together only the lowest notifies", func(t *testing.T) {
		// Caught up through E05; E06, E07 and E08 land in one batch, so each
		// one's highest available episode below is the one before it.
		interest := following(EpisodeKey(1, 6))
		if !notifies(interest, prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E06 must notify")
		}
		for _, n := range []int{7, 8} {
			if notifies(interest, prefs, EpisodeKey(1, n), intPtr(EpisodeKey(1, n-1))) {
				t.Fatalf("E%02d must not notify while E06 is available", n)
			}
		}
	})

	t.Run("out of order: a later episode that is Next Up notifies", func(t *testing.T) {
		// E07 lands before E06: E05 is the highest available below it.
		if !notifies(following(EpisodeKey(1, 6)), prefs, EpisodeKey(1, 7), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E07 is Home's Next Up while E06 is missing and must notify")
		}
		// E06 arrives after E07 notified.
		late := following(EpisodeKey(1, 6))
		late.LastNotifiedEpisodeKey = intPtr(EpisodeKey(1, 7))
		if notifies(late, prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E06 arriving after E07 notified must not notify")
		}
	})

	t.Run("a profile that is behind gets nothing", func(t *testing.T) {
		// Stopped at S01E02; E03 through E05 are available.
		if notifies(following(EpisodeKey(1, 3)), prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("E06 is not the profile's Next Up and must not notify")
		}
	})

	t.Run("an episode below the cursor does not notify", func(t *testing.T) {
		if notifies(following(EpisodeKey(1, 6)), prefs, EpisodeKey(1, 4), nil) {
			t.Fatal("an episode before the cursor must not notify")
		}
	})

	t.Run("no available episode below", func(t *testing.T) {
		if !notifies(following(EpisodeKey(1, 1)), prefs, EpisodeKey(1, 1), nil) {
			t.Fatal("the cursor's own episode with nothing below must notify")
		}
	})

	t.Run("last notified suppresses repeats and older keys", func(t *testing.T) {
		interest := following(EpisodeKey(1, 6))
		interest.LastNotifiedEpisodeKey = intPtr(EpisodeKey(1, 6))
		if notifies(interest, prefs, EpisodeKey(1, 6), intPtr(EpisodeKey(1, 5))) {
			t.Fatal("an already-notified key must not notify again")
		}
	})

	t.Run("next up needs a candidate with a cursor", func(t *testing.T) {
		if notifies(SeriesInterest{NextExpectedEpisodeKey: intPtr(EpisodeKey(1, 1))}, prefs, EpisodeKey(1, 1), nil) {
			t.Fatal("a row that is no Next Up candidate must not notify")
		}
		withoutCursor := SeriesInterest{NextUpCandidate: true}
		if notifies(withoutCursor, prefs, EpisodeKey(1, 1), nil) {
			t.Fatal("a Next Up candidate without a cursor must not notify")
		}
	})

	t.Run("preferences are a hard gate", func(t *testing.T) {
		nextUpOff := DefaultPreferences(1, "p1")
		nextUpOff.NotifyNextUp = false
		if notifies(following(EpisodeKey(1, 1)), nextUpOff, EpisodeKey(1, 1), nil) {
			t.Fatal("notify_next_up off must not produce a delivery")
		}
		killSwitch := DefaultPreferences(1, "p1")
		killSwitch.Enabled = false
		if notifies(following(EpisodeKey(1, 1)), killSwitch, EpisodeKey(1, 1), nil) {
			t.Fatal("master toggle must suppress everything")
		}
	})
}

func TestMemoryTicketStore(t *testing.T) {
	store := NewTicketStore(nil)
	ctx := context.Background()

	minted := Ticket{UserID: 7, ProfileID: "profile-1", ProfileToken: "proof"}
	ticket, ttl, err := store.Mint(ctx, minted)
	if err != nil || ticket == "" || ttl <= 0 {
		t.Fatalf("mint failed: %q %v %v", ticket, ttl, err)
	}

	binding, ok := store.Consume(ctx, ticket)
	if !ok || binding != minted {
		t.Fatalf("consume returned %+v %v", binding, ok)
	}

	if _, ok := store.Consume(ctx, ticket); ok {
		t.Fatal("tickets must be single-use")
	}
	if _, ok := store.Consume(ctx, "unknown"); ok {
		t.Fatal("unknown tickets must be rejected")
	}

	// A token no signed one could be is not kept: the viewer gate does not
	// read X-Profile-Token for a profile without a PIN.
	oversized := Ticket{UserID: 7, ProfileID: "profile-1", ProfileToken: strings.Repeat("x", maxTicketProofBytes+1)}
	ticket, _, err = store.Mint(ctx, oversized)
	if err != nil {
		t.Fatal(err)
	}
	if binding, ok := store.Consume(ctx, ticket); !ok || binding != (Ticket{UserID: 7, ProfileID: "profile-1"}) {
		t.Fatalf("oversized proof consumed as %+v %v", binding, ok)
	}
}
