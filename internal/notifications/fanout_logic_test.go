package notifications

import (
	"context"
	"testing"
	"time"
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

func TestValidEpisodeOrdinals(t *testing.T) {
	cases := []struct {
		season, episode int
		want            bool
	}{
		{0, 0, true},
		{1, 999_999, true},
		{1, 1_000_000, false},
		{-1, 1, false},
		{1, -1, false},
	}
	for _, tc := range cases {
		if got := ValidEpisodeOrdinals(tc.season, tc.episode); got != tc.want {
			t.Errorf("ValidEpisodeOrdinals(%d,%d) = %v, want %v", tc.season, tc.episode, got, tc.want)
		}
	}
}

func TestEvaluateRecipientReasons(t *testing.T) {
	prefs := DefaultPreferences("p1")
	episodeKey := EpisodeKey(2, 5)

	t.Run("favorite matches", func(t *testing.T) {
		flags, ok := EvaluateRecipient(SeriesInterest{Favorite: true}, prefs, episodeKey)
		if !ok || !flags.Favorite || flags.Watchlist || flags.NextUp {
			t.Fatalf("unexpected flags %+v ok=%v", flags, ok)
		}
	})

	t.Run("multiple reasons merge", func(t *testing.T) {
		interest := SeriesInterest{
			Favorite:               true,
			ContinueWatching:       true,
			NextUpCandidate:        true,
			NextExpectedEpisodeKey: intPtr(episodeKey),
		}
		flags, ok := EvaluateRecipient(interest, prefs, episodeKey)
		if !ok || !flags.Favorite || !flags.ContinueWatching || !flags.NextUp {
			t.Fatalf("unexpected flags %+v ok=%v", flags, ok)
		}
	})

	t.Run("next up gated by cursor", func(t *testing.T) {
		interest := SeriesInterest{
			NextUpCandidate:        true,
			NextExpectedEpisodeKey: intPtr(episodeKey + 1),
		}
		if _, ok := EvaluateRecipient(interest, prefs, episodeKey); ok {
			t.Fatal("episode below next_expected must not notify via next_up")
		}
		interest.NextExpectedEpisodeKey = intPtr(episodeKey)
		if _, ok := EvaluateRecipient(interest, prefs, episodeKey); !ok {
			t.Fatal("episode at next_expected must notify via next_up")
		}
	})

	t.Run("last notified suppresses repeats and older keys", func(t *testing.T) {
		interest := SeriesInterest{Favorite: true, LastNotifiedEpisodeKey: intPtr(episodeKey)}
		if _, ok := EvaluateRecipient(interest, prefs, episodeKey); ok {
			t.Fatal("already-notified key must suppress")
		}
		if _, ok := EvaluateRecipient(interest, prefs, episodeKey-1); ok {
			t.Fatal("older key must suppress")
		}
		if _, ok := EvaluateRecipient(interest, prefs, episodeKey+1); !ok {
			t.Fatal("newer key must notify")
		}
	})

	t.Run("preferences are a hard gate", func(t *testing.T) {
		disabled := DefaultPreferences("p1")
		disabled.NotifyFavorites = false
		if _, ok := EvaluateRecipient(SeriesInterest{Favorite: true}, disabled, episodeKey); ok {
			t.Fatal("disabled reason must not produce a delivery")
		}
		killSwitch := DefaultPreferences("p1")
		killSwitch.Enabled = false
		interest := SeriesInterest{Favorite: true, Watchlist: true, ContinueWatching: true}
		if _, ok := EvaluateRecipient(interest, killSwitch, episodeKey); ok {
			t.Fatal("master toggle must suppress everything")
		}
	})
}

func TestApplyBurstCap(t *testing.T) {
	event := func(library int, series string, key int) ReleaseEvent {
		return ReleaseEvent{
			ID:         series + "-" + time.Duration(key).String(),
			LibraryID:  library,
			SeriesID:   series,
			EpisodeKey: key,
		}
	}

	t.Run("caps per series keeping highest keys", func(t *testing.T) {
		events := []ReleaseEvent{
			event(1, "a", 1), event(1, "a", 2), event(1, "a", 3), event(1, "a", 4), event(1, "a", 5),
			event(1, "b", 10),
		}
		fanout, suppressed := ApplyBurstCap(events, 3)
		if len(fanout) != 4 || len(suppressed) != 2 {
			t.Fatalf("got %d fanned out, %d suppressed; want 4/2", len(fanout), len(suppressed))
		}
		for _, ev := range suppressed {
			if ev.SeriesID != "a" || ev.EpisodeKey > 2 {
				t.Fatalf("suppressed wrong event: %+v", ev)
			}
		}
	})

	t.Run("distinct libraries are distinct groups", func(t *testing.T) {
		events := []ReleaseEvent{
			event(1, "a", 1), event(1, "a", 2),
			event(2, "a", 1), event(2, "a", 2),
		}
		fanout, suppressed := ApplyBurstCap(events, 2)
		if len(fanout) != 4 || len(suppressed) != 0 {
			t.Fatalf("got %d/%d; same series in two libraries must not share a cap", len(fanout), len(suppressed))
		}
	})

	t.Run("under cap passes through", func(t *testing.T) {
		events := []ReleaseEvent{event(1, "a", 1), event(1, "b", 2)}
		fanout, suppressed := ApplyBurstCap(events, 3)
		if len(fanout) != 2 || len(suppressed) != 0 {
			t.Fatalf("got %d/%d; want 2/0", len(fanout), len(suppressed))
		}
	})

	t.Run("kept events emit in ascending key order", func(t *testing.T) {
		// Fanout raises last_notified_episode_key as it processes each event
		// inside one transaction; emitting a higher key first would make
		// EvaluateRecipient suppress every remaining event in the group.
		events := []ReleaseEvent{
			event(1, "a", 5), event(1, "a", 3), event(1, "a", 4), event(1, "a", 1),
		}
		fanout, suppressed := ApplyBurstCap(events, 3)
		if len(fanout) != 3 || len(suppressed) != 1 {
			t.Fatalf("got %d/%d; want 3/1", len(fanout), len(suppressed))
		}
		if fanout[0].EpisodeKey != 3 || fanout[1].EpisodeKey != 4 || fanout[2].EpisodeKey != 5 {
			t.Fatalf("want keys [3 4 5] in order, got %+v", fanout)
		}
		if suppressed[0].EpisodeKey != 1 {
			t.Fatalf("want lowest key suppressed, got %+v", suppressed[0])
		}
	})
}

func TestCursorRoundTrip(t *testing.T) {
	cursor := Cursor{CreatedAt: time.Date(2026, 6, 11, 10, 30, 0, 123456789, time.UTC), ID: "01ABC"}
	decoded, err := DecodeCursor(cursor.Encode())
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !decoded.CreatedAt.Equal(cursor.CreatedAt) || decoded.ID != cursor.ID {
		t.Fatalf("round trip mismatch: %+v vs %+v", decoded, cursor)
	}
	if _, err := DecodeCursor("not-a-cursor"); err == nil {
		t.Fatal("garbage cursor must fail to decode")
	}
	if _, err := DecodeCursor(""); err == nil {
		t.Fatal("empty cursor must fail to decode")
	}
}

func TestMemoryTicketStore(t *testing.T) {
	store := NewTicketStore(nil)
	ctx := context.Background()

	ticket, ttl, err := store.Mint(ctx, 7, "profile-1")
	if err != nil || ticket == "" || ttl <= 0 {
		t.Fatalf("mint failed: %q %v %v", ticket, ttl, err)
	}

	userID, profileID, ok := store.Consume(ctx, ticket)
	if !ok || userID != 7 || profileID != "profile-1" {
		t.Fatalf("consume returned %d %q %v", userID, profileID, ok)
	}

	if _, _, ok := store.Consume(ctx, ticket); ok {
		t.Fatal("tickets must be single-use")
	}
	if _, _, ok := store.Consume(ctx, "unknown"); ok {
		t.Fatal("unknown tickets must be rejected")
	}
}
