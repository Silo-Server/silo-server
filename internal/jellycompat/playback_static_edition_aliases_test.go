package jellycompat

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A client that switches versions while keeping its device and client play ID
// gets one static reservation per edition, and each reservation offers both
// editions. Lookups must use the edition each reservation recorded, or a
// report naming one edition matches both and is dropped as ambiguous.
func staticEditionReservations(t *testing.T, now time.Time) (*PlaybackSessionStore, *PlaybackSession, *PlaybackSession) {
	t.Helper()
	clock := now
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return clock })
	sources := []PlaybackMediaSource{{ID: "source-42", FileID: 42}, {ID: "source-43", FileID: 43}}
	reserve := func(fileID int, sourceID string) *PlaybackSession {
		stored, err := store.GetOrCreateStatic(context.Background(), PlaybackSession{
			ID:                  "play-" + sourceID,
			CompatToken:         "token-1",
			ItemID:              "item-1",
			RouteItemID:         "item-1",
			ClientPlaySessionID: "client-play-1",
			ClientDeviceID:      "device-1",
			ClientIP:            "192.0.2.10",
			ClientPeer:          "192.0.2.10",
			MediaSources:        append([]PlaybackMediaSource(nil), sources...),
			SelectedMediaFileID: fileID,
			StaticPlaybackKey:   "static-" + sourceID,
		})
		if err != nil {
			t.Fatalf("GetOrCreateStatic(%s): %v", sourceID, err)
		}
		return stored
	}
	first := reserve(42, "source-42")
	// The other edition is the newer play, which the stream grant would
	// otherwise prefer for either source.
	clock = now.Add(time.Minute)
	second := reserve(43, "source-43")
	if first.ID == second.ID {
		t.Fatal("test setup: both editions share one reservation")
	}
	return store, first, second
}

func TestStaticReservationAliasesResolveToTheReservedEdition(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store, first, second := staticEditionReservations(t, now)

	for _, test := range []struct {
		sourceID string
		want     *PlaybackSession
	}{{"source-42", first}, {"source-43", second}} {
		got, err := store.ResolveDeviceClientPlaySessionID("token-1", "client-play-1", "device-1", "item-1", test.sourceID, false)
		if err != nil || got.ID != test.want.ID {
			t.Fatalf("progress report for %s resolved %v (err %v), want %s", test.sourceID, got, err, test.want.ID)
		}
		if got, ok := store.FindByClientPlaySessionID("token-1", "client-play-1"); ok {
			t.Fatalf("a report without a source still has to stay ambiguous, got %s", got.ID)
		}
		got, ok := store.FindFinalizableByClientPlaySessionID("token-1", "client-play-1", "item-1", test.sourceID)
		if !ok || got.ID != test.want.ID {
			t.Fatalf("finalizable lookup for %s = %v, %v; want %s", test.sourceID, got, ok, test.want.ID)
		}
		session, source, ok := store.FindFinalizableByRoute("token-1", test.sourceID)
		if !ok || session.ID != test.want.ID || source == nil || source.ID != test.sourceID {
			t.Fatalf("source-keyed route lookup for %s = %v, %v, %v; want %s", test.sourceID, session, source, ok, test.want.ID)
		}
	}

	// Stopped reports also search terminal records.
	terminal := *second
	terminal.Terminal = true
	store.Put(terminal)
	got, err := store.ResolveDeviceClientPlaySessionID("token-1", "client-play-1", "device-1", "item-1", "source-43", true)
	if err != nil || got.ID != second.ID {
		t.Fatalf("stop report for the terminal edition resolved %v (err %v), want %s", got, err, second.ID)
	}
}

func TestStaticStreamGrantUsesTheReservedEdition(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store, first, second := staticEditionReservations(t, now)

	for _, test := range []struct {
		sourceID string
		want     *PlaybackSession
	}{{"source-42", first}, {"source-43", second}} {
		got, ok := store.FindStreamGrant("item-1", test.sourceID, "192.0.2.10", "192.0.2.10", time.Hour)
		if !ok || got.ID != test.want.ID {
			t.Fatalf("stream grant for %s = %v, %v; want %s", test.sourceID, got, ok, test.want.ID)
		}
	}
}

func TestNegotiatedPlayStillServesEveryOfferedSource(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	// A PlaybackInfo play records its edition when native playback starts, but
	// the client may switch editions within it.
	store.Put(PlaybackSession{
		ID:                  "negotiated-1",
		CompatToken:         "token-1",
		RouteItemID:         "item-1",
		ClientPlaySessionID: "client-play-1",
		MediaSources:        []PlaybackMediaSource{{ID: "source-42", FileID: 42}, {ID: "source-43", FileID: 43}},
		SelectedMediaFileID: 42,
	})
	for _, sourceID := range []string{"source-42", "source-43"} {
		if got, ok := store.FindFinalizableByClientPlaySessionID("token-1", "client-play-1", "item-1", sourceID); !ok || got.ID != "negotiated-1" {
			t.Fatalf("negotiated play lookup for %s = %v, %v", sourceID, got, ok)
		}
	}
}

// The durable store reloads a compat token's plays from PostgreSQL before it
// matches them, so a fresh API instance must apply the same edition filter.
func TestDurableStaticReservationAliasesResolveToTheReservedEdition(t *testing.T) {
	pool := newCompatTestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	token := "edition-token-" + suffix
	writer := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	sources := []PlaybackMediaSource{{ID: "source-42", FileID: 42}, {ID: "source-43", FileID: 43}}
	ids := map[string]string{}
	for _, edition := range []struct {
		fileID   int
		sourceID string
	}{{42, "source-42"}, {43, "source-43"}} {
		stored, err := writer.GetOrCreateStatic(ctx, PlaybackSession{
			ID:                  "edition-" + edition.sourceID + "-" + suffix,
			CompatToken:         token,
			ItemID:              "item-1",
			RouteItemID:         "item-1",
			ClientPlaySessionID: "client-play-1",
			ClientDeviceID:      "device-1",
			MediaSources:        append([]PlaybackMediaSource(nil), sources...),
			SelectedMediaFileID: edition.fileID,
			StaticPlaybackKey:   "edition-" + edition.sourceID + "-" + suffix,
		})
		if err != nil {
			t.Fatalf("GetOrCreateStatic(%s): %v", edition.sourceID, err)
		}
		ids[edition.sourceID] = stored.ID
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = pool.Exec(ctx, `DELETE FROM jellycompat_playback_sessions WHERE id = $1`, id)
		}
	})

	reader := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	for sourceID, want := range ids {
		got, err := reader.ResolveDeviceClientPlaySessionID(token, "client-play-1", "device-1", "item-1", sourceID, true)
		if err != nil || got.ID != want {
			t.Fatalf("durable report for %s resolved %v (err %v), want %s", sourceID, got, err, want)
		}
	}

	// ID-less reports are matched from a SQL identity projection, which must
	// carry each reservation's edition and its sources' files.
	for _, id := range ids {
		if err := writer.Update(id, func(session *PlaybackSession) error {
			session.UpstreamSessionID = "upstream-" + session.ID
			return nil
		}); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	fresh := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	for sourceID, want := range ids {
		got, err := fresh.FindUnidentifiedPlayback(token, "item-1", sourceID, "device-1")
		if err != nil || got == nil || got.ID != want {
			t.Fatalf("durable ID-less report for %s = %v (err %v), want %s", sourceID, got, err, want)
		}
	}
}

// Direct players that omit PlaySessionId are matched by route and source. Each
// reservation offers both editions, so only the recorded edition may decide.
func TestUnidentifiedPlaybackUsesTheReservedEdition(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	store, first, second := staticEditionReservations(t, now)
	for _, session := range []*PlaybackSession{first, second} {
		started := *session
		started.UpstreamSessionID = "upstream-" + session.ID
		store.Put(started)
	}
	for _, test := range []struct {
		sourceID string
		want     *PlaybackSession
	}{{"source-42", first}, {"source-43", second}} {
		got, err := store.FindUnidentifiedPlayback("token-1", "item-1", test.sourceID, "device-1")
		if err != nil || got == nil || got.ID != test.want.ID {
			t.Fatalf("ID-less report for %s = %v (err %v), want %s", test.sourceID, got, err, test.want.ID)
		}
	}
	if _, err := store.FindUnidentifiedPlayback("token-1", "item-1", "", "device-1"); err == nil {
		t.Fatal("an ID-less report naming no source should stay ambiguous")
	}
}
