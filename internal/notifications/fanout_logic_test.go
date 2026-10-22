package notifications

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

// notifiedInBatch runs a claimed batch of one series' episode keys through
// fanout's order and eligibility for one profile, raising last-notified the
// way fanout does, and returns the keys that notified.
func notifiedInBatch(interest SeriesInterest, prefs Preferences, keys ...int) []int {
	events := make([]ReleaseEvent, len(keys))
	for i, key := range keys {
		events[i] = ReleaseEvent{LibraryID: 1, SeriesID: "s", EpisodeKey: key}
	}
	SortFanoutEvents(events)
	var notified []int
	for _, event := range events {
		if _, ok := EvaluateRecipient(interest, prefs, event.EpisodeKey); ok {
			notified = append(notified, event.EpisodeKey)
			interest.LastNotifiedEpisodeKey = intPtr(event.EpisodeKey)
		}
	}
	return notified
}

func TestEvaluateRecipientNotifiesOnlyTheNextEpisode(t *testing.T) {
	e := func(season, episode int) int { return EpisodeKey(season, episode) }
	waiting := func(next int, open bool) SeriesInterest {
		return SeriesInterest{NextUpCandidate: true, LastCompletedEpisodeKey: intPtr(next - 1), NextExpectedEpisodeKey: intPtr(next), NextExpectedOpen: open}
	}
	off := DefaultPreferences(1, "p")
	off.NotifyNextUp = false
	master := DefaultPreferences(1, "p")
	master.Enabled = false

	for _, tc := range []struct {
		name     string
		interest SeriesInterest
		prefs    *Preferences
		batch    []int
		want     []int
	}{
		{name: "the next episode notifies", interest: waiting(e(1, 6), false), batch: []int{e(1, 6)}, want: []int{e(1, 6)}},
		{name: "a batch drop notifies only the next", interest: waiting(e(1, 6), false), batch: []int{e(1, 8), e(1, 6), e(1, 7)}, want: []int{e(1, 6)}},
		{name: "a later episode landing first does not notify", interest: waiting(e(1, 6), false), batch: []int{e(1, 7)}},
		{name: "a profile behind the release does not hear about it", interest: waiting(e(1, 3), false), batch: []int{e(1, 6), e(1, 7), e(1, 8)}},
		{name: "an open cursor takes the first arrival past it", interest: waiting(e(1, 11), true), batch: []int{e(2, 2), e(2, 1)}, want: []int{e(2, 1)}},
		{name: "an open cursor ignores episodes before it", interest: waiting(e(1, 11), true), batch: []int{e(1, 3)}},
		{name: "one notification per Next Up position", interest: func() SeriesInterest {
			i := waiting(e(2, 1), true)
			i.LastNotifiedEpisodeKey = intPtr(e(2, 1))
			return i
		}(), batch: []int{e(2, 2)}},
		{name: "a notification before the cursor does not block it", interest: func() SeriesInterest {
			i := waiting(e(1, 7), false)
			i.LastNotifiedEpisodeKey = intPtr(e(1, 6))
			return i
		}(), batch: []int{e(1, 7)}, want: []int{e(1, 7)}},
		{name: "a series that is not a Next Up candidate is silent", interest: SeriesInterest{LastCompletedEpisodeKey: intPtr(e(1, 5)), NextExpectedEpisodeKey: intPtr(e(1, 6))}, batch: []int{e(1, 6)}},
		{name: "no cursor, no notification", interest: SeriesInterest{NextUpCandidate: true}, batch: []int{e(1, 1)}},
		{name: "the Next Up switch is a hard gate", interest: waiting(e(1, 6), false), prefs: &off, batch: []int{e(1, 6)}},
		{name: "the master switch is a hard gate", interest: waiting(e(1, 6), false), prefs: &master, batch: []int{e(1, 6)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefs := DefaultPreferences(1, "p")
			if tc.prefs != nil {
				prefs = *tc.prefs
			}
			got := notifiedInBatch(tc.interest, prefs, tc.batch...)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("notified %v, want %v", got, tc.want)
			}
		})
	}
}

// nextExpectedEpisode places the Next Up cursor the way Home picks its next
// episode, except that an episode the library has no file for yet still
// holds the cursor.
func TestNextExpectedEpisode(t *testing.T) {
	e := func(season, episode int) int { return EpisodeKey(season, episode) }
	catalog := func(keys ...int) map[string]int {
		out := make(map[string]int, len(keys))
		for _, key := range keys {
			out[strconv.Itoa(key)] = key
		}
		return out
	}
	ids := func(keys ...int) []string {
		out := make([]string, len(keys))
		for i, key := range keys {
			out[i] = strconv.Itoa(key)
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		anchor   int
		episodes map[string]int
		started  []string
		want     int
		open     bool
	}{
		{name: "the following episode", anchor: e(1, 5), episodes: catalog(e(1, 4), e(1, 5), e(1, 6), e(1, 7)), started: ids(e(1, 4), e(1, 5)), want: e(1, 6)},
		{name: "a gap in the numbering", anchor: e(1, 5), episodes: catalog(e(1, 5), e(1, 7)), started: ids(e(1, 5)), want: e(1, 7)},
		{name: "the next season's premiere", anchor: e(1, 10), episodes: catalog(e(1, 10), e(2, 1), e(2, 2)), started: ids(e(1, 10)), want: e(2, 1)},
		{name: "a finale before the next season is known", anchor: e(1, 10), episodes: catalog(e(1, 9), e(1, 10)), started: ids(e(1, 10)), want: e(1, 11), open: true},
		{name: "a rewatch skips what was already watched", anchor: e(1, 2), episodes: catalog(e(1, 1), e(1, 2), e(1, 3), e(1, 4), e(1, 5)), started: ids(e(1, 1), e(1, 2), e(1, 3), e(1, 4)), want: e(1, 5)},
		{name: "a rewatch of a series watched through is open past its end", anchor: e(1, 2), episodes: catalog(e(1, 1), e(1, 2), e(1, 3)), started: ids(e(1, 1), e(1, 2), e(1, 3)), want: e(1, 4), open: true},
		{name: "an episode watched out of order stays watched", anchor: e(1, 5), episodes: catalog(e(1, 5), e(1, 6), e(1, 7), e(1, 8)), started: ids(e(1, 5), e(1, 7)), want: e(1, 6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, open := nextExpectedEpisode(tc.anchor, tc.episodes, tc.started)
			if got != tc.want || open != tc.open {
				t.Fatalf("nextExpectedEpisode = %d open=%v, want %d open=%v", got, open, tc.want, tc.open)
			}
		})
	}
	// Two catalog rows for one episode: watching either counts.
	episodes := map[string]int{"a": e(1, 6), "b": e(1, 6), "c": e(1, 7)}
	if got, open := nextExpectedEpisode(e(1, 5), episodes, []string{"a"}); got != e(1, 7) || open {
		t.Fatalf("duplicate rows: got %d open=%v, want %d", got, open, e(1, 7))
	}
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
