package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func compatTombstoneWriteFailure() error {
	return errors.Join(playback.ErrSessionGenerationTombstoneUnavailable, errors.New("database unavailable"))
}

func TestDeliberateCleanupPreservesAllStateWhenTombstoneWriteFails(t *testing.T) {
	stopErr := compatTombstoneWriteFailure()
	mgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}},
		stopErr:  stopErr,
	}
	h, store := newActiveEncodingsHandler(mgr)
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"upstream-1": {SessionID: "upstream-1"},
	}}
	h.RecipeNodeStore = recipeStore
	dead := &playback.TranscodeSession{}
	h.tm.RegisterTranscodeSession("upstream-1", dead)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest(http.MethodDelete, "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "tok")
	recorder := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want retryable 503", recorder.Code, recorder.Body.String())
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("tombstone failure hid or deleted the compat routing session")
	}
	if _, err := mgr.GetSession("upstream-1"); err != nil {
		t.Fatalf("tombstone failure removed live native session: %v", err)
	}
	if got := h.tm.GetTranscodeSession("upstream-1"); got != dead {
		t.Fatalf("tombstone failure tore down transcode: got %p, want %p", got, dead)
	}
	if _, ok := recipeStore.Get("upstream-1"); !ok {
		t.Fatal("tombstone failure deleted node recipe")
	}
}

func TestMethodSwitchPreservesOldMethodWhenTombstoneWriteFails(t *testing.T) {
	stopErr := compatTombstoneWriteFailure()
	mgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{"upstream-old": {
			ID: "upstream-old", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		}},
		stopErr: stopErr,
	}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"upstream-old": {SessionID: "upstream-old"},
	}}
	h.RecipeNodeStore = recipeStore
	oldTranscode := &playback.TranscodeSession{}
	h.tm.RegisterTranscodeSession("upstream-old", oldTranscode)
	store.Put(PlaybackSession{
		ID:                 "ps-1",
		CompatToken:        "tok",
		ItemID:             "item-1",
		MediaSources:       []PlaybackMediaSource{{FileID: 42}},
		UpstreamSessionID:  "upstream-old",
		UpstreamPlayMethod: "direct",
	})

	_, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "tok", StreamAppUserID: 7, ProfileID: "profile-1"},
		"ps-1",
		PlaybackMediaSource{FileID: 42},
		"transcode",
	)
	if !errors.Is(err, playback.ErrSessionGenerationTombstoneUnavailable) {
		t.Fatalf("method switch error = %v, want tombstone unavailable", err)
	}
	if mgr.startCalls != 0 {
		t.Fatalf("method switch started %d replacement sessions after failed tombstone", mgr.startCalls)
	}
	current, ok := store.Get("ps-1")
	if !ok || current.UpstreamSessionID != "upstream-old" || current.UpstreamPlayMethod != "direct" {
		t.Fatalf("compat method state changed after failed tombstone: %+v", current)
	}
	if got := h.tm.GetTranscodeSession("upstream-old"); got != oldTranscode {
		t.Fatal("method switch tore down old transcode after failed tombstone")
	}
	if _, ok := recipeStore.Get("upstream-old"); !ok {
		t.Fatal("method switch deleted old recipe after failed tombstone")
	}
	if len(scrobbler.calls) != 0 {
		t.Fatalf("method switch scrobbled before failed tombstone persistence: %+v", scrobbler.calls)
	}
}

func TestDeadFFmpegCleanupPreservesStateWhenTombstoneWriteFails(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail-ffmpeg.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
		t.Fatalf("write failing ffmpeg: %v", err)
	}
	dead, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "ignored", OutputDir: t.TempDir(), FFmpegPath: script,
	})
	if err != nil {
		t.Fatalf("start failing ffmpeg: %v", err)
	}

	stopErr := compatTombstoneWriteFailure()
	mgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}},
		stopErr:  stopErr,
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok", ItemID: "item-1",
	})
	h := NewPlaybackHandler(nil, nil, nil, nil, store, mgr, nil, nil)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	h.tm.RegisterTranscodeSession("upstream-1", dead)

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	seenGeneration := make(chan string, 1)
	onCrash := h.tm.OnFFmpegCrashIdentity
	h.tm.OnFFmpegCrashIdentity = func(ctx context.Context, id, generation string, crashed *playback.TranscodeSession) {
		onCrash(ctx, id, generation, crashed)
		seenGeneration <- generation
	}
	h.monitorLocalTranscodeExit("upstream-1", "", dead)
	select {
	case generation := <-seenGeneration:
		if generation != "" {
			t.Fatalf("legacy crash generation = %q, want empty", generation)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for legacy empty-generation crash dispatch")
	}

	if got := h.tm.GetTranscodeSession("upstream-1"); got != dead {
		t.Fatalf("failed crash tombstone tore down transcode: got %p, want %p", got, dead)
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("failed crash tombstone hid compat routing state")
	}
	if _, err := mgr.GetSession("upstream-1"); err != nil {
		t.Fatalf("failed crash tombstone removed native session: %v", err)
	}
	if len(scrobbler.calls) != 0 {
		t.Fatalf("failed crash tombstone emitted compat scrobble: %+v", scrobbler.calls)
	}
	if !strings.Contains(logs.String(), "dead jellycompat ffmpeg cleanup remains retryable") {
		t.Fatalf("missing retryable cleanup warning in logs: %s", logs.String())
	}
}

func TestConcurrentReplacementLoserRemainsRetryableWhenTombstoneWriteFails(t *testing.T) {
	stopErr := errors.New("unexpected tombstone persistence failure")
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "ps-1", CompatToken: "tok"})
	mgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{},
		stopErr:  stopErr,
	}
	mgr.startHook = func(_ *playback.Session) {
		mgr.sessions["upstream-winner"] = &playback.Session{ID: "upstream-winner"}
		if err := store.Update("ps-1", func(current *PlaybackSession) error {
			current.UpstreamSessionID = "upstream-winner"
			current.UpstreamPlayMethod = "direct"
			return nil
		}); err != nil {
			t.Fatalf("install concurrent winner: %v", err)
		}
	}
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		tm:            playback.NewTranscodeManager(),
	}

	_, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "tok", StreamAppUserID: 7, ProfileID: "profile-1"},
		"ps-1",
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if !errors.Is(err, errCompatSessionStopUnavailable) {
		t.Fatalf("replacement-loser error = %v, want retryable compat stop failure", err)
	}
	if _, err := mgr.GetSession("upstream-started"); err != nil {
		t.Fatalf("failed loser tombstone removed live loser session: %v", err)
	}
	current, ok := store.Get("ps-1")
	if !ok || current.UpstreamSessionID != "upstream-winner" || current.UpstreamPlayMethod != "direct" {
		t.Fatalf("failed loser cleanup disturbed concurrent winner: %+v", current)
	}
}

func TestStaleGenerationCleanupPreservesInstalledReplacementState(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	startedAt := time.Now().UTC()
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: startedAt,
	}); err != nil || got == nil {
		t.Fatalf("register replacement generation: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "shared-upstream",
		UpstreamSessionGeneration: "g2",
		UpstreamStartedAt:         startedAt,
		UpstreamPlayMethod:        "transcode",
		TranscodeStarted:          true,
		Recipe:                    &playback.RecipeCard{SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 42},
	})
	tm := playback.NewTranscodeManager()
	tm.Sessions = mgr
	replacementTranscode := &playback.TranscodeSession{}
	tm.RegisterTranscodeSession("shared-upstream", replacementTranscode)
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"shared-upstream": {SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 42},
	}}
	h := &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: tm, RecipeNodeStore: recipeStore}
	stale := &PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "shared-upstream",
		UpstreamSessionGeneration: "g1",
		UpstreamStartedAt:         startedAt.Add(-time.Minute),
		UpstreamPlayMethod:        "direct",
	}

	if err := h.cleanupPlaySession(context.Background(), stale, nil, ""); err != nil {
		t.Fatalf("stale cleanup: %v", err)
	}
	if live, err := mgr.GetSession("shared-upstream"); err != nil || live.Generation != "g2" {
		t.Fatalf("replacement native session changed: live=%+v err=%v", live, err)
	}
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g2" || got.UpstreamPlayMethod != "transcode" || !got.TranscodeStarted || got.Recipe == nil || got.Recipe.MediaFileID != 42 {
		t.Fatalf("replacement compat state changed: ok=%v got=%+v", ok, got)
	}
	if tm.GetTranscodeSession("shared-upstream") != replacementTranscode {
		t.Fatal("stale cleanup closed replacement transcode")
	}
	if card, ok := recipeStore.Get("shared-upstream"); !ok || card.SessionGeneration != "g2" || card.MediaFileID != 42 {
		t.Fatalf("stale cleanup changed replacement recipe: ok=%v card=%+v", ok, card)
	}
}

func TestDelayedOldGenerationCrashPreservesReplacementState(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	startedAt := time.Now().UTC()
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: startedAt,
	}); err != nil || got == nil {
		t.Fatalf("register replacement generation: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                        "play-1",
		CompatToken:               "owner",
		UpstreamSessionID:         "shared-upstream",
		UpstreamSessionGeneration: "g2",
		UpstreamStartedAt:         startedAt,
		UpstreamPlayMethod:        "transcode",
		TranscodeStarted:          true,
		Recipe:                    &playback.RecipeCard{SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 42},
	})
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"shared-upstream": {SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 42},
	}}
	h := NewPlaybackHandler(nil, nil, nil, nil, store, mgr, nil, nil)
	h.RecipeNodeStore = recipeStore
	deadG1 := &playback.TranscodeSession{}
	// The public-ID slot still contains the exited G1 process while G2 native and
	// compat state are already installed. This is the delayed-callback window in
	// which reloading identity by ID would incorrectly target G2.
	h.tm.RegisterTranscodeSession("shared-upstream", deadG1)

	h.tm.OnFFmpegCrashIdentity(context.Background(), "shared-upstream", "g1", deadG1)

	if live, err := mgr.GetSession("shared-upstream"); err != nil || live.Generation != "g2" {
		t.Fatalf("delayed G1 crash changed G2 native session: live=%+v err=%v", live, err)
	}
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g2" || got.Recipe == nil || got.Recipe.SessionGeneration != "g2" {
		t.Fatalf("delayed G1 crash changed G2 compat state: ok=%v got=%+v", ok, got)
	}
	if h.tm.GetTranscodeSession("shared-upstream") != deadG1 {
		t.Fatal("delayed G1 crash closed G2 transcode")
	}
	if card, ok := recipeStore.Get("shared-upstream"); !ok || card.SessionGeneration != "g2" {
		t.Fatalf("delayed G1 crash changed G2 recipe: ok=%v card=%+v", ok, card)
	}
}
