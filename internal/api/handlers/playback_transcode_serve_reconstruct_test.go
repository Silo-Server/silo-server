package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestTranscodeServeReconstructsPlainTranscodeFromTokenOnLiveSession pins the
// live-session recovery contract for plain (non-tone-mapped) transcodes: a
// live Session whose runtime is gone must reconstruct from the client's recipe
// token instead of answering 404, joining the same atomic playback+runtime
// front door the tone-mapped path uses.
func TestTranscodeServeReconstructsPlainTranscodeFromTokenOnLiveSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
	handler.JWTSecret = "test-secret"

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(live.ID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want reconstructed manifest", rec.Code, rec.Body.String())
	}
	if handler.tm.GetTranscodeSession(live.ID) == nil {
		t.Fatal("live plain transcode was not reconstructed from its token")
	}
}

// TestTranscodeServePlainCardRecoveryFailureStays404 pins that a plain
// transcode whose token recipe cannot be rebuilt keeps the 404 semantics of
// the second-chance reconstruct branch instead of degrading into a 503.
func TestTranscodeServePlainCardRecoveryFailureStays404(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	// A nonexistent ffmpeg binary fails at spawn time, so the reconstruction
	// itself cannot be performed (as opposed to a binary that starts and never
	// produces a manifest, which is a different 503 path).
	handler.PlaybackConfig = playbackTestConfig(filepath.Join(t.TempDir(), "missing-ffmpeg"), t.TempDir())
	handler.JWTSecret = "test-secret"

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404 when plain recovery cannot be performed", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
	}
	if resp.Error != "not_found" {
		t.Fatalf("error = %q, want %q", resp.Error, "not_found")
	}
	if handler.tm.GetTranscodeSession(live.ID) != nil {
		t.Fatal("failed recovery left a registered transcode runtime")
	}
}

// A keyframe-planned session rebuilt after the server lost it (no live
// session, no runtime) whose recorded playlist can't be served again ends:
// the player hears 410, and the session the rebuild registered is removed
// rather than left holding a stream slot nothing serves.
func TestTranscodeServeColdPlannedRebuildThatCantPlanEndsTheSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), transcodeDir)
	handler.JWTSecret = "test-secret"

	sessionID := "5b1e6f0e-6c1d-4a51-9d0c-1770a0000001"
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "aac", SegmentDuration: 6,
		KeyframePlaylist: true, CopyVideoMPEGTS: true,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}
	// The first start planned an MPEG-TS playlist (a durations-only record),
	// which planned sessions no longer assemble.
	outputDir := filepath.Join(transcodeDir, sessionID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "keyframe-plan.json"), []byte(`{"planned":true,"durations":[6,6]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+sessionID+"/segment/seg_00001.ts?st="+token,
		nil, map[string]string{"session_id": sessionID, "name": "seg_00001.ts"}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(sessionID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s, want 410 for a playlist no rebuild can serve", rec.Code, rec.Body.String())
	}
	if _, err := sessionMgr.GetSession(sessionID); err == nil {
		t.Fatal("the failed rebuild left its playback session registered")
	}
}

// TestTranscodeManifestStatesItsLength pins that the playlist is served with a
// Content-Length. A full-length playlist runs to tens of kilobytes, past
// net/http's write buffer, so without the header it goes out chunked; a player
// that opens HLS with a ranged byte reader (AetherEngine on Apple platforms)
// then takes it for a non-seekable stream and fails to recognize it as a
// playlist.
func TestTranscodeManifestStatesItsLength(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
	handler.JWTSecret = "test-secret"

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(live.ID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want the manifest", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "#EXTM3U") {
		t.Fatalf("body = %q, want an HLS playlist", rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Fatalf("Content-Length = %q, want %q (the body's length)", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-transform") {
		t.Fatalf("Cache-Control = %q, want no-transform so proxies keep the length", got)
	}
}
