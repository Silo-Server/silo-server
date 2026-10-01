package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
