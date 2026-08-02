package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/go-chi/chi/v5"
)

func TestHLSSegmentStaleRecipeCannotAdoptLiveSuccessor(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", UserID: 7, MediaFileID: 99,
	}); err != nil || got == nil {
		t.Fatalf("register G2: session=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	g1Recipe := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{SessionID: "shared-upstream"}).WithSessionIdentity("g1", time.Now().UTC().Add(-time.Minute))
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "tok", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1",
		UpstreamPlayMethod: "transcode", TranscodeStarted: true, Recipe: &g1Recipe,
	})
	h := NewPlaybackHandler(nil, nil, nil, nil, store, mgr, nil, nil)
	g2Transcode := &playback.TranscodeSession{}
	h.tm.RegisterTranscodeSession("shared-upstream", g2Transcode)
	req := withCompatSession(httptest.NewRequest(http.MethodGet, "/Videos/play-1/hls/seg.ts", nil), "tok")
	compat := SessionFromContext(req.Context())
	compat.StreamAppUserID = 7
	route := chi.NewRouteContext()
	route.URLParams.Add("playlistId", "play-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()

	h.HandleHLSSegment(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale HLS status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g1" || got.Recipe == nil || got.Recipe.SessionGeneration != "g1" {
		t.Fatalf("stale HLS adopted G2 compat identity: ok=%v session=%+v", ok, got)
	}
	if live, err := mgr.GetSession("shared-upstream"); err != nil || live.Generation != "g2" || live.MediaFileID != 99 {
		t.Fatalf("stale HLS changed G2 native: session=%+v err=%v", live, err)
	}
	if h.tm.GetTranscodeSession("shared-upstream") != g2Transcode {
		t.Fatal("stale HLS changed G2 transcode")
	}
}

func TestStaleCompatGenerationCannotBindReplacementNodeURL(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: time.Now().UTC(), TranscodeNodeURL: "https://g2-node",
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "owner", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g2",
	})
	h := &PlaybackHandler{sessionMgr: mgr, playbackStore: store}
	stale := &PlaybackSession{
		ID: "play-1", CompatToken: "owner", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1",
	}

	err := h.setCompatTranscodeNodeURL(stale, "https://stale-g1-node")
	if !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("stale node bind error = %v, want superseded", err)
	}
	live, err := mgr.GetSession("shared-upstream")
	if err != nil || live.TranscodeNodeURL != "https://g2-node" {
		t.Fatalf("stale G1 changed G2 node URL: live=%+v err=%v", live, err)
	}
	compat, ok := store.Get("play-1")
	if !ok || compat.UpstreamSessionGeneration != "g2" || compat.Recipe != nil || compat.TranscodeStarted {
		t.Fatalf("stale node bind changed G2 compat/recipe state: ok=%v compat=%+v", ok, compat)
	}
}

func TestStaleTranscodeStartCannotPersistRecipeIntoReplacement(t *testing.T) {
	ctx := context.Background()
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(ctx, &playback.Session{
		ID: "shared-upstream", Generation: "g1", StartedAt: time.Now().UTC(), MediaFileID: 11,
	}); err != nil || got == nil {
		t.Fatalf("register G1: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1",
	})
	h := &PlaybackHandler{sessionMgr: mgr, playbackStore: store}
	opts := playback.TranscodeOpts{SessionID: "shared-upstream", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	if err := h.recordTranscodeStreamDetails(ctx, "shared-upstream", "g1", opts); err != nil {
		t.Fatalf("G1 exact stream details: %v", err)
	}

	if err := mgr.TerminateSessionGeneration(ctx, "shared-upstream", "g1", func() error { return nil }); err != nil {
		t.Fatalf("terminate G1: %v", err)
	}
	startedAt := time.Now().UTC()
	if got, err := mgr.RegisterReconstructedChecked(ctx, &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: startedAt, MediaFileID: 22,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	g2Recipe := &playback.RecipeCard{SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 22}
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g2",
		UpstreamStartedAt: startedAt, UpstreamPlayMethod: "transcode", TranscodeStarted: true, Recipe: g2Recipe,
	})

	err := h.persistTranscodeRecipe(ctx, "play-1", "shared-upstream", "g1", opts)
	if !errors.Is(err, errUpstreamReplaced) {
		t.Fatalf("stale G1 recipe persistence = %v, want upstream replaced", err)
	}
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g2" || !got.TranscodeStarted || got.Recipe == nil || got.Recipe.SessionGeneration != "g2" || got.Recipe.MediaFileID != 22 {
		t.Fatalf("stale G1 recipe changed G2 compat state: ok=%v got=%+v", ok, got)
	}
}

func TestDelayedG1ProcessMonitorUsesCapturedGeneration(t *testing.T) {
	ctx := context.Background()
	mgr := playback.NewSessionManager(0, 0)
	script := filepath.Join(t.TempDir(), "fail-ffmpeg.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
		t.Fatalf("write failing ffmpeg: %v", err)
	}
	deadG1, err := playback.StartTranscode(ctx, playback.TranscodeOpts{
		SessionID: "shared-upstream", InputPath: "ignored", OutputDir: t.TempDir(), FFmpegPath: script,
	})
	if err != nil {
		t.Fatalf("start G1 process: %v", err)
	}

	// G2 is installed after the G1 process starts but before its exit monitor is
	// registered, reproducing the exact reusable-ID interleave.
	startedAt := time.Now().UTC()
	if got, err := mgr.RegisterReconstructedChecked(ctx, &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: startedAt,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g2",
		UpstreamStartedAt: startedAt, UpstreamPlayMethod: "transcode", TranscodeStarted: true,
		Recipe: &playback.RecipeCard{SessionID: "shared-upstream", SessionGeneration: "g2", MediaFileID: 22},
	})
	h := NewPlaybackHandler(nil, nil, nil, nil, store, mgr, nil, nil)
	h.tm.RegisterTranscodeSession("shared-upstream", deadG1)
	seenGeneration := make(chan string, 1)
	onCrash := h.tm.OnFFmpegCrashIdentity
	h.tm.OnFFmpegCrashIdentity = func(ctx context.Context, id, generation string, dead *playback.TranscodeSession) {
		seenGeneration <- generation
		onCrash(ctx, id, generation, dead)
	}

	h.monitorLocalTranscodeExit("shared-upstream", "g1", deadG1)
	select {
	case generation := <-seenGeneration:
		if generation != "g1" {
			t.Fatalf("delayed G1 monitor generation = %q, want g1", generation)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for delayed G1 crash callback")
	}
	if live, err := mgr.GetSession("shared-upstream"); err != nil || live.Generation != "g2" {
		t.Fatalf("delayed G1 monitor changed G2 native state: live=%+v err=%v", live, err)
	}
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g2" || got.Recipe == nil || got.Recipe.SessionGeneration != "g2" {
		t.Fatalf("delayed G1 monitor changed G2 compat state: ok=%v got=%+v", ok, got)
	}
}

func TestStaleProgressPersistenceMetadataCannotMutateReplacement(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g2",
		ProgressPersistenceKnown: true, DisableProgressPersistence: false,
	})
	h := &PlaybackHandler{playbackStore: store}
	stale := &PlaybackSession{ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1"}

	h.recordCompatProgressPersistence(stale, true)
	got, ok := store.Get("play-1")
	if !ok || got.UpstreamSessionGeneration != "g2" || !got.ProgressPersistenceKnown || got.DisableProgressPersistence {
		t.Fatalf("stale G1 changed G2 progress persistence: ok=%v got=%+v", ok, got)
	}
}

func TestProxyRedirectUsesCapturedCompatGeneration(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: time.Now().UTC(),
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	g1StartedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	stale := &PlaybackSession{
		ID: "play-1", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1", UpstreamStartedAt: g1StartedAt,
	}
	h := &PlaybackHandler{sessionMgr: mgr, JWTSecret: "secret"}
	redirect, err := h.buildProxyRedirectURL(
		stale,
		string(playback.PlayDirect),
		&models.MediaFile{FilePath: "/media/movie.mkv"},
		PlaybackMediaSource{},
		"",
		0,
		&nodepool.Node{URL: "https://proxy.example"},
	)
	if err != nil {
		t.Fatalf("build proxy redirect: %v", err)
	}
	parsed, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse proxy redirect: %v", err)
	}
	claims, err := streamtoken.Verify(path.Base(parsed.Path), "secret")
	if err != nil {
		t.Fatalf("verify proxy token: %v", err)
	}
	if claims.SessionGeneration != "g1" || claims.StartedAt != g1StartedAt.Format(time.RFC3339Nano) {
		t.Fatalf("proxy token adopted G2: generation=%q started_at=%q", claims.SessionGeneration, claims.StartedAt)
	}
}

func TestStaleCompatGenerationCannotRecordReplacementTranscodeDetails(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: time.Now().UTC(),
		TargetVideoCodec: "hevc", TargetAudioCodec: "copy", TranscodeAudio: false,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "owner", UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g2",
	})
	h := &PlaybackHandler{sessionMgr: mgr, playbackStore: store}

	err := h.recordTranscodeStreamDetails(
		context.Background(), "shared-upstream", "g1",
		playback.TranscodeOpts{TargetCodecVideo: "h264", TargetCodecAudio: "aac"},
	)
	if !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("stale stream-details error = %v, want superseded", err)
	}
	live, err := mgr.GetSession("shared-upstream")
	if err != nil || live.TargetVideoCodec != "hevc" || live.TargetAudioCodec != "copy" || live.TranscodeAudio {
		t.Fatalf("stale G1 changed G2 stream details: live=%+v err=%v", live, err)
	}
	compat, ok := store.Get("play-1")
	if !ok || compat.UpstreamSessionGeneration != "g2" || compat.Recipe != nil || compat.TranscodeStarted {
		t.Fatalf("stale details changed G2 compat/recipe state: ok=%v compat=%+v", ok, compat)
	}
}

func TestStaleCompatGenerationCannotMutateReplacementActivity(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(context.Background(), &playback.Session{
		ID: "shared-upstream", Generation: "g2", StartedAt: time.Now().UTC(), Position: 12, AudioTrackIndex: 3,
	}); err != nil || got == nil {
		t.Fatalf("register G2: got=%+v err=%v", got, err)
	}
	h := &PlaybackHandler{sessionMgr: mgr}
	stale := &PlaybackSession{
		UpstreamSessionID: "shared-upstream", UpstreamSessionGeneration: "g1", UpstreamPlayMethod: "direct",
	}
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	if err := h.updateCompatProgress(stale, 99, true); !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("stale progress = %v, want superseded", err)
	}
	if err := h.syncUpstreamAudioSelection(stale, source); !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("stale audio = %v, want superseded", err)
	}
	if _, err := h.beginCompatTransport(stale); !errors.Is(err, playback.ErrSessionSuperseded) {
		t.Fatalf("stale transport begin = %v, want superseded", err)
	}
	live, err := mgr.GetSession("shared-upstream")
	if err != nil || live.Position != 12 || live.IsPaused || live.AudioTrackIndex != 3 {
		t.Fatalf("stale activity changed G2: live=%+v err=%v", live, err)
	}
}
