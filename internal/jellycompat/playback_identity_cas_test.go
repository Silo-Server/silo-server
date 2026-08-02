package jellycompat

import (
	"testing"
	"time"
)

func TestPlaybackSessionStoreCompareAndSwapUpstreamUsesFullIdentity(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	startedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	store.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "upstream",
		UpstreamSessionGeneration: "g2",
		UpstreamStartedAt:         startedAt,
		UpstreamPlayMethod:        "remux",
		TranscodeStarted:          true,
	})

	called := false
	_, matched, err := store.CompareAndSwapUpstream(
		"play-1",
		UpstreamSessionIdentity{ID: "upstream", Generation: "g1"},
		func(*PlaybackSession) (bool, error) {
			called = true
			return true, nil
		},
	)
	if err != nil {
		t.Fatalf("stale compare-and-swap: %v", err)
	}
	if matched || called {
		t.Fatalf("stale generation matched=%v callbackCalled=%v", matched, called)
	}
	got, ok := store.Get("play-1")
	if !ok {
		t.Fatal("stale generation deleted the replacement")
	}
	if got.UpstreamSessionGeneration != "g2" || got.UpstreamPlayMethod != "remux" || !got.TranscodeStarted || !got.UpstreamStartedAt.Equal(startedAt) {
		t.Fatalf("stale generation changed replacement state: %+v", got)
	}
}

func TestPlaybackSessionStoreCompareAndSwapUpstreamWritesIdentityTogether(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "owner"})
	startedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.FixedZone("EDT", -4*60*60))

	updated, matched, err := store.CompareAndSwapUpstream(
		"play-1",
		UpstreamSessionIdentity{},
		func(session *PlaybackSession) (bool, error) {
			session.UpstreamSessionID = "upstream"
			session.UpstreamSessionGeneration = "g1"
			session.UpstreamStartedAt = startedAt
			session.UpstreamPlayMethod = "direct"
			return false, nil
		},
	)
	if err != nil || !matched {
		t.Fatalf("compare-and-swap matched=%v err=%v", matched, err)
	}
	if updated == nil || updated.UpstreamSessionID != "upstream" || updated.UpstreamSessionGeneration != "g1" ||
		updated.UpstreamPlayMethod != "direct" || !updated.UpstreamStartedAt.Equal(startedAt.UTC()) || updated.UpstreamStartedAt.Location() != time.UTC {
		t.Fatalf("identity was not written together in UTC: %+v", updated)
	}
}
