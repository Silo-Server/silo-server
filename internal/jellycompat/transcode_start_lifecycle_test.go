package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

var errInjectedTranscodeCommit = errors.New("injected transcode commit failure")

const (
	testGeneration1 = "32a4e124-9df7-4cfa-be49-e8e503316714"
	testGeneration2 = "d24a82d2-2bc2-44a4-96c6-a86671d508d7"
)

type blockingFailureCASStore struct {
	CompatPlaybackStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingFailureCASStore) CompareAndSwapUpstream(
	id string,
	expected UpstreamSessionIdentity,
	fn UpstreamSessionMutation,
) (*PlaybackSession, bool, error) {
	close(s.entered)
	<-s.release
	return nil, false, errInjectedTranscodeCommit
}

type generationInstallResult struct {
	session *playback.Session
	err     error
}

func installReplacementGeneration(ctx context.Context, mgr *playback.SessionManager, done chan<- generationInstallResult) {
	if err := mgr.TerminateSessionGeneration(ctx, "shared-upstream", testGeneration1, func() error { return nil }); err != nil {
		done <- generationInstallResult{err: err}
		return
	}
	session, err := mgr.RegisterReconstructedChecked(ctx, &playback.Session{
		ID: "shared-upstream", Generation: testGeneration2, StartedAt: time.Now().UTC(),
	})
	done <- generationInstallResult{session: session, err: err}
}

func seededTranscodeStartState(t *testing.T) (*playback.SessionManager, *PlaybackSessionStore, PlaybackMediaSource) {
	t.Helper()
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "shared-upstream", Generation: testGeneration1, StartedAt: time.Now().UTC(), UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
	}); err != nil || got == nil {
		t.Fatalf("register G1: got=%+v err=%v", got, err)
	}
	base := NewPlaybackSessionStore(time.Hour, nil)
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	base.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: testGeneration1,
		UpstreamPlayMethod: "transcode", MediaSources: []PlaybackMediaSource{source},
	})
	return mgr, base, source
}

func assertReplacementInstallBlocked(t *testing.T, done <-chan generationInstallResult) *generationInstallResult {
	t.Helper()
	select {
	case result := <-done:
		return &result
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

func TestLocalTranscodeSetupBlocksReplacementUntilRollback(t *testing.T) {
	mgr, base, source := seededTranscodeStartState(t)
	store := &blockingFailureCASStore{CompatPlaybackStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	marker := filepath.Join(t.TempDir(), "started")
	ffmpeg := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ntouch "+marker+"\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    ffmpeg,
		tm:            playback.NewTranscodeManager(),
	}
	startDone := make(chan error, 1)
	go func() {
		_, err := h.ensureTranscodeSession(t.Context(), "play-1", "shared-upstream", testGeneration1, source)
		startDone <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("local G1 never reached commit after process start")
	}
	if h.tm.GetTranscodeSession("shared-upstream") == nil {
		t.Fatal("local G1 process was not registered before commit pause")
	}

	g2Done := make(chan generationInstallResult, 1)
	go installReplacementGeneration(t.Context(), mgr, g2Done)
	early := assertReplacementInstallBlocked(t, g2Done)
	close(store.release)
	startErr := <-startDone
	if early != nil {
		t.Fatalf("G2 installed before G1 rollback: session=%+v err=%v", early.session, early.err)
	}
	result := <-g2Done
	if result.err != nil || result.session == nil || result.session.Generation != testGeneration2 {
		t.Fatalf("install G2 after rollback: session=%+v err=%v", result.session, result.err)
	}
	if !errors.Is(startErr, errInjectedTranscodeCommit) {
		t.Fatalf("local G1 setup error = %v, want injected commit failure", startErr)
	}
	if h.tm.GetTranscodeSession("shared-upstream") != nil {
		t.Fatal("G1 process survived rollback or was adopted by G2")
	}
}

func TestRemoteTranscodeSetupBlocksReplacementUntilRollback(t *testing.T) {
	mgr, base, source := seededTranscodeStartState(t)
	store := &blockingFailureCASStore{CompatPlaybackStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	var posts atomic.Int32
	var deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer node.Close()
	h := &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: playback.NewTranscodeManager(), JWTSecret: "secret"}
	startDone := make(chan error, 1)
	go func() {
		startDone <- h.startRemoteTranscode(
			t.Context(), "play-1", "shared-upstream", testGeneration1, source,
			&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL,
		)
	}()
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("remote G1 never reached commit after POST")
	}
	if posts.Load() != 1 {
		t.Fatalf("remote starts = %d, want 1 before commit pause", posts.Load())
	}

	g2Done := make(chan generationInstallResult, 1)
	go installReplacementGeneration(t.Context(), mgr, g2Done)
	early := assertReplacementInstallBlocked(t, g2Done)
	close(store.release)
	startErr := <-startDone
	if early != nil {
		t.Fatalf("G2 installed before remote G1 rollback: session=%+v err=%v", early.session, early.err)
	}
	result := <-g2Done
	if result.err != nil || result.session == nil || result.session.Generation != testGeneration2 {
		t.Fatalf("install G2 after remote rollback: session=%+v err=%v", result.session, result.err)
	}
	if !errors.Is(startErr, errInjectedTranscodeCommit) {
		t.Fatalf("remote G1 setup error = %v, want injected commit failure", startErr)
	}
	if posts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("remote process lifecycle posts=%d deletes=%d, want 1/1", posts.Load(), deletes.Load())
	}
}

func TestStaleTranscodeSetupDoesNotStartProcess(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "shared-upstream", Generation: testGeneration2, StartedAt: time.Now().UTC(),
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	base := NewPlaybackSessionStore(time.Hour, nil)
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	base.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: testGeneration2})

	t.Run("local", func(t *testing.T) {
		h := &PlaybackHandler{
			playbackStore: base, sessionMgr: mgr,
			fileResolver: testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}},
			TranscodeDir: t.TempDir(), FFmpegPath: filepath.Join(t.TempDir(), "must-not-execute"), tm: playback.NewTranscodeManager(),
		}
		_, err := h.ensureTranscodeSession(t.Context(), "play-1", "shared-upstream", testGeneration1, source)
		if !errors.Is(err, playback.ErrSessionSuperseded) {
			t.Fatalf("stale local setup = %v, want superseded", err)
		}
	})

	t.Run("remote", func(t *testing.T) {
		var posts atomic.Int32
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		}))
		defer node.Close()
		h := &PlaybackHandler{playbackStore: base, sessionMgr: mgr, tm: playback.NewTranscodeManager(), JWTSecret: "secret"}
		err := h.startRemoteTranscode(
			t.Context(), "play-1", "shared-upstream", testGeneration1, source,
			&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL,
		)
		if !errors.Is(err, playback.ErrSessionSuperseded) {
			t.Fatalf("stale remote setup = %v, want superseded", err)
		}
		if posts.Load() != 0 {
			t.Fatalf("stale remote setup sent %d POSTs, want 0", posts.Load())
		}
	})
}

func TestLegacyGenerationlessRecipeReconstructsLocalTranscode(t *testing.T) {
	ctx := context.Background()
	mgr := playback.NewSessionManager(0, 0)
	ffmpeg := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	transcodeDir := t.TempDir()
	tm := playback.NewTranscodeManager()
	tm.Sessions = mgr
	tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{TranscodeDir: transcodeDir, FFmpegPath: ffmpeg}
	}
	card := playback.RecipeCard{
		SessionID: "legacy-upstream", SessionGeneration: "", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		// Empty PlayMethod is the legacy transcode discriminator.
		InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
	}
	native, err := tm.ReconstructSessionChecked(ctx, card.SessionID, card.UserID, card)
	if err != nil || native == nil || native.Generation != "" {
		t.Fatalf("reconstruct legacy native session: native=%+v err=%v", native, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	store.Put(PlaybackSession{
		ID: "play-legacy", UpstreamSessionID: card.SessionID, UpstreamSessionGeneration: "",
		UpstreamPlayMethod: "transcode", Recipe: &card, MediaSources: []PlaybackMediaSource{source},
	})
	h := &PlaybackHandler{playbackStore: store, sessionMgr: mgr, tm: tm}

	transcodeSession, err := h.ensureTranscodeSession(ctx, "play-legacy", card.SessionID, "", source)
	if err != nil || transcodeSession == nil {
		t.Fatalf("start legacy reconstructed ffmpeg: session=%+v err=%v", transcodeSession, err)
	}
	t.Cleanup(func() { h.tm.CloseTranscodeSession(card.SessionID, "") })
	live, err := mgr.GetSession(card.SessionID)
	if err != nil || live.Generation != "" {
		t.Fatalf("legacy snapshot generation was invented: live=%+v err=%v", live, err)
	}
	compat, ok := store.Get("play-legacy")
	if !ok || compat.UpstreamSessionGeneration != "" || compat.Recipe == nil || compat.Recipe.SessionGeneration != "" {
		t.Fatalf("legacy compat identity was upgraded: ok=%v compat=%+v", ok, compat)
	}
}

func TestLegacyEmptyCompatIdentityCannotAdoptG2(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", TranscodeNodeURL: "g2-node", Position: 12,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	h := &PlaybackHandler{sessionMgr: mgr}
	legacy := &PlaybackSession{UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: ""}

	if err := h.setCompatTranscodeNodeURL(legacy, "legacy-node"); !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("empty legacy node mutation = %v, want superseded", err)
	}
	if err := h.updateCompatProgress(legacy, 99, true); !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("empty legacy progress mutation = %v, want superseded", err)
	}
	card := h.upstreamRecipeCard(
		legacy,
		&Session{StreamAppUserID: 7, ProfileID: "profile-1"},
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if card.SessionGeneration != "" {
		t.Fatalf("empty legacy recipe adopted generation %q", card.SessionGeneration)
	}
	finalized := false
	err := h.terminateCompatUpstream(t.Context(), legacy, func() error {
		finalized = true
		return nil
	})
	if !errors.Is(err, playback.ErrSessionSuperseded) || finalized {
		t.Fatalf("empty legacy termination err=%v finalized=%v, want fail closed", err, finalized)
	}
	live, err := mgr.GetSession("shared-upstream")
	if err != nil || live.Generation != "g2" || live.TranscodeNodeURL != "g2-node" || live.Position != 12 || live.IsPaused {
		t.Fatalf("empty legacy identity adopted or changed G2: live=%+v err=%v", live, err)
	}
}
