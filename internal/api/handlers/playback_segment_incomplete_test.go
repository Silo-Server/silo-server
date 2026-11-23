package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A segment request that races a writer must never be answered 200 with a body
// that is a fragment of the segment: the client appends that fragment and
// waits for frames that never come. The route answers the retry shape the
// client already understands (503 + Retry-After) instead.
func TestTranscodeSegmentIncompleteAnswersRetry(t *testing.T) {
	const sessionID = "incomplete-segment-session"

	wholeInit := append(
		append([]byte{0, 0, 2, 0, 'f', 't', 'y', 'p'}, bytes.Repeat([]byte{0}, 504)...),
		append([]byte{0, 0, 2, 0, 'm', 'o', 'o', 'v'}, bytes.Repeat([]byte{0}, 504)...)...,
	)
	dir := t.TempDir()
	// FFmpeg rewrites init.mp4 in place; this is the prefix a request in that
	// window reads.
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), wholeInit[:512], 0o600); err != nil {
		t.Fatal(err)
	}
	// A media segment whose muxer temp file is still present is bytes in
	// progress, not a segment.
	if err := os.WriteFile(filepath.Join(dir, "seg_00468.ts"), bytes.Repeat([]byte{0x47}, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seg_00468.ts.tmp"), bytes.Repeat([]byte{0x47}, 128), 0o600); err != nil {
		t.Fatal(err)
	}

	session := playback.NewTranscodeSessionForTest(dir)
	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID:          sessionID,
		UserID:      1,
		MediaFileID: 91,
		PlayMethod:  playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(sessions)
	handler.TranscodeManager().RegisterTranscodeSession(sessionID, session)

	for _, name := range []string{"init.mp4", "seg_00468.ts"} {
		rec := httptest.NewRecorder()
		handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
			http.MethodGet,
			"/api/v1/playback/transcode/"+sessionID+"/segment/"+name,
			nil,
			map[string]string{"session_id": sessionID, "name": name},
		))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want %d (retry); body = %s", name, rec.Code, http.StatusServiceUnavailable, rec.Body.String())
		}
		if got := rec.Header().Get("Retry-After"); got == "" {
			t.Fatalf("%s answered without a Retry-After", name)
		}
		if rec.Body.Len() >= 512 {
			t.Fatalf("%s answered a %d-byte body, which is the fragment itself", name, rec.Body.Len())
		}
	}

	// The same names are served once the writer finishes them.
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), wholeInit, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "seg_00468.ts.tmp")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"init.mp4", "seg_00468.ts"} {
		rec := httptest.NewRecorder()
		handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
			http.MethodGet,
			"/api/v1/playback/transcode/"+sessionID+"/segment/"+name,
			nil,
			map[string]string{"session_id": sessionID, "name": name},
		))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s after completion status = %d, want 200; body = %s", name, rec.Code, rec.Body.String())
		}
	}
}
