package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// newFinishRecordingHandler swaps the fake session manager for the real one,
// whose finish hook stands in for the native history writer.
func newFinishRecordingHandler(t *testing.T) (*PlaybackHandler, string, *[]playback.Session) {
	t.Helper()
	handler, _, _, sourceID := newReportLivenessHandler("upstream-1", false)
	mgr := playback.NewSessionManager(0, 0)
	mgr.RegisterReconstructed(&playback.Session{
		ID: "upstream-1", UserID: 1, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayDirect, BasePlayMethod: playback.PlayDirect, IsJellyfinCompat: true,
	})
	var finished []playback.Session
	mgr.AddFinishHook(func(_ context.Context, s *playback.Session) {
		finished = append(finished, *s)
	})
	handler.sessionMgr = mgr
	return handler, sourceID, &finished
}

func postCompatPlaybackRequest(t *testing.T, serve http.HandlerFunc, method, target, body string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	serve(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%s %s: status = %d, body = %s", method, target, rec.Code, rec.Body.String())
	}
}

// TestJellyfinStopFinishesUpstreamSessionOnce covers issue #1738: ending a
// Jellyfin play finishes its native session, so the native history writer
// runs, and it runs once however many teardown requests the client sends.
func TestJellyfinStopFinishesUpstreamSessionOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order []string
	}{
		{name: "Stopped, retried Stopped, then ActiveEncodings", order: []string{"stopped", "stopped", "encodings"}},
		{name: "ActiveEncodings before Stopped", order: []string{"encodings", "stopped"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, sourceID, finished := newFinishRecordingHandler(t)
			progress := `{"PlaySessionId":"play-1","MediaSourceId":"` + sourceID + `","PositionTicks":32000000000}`
			postCompatPlaybackRequest(t, handler.HandleSessionPlayingProgress, http.MethodPost, "/Sessions/Playing/Progress", progress)

			for _, step := range tc.order {
				switch step {
				case "stopped":
					stop := `{"PlaySessionId":"play-1","MediaSourceId":"` + sourceID + `","PositionTicks":35000000000}`
					postCompatPlaybackRequest(t, handler.HandleSessionPlayingStopped, http.MethodPost, "/Sessions/Playing/Stopped", stop)
				case "encodings":
					postCompatPlaybackRequest(t, handler.HandleDeleteActiveEncodings, http.MethodDelete, "/Videos/ActiveEncodings?PlaySessionId=play-1", "")
				}
			}

			if len(*finished) != 1 {
				t.Fatalf("finished sessions = %d, want 1", len(*finished))
			}
			want := 3500.0
			if tc.order[0] == "encodings" {
				// ActiveEncodings carries no position; the last progress report stands.
				want = 3200
			}
			if got := (*finished)[0]; got.ID != "upstream-1" || got.Position != want {
				t.Fatalf("finished %q at %v, want upstream-1 at %v", got.ID, got.Position, want)
			}
		})
	}
}

// TestAdminEndRetiresJellyfinPlay: an administrator's stop or terminate ends a
// Jellyfin play for good. Before it, the play serves a segment from a local
// transcode; after it, the transcode is closed, the next segment request finds
// no play, and a progress report does not register the session again.
func TestAdminEndRetiresJellyfinPlay(t *testing.T) {
	const upstreamID = "upstream-1"
	source := PlaybackMediaSource{ID: "source-42", FileID: 42, Version: catalog.FileVersion{FileID: 42}}
	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID: upstreamID, UserID: 1, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode, IsJellyfinCompat: true,
	})
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: upstreamID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		AudioTrackIndex: compatAudioTrackIndexOrDefault(source), SegmentDuration: 2,
	})
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: "item", UpstreamSessionID: upstreamID,
		UpstreamPlayMethod: "transcode", ProgressPersistenceKnown: true, MediaSources: []PlaybackMediaSource{source},
		Recipe: &card, RoutingAssignment: compatLocalVideoAPIRoutingAssignment(),
	})
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, upstreamID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, upstreamID, "seg_00000.ts"), []byte("segment"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm := playback.NewTranscodeManager()
	tm.Sessions = sessions
	ffmpegPath := writeCompatTestFFmpeg(t)
	tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{TranscodeDir: root, FFmpegPath: ffmpegPath, HWAccel: playback.HWAccelNone}
	}
	t.Cleanup(func() { tm.CloseTranscodeSession(upstreamID, "") })
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: sessions, tm: tm}

	requestSegment := func() int {
		req := httptest.NewRequest(http.MethodGet, "/Videos/item/hls/play-1/seg_00000.ts", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("playlistId", "play-1")
		routeCtx.URLParams.Add("segmentId", "seg_00000")
		routeCtx.URLParams.Add("segmentContainer", "ts")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
		rec := httptest.NewRecorder()
		handler.HandleHLSSegment(rec, req.WithContext(ctx))
		return rec.Code
	}
	if code := requestSegment(); code != http.StatusOK || tm.GetTranscodeSession(upstreamID) == nil {
		t.Fatalf("segment before the end = %d, transcode running = %v", code, tm.GetTranscodeSession(upstreamID) != nil)
	}

	if !handler.endUpstreamSessionAsAdmin(t.Context(), upstreamID) {
		t.Error("the administrator's end found no play to retire")
	}
	// The native administrator path stops the session next; the Jellyfin
	// surface leaves that to it.
	if err := sessions.StopSession(upstreamID); err != nil {
		t.Fatalf("native session was already gone: %v", err)
	}

	if tm.GetTranscodeSession(upstreamID) != nil {
		t.Error("the play's transcode is still running")
	}
	if code := requestSegment(); code != http.StatusNotFound {
		t.Errorf("segment after the end = %d, want 404", code)
	}
	progress := `{"PlaySessionId":"play-1","MediaSourceId":"source-42","PositionTicks":32000000000}`
	postCompatPlaybackRequest(t, handler.HandleSessionPlayingProgress, http.MethodPost, "/Sessions/Playing/Progress", progress)
	if _, err := sessions.GetSession(upstreamID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Errorf("a progress report registered the ended session again: %v", err)
	}
	if tm.GetTranscodeSession(upstreamID) != nil {
		t.Error("a request after the end started the transcode again")
	}
}
