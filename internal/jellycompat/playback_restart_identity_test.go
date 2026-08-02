package jellycompat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type failOnceStartSessionManager struct {
	*playback.SessionManager
	fail bool
}

type staleFirstPlaybackGetStore struct {
	*PlaybackSessionStore
	stale PlaybackSession
	once  bool
}

func (s *staleFirstPlaybackGetStore) Get(id string) (*PlaybackSession, bool) {
	if !s.once && id == s.stale.ID {
		s.once = true
		copy := s.stale
		return &copy, true
	}
	return s.PlaybackSessionStore.Get(id)
}

func (m *failOnceStartSessionManager) StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	return m.StartSessionWithContext(context.Background(), userID, profileID, fileID, method, transcodeAudio)
}

func (m *failOnceStartSessionManager) StartSessionWithContext(ctx context.Context, userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	if m.fail {
		m.fail = false
		return nil, errors.New("injected replacement start failure")
	}
	return m.SessionManager.StartSessionWithContext(ctx, userID, profileID, fileID, method, transcodeAudio)
}

func newRestartIdentityHandler(store CompatPlaybackStore, mgr *playback.SessionManager) *PlaybackHandler {
	tm := playback.NewTranscodeManager()
	tm.Sessions = mgr
	return &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: tm}
}

func TestStoppedFirstRequestAfterRestartUsesPersistedIdentity(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	startedAt := time.Now().UTC().Add(-time.Minute)
	playSession := PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "upstream-1",
		UpstreamSessionGeneration: "generation-1",
		UpstreamStartedAt:         startedAt,
		UpstreamPlayMethod:        "direct",
	}
	store.Put(playSession)
	h := newRestartIdentityHandler(store, playback.NewSessionManager(0, 0))

	if err := h.teardownPlaySession(context.Background(), &playSession, nil, nil); err != nil {
		t.Fatalf("Stopped teardown after restart: %v", err)
	}
	if _, ok := store.GetFinalizable("play-1", "owner"); ok {
		t.Fatal("Stopped after restart retained the exact compat row")
	}
	if got := h.sessionMgr.(*playback.SessionManager).RegisterReconstructed(&playback.Session{
		ID: "upstream-1", Generation: "generation-1", StartedAt: startedAt,
	}); got == nil || got.Generation == "generation-1" {
		t.Fatalf("ended generation reconstructed after Stopped: %+v", got)
	}
}

func TestMethodSwitchBeforeReconstructionUsesPersistedIdentity(t *testing.T) {
	tests := []struct {
		name      string
		oldMethod string
		newMethod string
	}{
		{name: "to direct", oldMethod: "remux", newMethod: "direct"},
		{name: "to remux", oldMethod: "direct", newMethod: "remux"},
		{name: "to transcode", oldMethod: "direct", newMethod: "transcode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID:                        "play-1",
				CompatToken:               "owner",
				UpstreamSessionID:         "upstream-old",
				UpstreamSessionGeneration: "generation-old",
				UpstreamStartedAt:         time.Now().UTC().Add(-time.Minute),
				UpstreamPlayMethod:        tc.oldMethod,
				MediaSources:              []PlaybackMediaSource{{FileID: 42, TranscodeAudio: true}},
			})
			mgr := playback.NewSessionManager(0, 0)
			h := newRestartIdentityHandler(store, mgr)

			got, err := h.ensureUpstreamPlayback(
				context.Background(),
				&Session{Token: "owner", StreamAppUserID: 7, ProfileID: "profile-1"},
				"play-1",
				PlaybackMediaSource{FileID: 42, TranscodeAudio: true},
				tc.newMethod,
			)
			if err != nil {
				t.Fatalf("method switch after restart: %v", err)
			}
			if got.UpstreamSessionID == "upstream-old" || got.UpstreamSessionGeneration == "" || got.UpstreamPlayMethod != tc.newMethod || got.UpstreamStartedAt.IsZero() {
				t.Fatalf("replacement identity was not installed together: %+v", got)
			}
			if reconstructed := mgr.RegisterReconstructed(&playback.Session{ID: "upstream-old", Generation: "generation-old"}); reconstructed == nil || reconstructed.Generation == "generation-old" {
				t.Fatalf("old generation reconstructed after method switch: %+v", reconstructed)
			}
		})
	}
}

func TestTombstoneDrivenReconstructionRotatesPersistedGeneration(t *testing.T) {
	const oldGeneration = "generation-old"
	mgr := playback.NewSessionManager(0, 0)
	if err := mgr.TerminateSessionGeneration(context.Background(), "upstream-1", oldGeneration, func() error { return nil }); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "upstream-1",
		UpstreamSessionGeneration: oldGeneration,
		UpstreamStartedAt:         time.Now().UTC().Add(-time.Minute),
		UpstreamPlayMethod:        "direct",
		MediaSources:              []PlaybackMediaSource{{FileID: 42}},
	})
	h := newRestartIdentityHandler(store, mgr)

	got, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "owner", StreamAppUserID: 7, ProfileID: "profile-1"},
		"play-1",
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if err != nil {
		t.Fatalf("reconstruct tombstoned generation: %v", err)
	}
	if got.UpstreamSessionID != "upstream-1" || got.UpstreamSessionGeneration == "" || got.UpstreamSessionGeneration == oldGeneration || got.UpstreamStartedAt.IsZero() {
		t.Fatalf("rotated reconstruction identity not persisted: %+v", got)
	}
}

func TestMethodSwitchRetriesAfterOldTombstoneAndReplacementStartFailure(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "upstream-old",
		UpstreamSessionGeneration: "generation-old",
		UpstreamStartedAt:         time.Now().UTC().Add(-time.Minute),
		UpstreamPlayMethod:        "direct",
		MediaSources:              []PlaybackMediaSource{{FileID: 42}},
	})
	mgr := &failOnceStartSessionManager{SessionManager: playback.NewSessionManager(0, 0), fail: true}
	tm := playback.NewTranscodeManager()
	tm.Sessions = mgr.SessionManager
	h := &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: tm}

	_, firstErr := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "owner", StreamAppUserID: 7, ProfileID: "profile-1"},
		"play-1",
		PlaybackMediaSource{FileID: 42},
		"remux",
	)
	if firstErr == nil {
		t.Fatal("first method switch unexpectedly succeeded")
	}
	stillOld, ok := store.Get("play-1")
	if !ok || stillOld.UpstreamSessionGeneration != "generation-old" || stillOld.UpstreamPlayMethod != "direct" {
		t.Fatalf("failed replacement changed old compat identity: %+v", stillOld)
	}

	got, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "owner", StreamAppUserID: 7, ProfileID: "profile-1"},
		"play-1",
		PlaybackMediaSource{FileID: 42},
		"remux",
	)
	if err != nil {
		t.Fatalf("retry method switch: %v", err)
	}
	if got.UpstreamSessionGeneration == "" || got.UpstreamSessionGeneration == "generation-old" || got.UpstreamPlayMethod != "remux" {
		t.Fatalf("retry did not install replacement identity: %+v", got)
	}
}

func TestEnsureUpstreamStaleGenerationDoesNotCloseReplacementTranscode(t *testing.T) {
	base := NewPlaybackSessionStore(time.Hour, nil)
	startedAt := time.Now().UTC()
	base.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "shared-upstream",
		UpstreamSessionGeneration: "g2",
		UpstreamStartedAt:         startedAt,
		UpstreamPlayMethod:        "direct",
		TranscodeStarted:          true,
	})
	store := &staleFirstPlaybackGetStore{
		PlaybackSessionStore: base,
		stale: PlaybackSession{
			ID:                        "play-1",
			CompatToken:               "owner",
			UpstreamSessionID:         "shared-upstream",
			UpstreamSessionGeneration: "g1",
			UpstreamStartedAt:         startedAt.Add(-time.Minute),
			UpstreamPlayMethod:        "direct",
		},
	}
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
	tm := playback.NewTranscodeManager()
	replacement := &playback.TranscodeSession{}
	tm.RegisterTranscodeSession("shared-upstream", replacement)
	h := &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: tm}

	_, _ = h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "owner", StreamAppUserID: 7, ProfileID: "profile-1"},
		"play-1",
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if got := tm.GetTranscodeSession("shared-upstream"); got != replacement {
		t.Fatalf("stale G1 ensure closed G2 transcode: got=%p want=%p", got, replacement)
	}
}
