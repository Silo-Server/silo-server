package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	// Built as the server builds it, so the end below reaches the handler
	// through the hook the handler registered on the session manager.
	handler := NewPlaybackHandler(nil, nil, nil, nil, store, sessions, nil, nil)
	handler.TranscodeDir, handler.FFmpegPath, handler.HWAccel = root, writeCompatTestFFmpeg(t), playback.HWAccelNone
	tm := handler.tm
	t.Cleanup(func() { tm.CloseTranscodeSession(upstreamID, "") })

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

	if !sessions.EndCompatPlayAsAdmin(t.Context(), upstreamID) {
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

// TestAdminEndLeavesDirectPlayRouted: an administrator's end does not retire
// a direct-play play. Its next request rebuilds the session under the same
// id, the id a proxy's deny marker refuses. A retired row would instead let a
// Static=true client start a fresh play under an id no marker covers.
func TestAdminEndLeavesDirectPlayRouted(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)
	sessions := playback.NewSessionManager(0, 0)
	handler.sessionMgr = sessions
	handler.tm = playback.NewTranscodeManager()
	handler.tm.Sessions = sessions
	if rec := serveStaticStream(handler, encodedID, "Static=true"); rec.Code != http.StatusOK {
		t.Fatalf("direct play = %d %s", rec.Code, rec.Body.String())
	}
	started := sessions.AllSessions()
	if len(started) != 1 {
		t.Fatalf("sessions after the first request = %d, want 1", len(started))
	}
	upstreamID := started[0].ID

	if handler.endUpstreamSessionAsAdmin(t.Context(), upstreamID) {
		t.Error("the administrator's end retired a direct play")
	}
	if err := sessions.StopSession(upstreamID); err != nil {
		t.Fatal(err)
	}

	if rec := serveStaticStream(handler, encodedID, "Static=true"); rec.Code != http.StatusOK {
		t.Fatalf("direct play after the end = %d %s", rec.Code, rec.Body.String())
	}
	if after := sessions.AllSessions(); len(after) != 1 || after[0].ID != upstreamID {
		t.Fatalf("the next request runs under %d session(s), first %q; want the ended session %q rebuilt", len(after), after[0].ID, upstreamID)
	}
}

// retireFailingStore fails the first retirements, as a store does while
// Postgres is unreachable.
type retireFailingStore struct {
	CompatPlaybackStore
	failures atomic.Int32
	landed   chan struct{}
}

func (s *retireFailingStore) RetireUpstreamSession(upstreamSessionID string) (bool, error) {
	if s.failures.Add(-1) >= 0 {
		return false, errors.New("store unavailable")
	}
	defer close(s.landed)
	return s.CompatPlaybackStore.RetireUpstreamSession(upstreamSessionID)
}

// TestAdminEndRetriesFailedRetirement: a retirement the store could not write
// is retried until it lands, so the other servers stop routing the play. The
// bubble's clock runs the backoff; with no retry left to run, waiting for the
// retirement deadlocks and fails the test.
func TestAdminEndRetriesFailedRetirement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &retireFailingStore{CompatPlaybackStore: NewPlaybackSessionStore(time.Hour, nil), landed: make(chan struct{})}
		store.failures.Store(2)
		store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode"})
		handler := &PlaybackHandler{playbackStore: store}

		if handler.endUpstreamSessionAsAdmin(t.Context(), "upstream-1") {
			t.Fatal("reported a retirement the store did not make")
		}
		<-store.landed
		if _, ok := store.Get("play-1"); ok {
			t.Fatal("the retried retirement left the play routable")
		}
	})
}

// TestAdminEndClosesTranscodeStartedBeforeSessionStop: a segment request that
// was already past its row lookup can start a transcode after the end closed
// the play's transcode and before the administrator path stops the session.
// Once the session is stopped nothing else would close it, so the end closes
// the play's transcode again at that point.
func TestAdminEndClosesTranscodeStartedBeforeSessionStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sessions := playback.NewSessionManager(0, 0)
		sessions.RegisterReconstructed(&playback.Session{
			ID: "upstream-1", UserID: 1, PlayMethod: playback.PlayTranscode, IsJellyfinCompat: true,
		})
		store := NewPlaybackSessionStore(time.Hour, nil)
		store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode"})
		handler := &PlaybackHandler{playbackStore: store, sessionMgr: sessions, tm: playback.NewTranscodeManager()}

		if !handler.endUpstreamSessionAsAdmin(t.Context(), "upstream-1") {
			t.Fatal("the administrator's end found no play to retire")
		}
		handler.tm.RegisterTranscodeSession("upstream-1", &playback.TranscodeSession{})
		if err := sessions.StopSession("upstream-1"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if handler.tm.GetTranscodeSession("upstream-1") != nil {
			t.Fatal("a transcode started before the session stop is still registered")
		}
	})
}

// headerSignalingRecorder reports when the handler has written its status.
type headerSignalingRecorder struct {
	*httptest.ResponseRecorder
	wroteHeader chan struct{}
	once        sync.Once
}

func (w *headerSignalingRecorder) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
	w.once.Do(func() { close(w.wroteHeader) })
}

// TestProgressiveRemuxEndsWhenItsSessionStops: a progressive remux is one
// response, so stopping its session has to end the response itself. The fake
// ffmpeg writes nothing and runs for 30 seconds, which is how long the
// response would stay open without the stop reaching it.
func TestProgressiveRemuxEndsWhenItsSessionStops(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)
	sessions := playback.NewSessionManager(0, 0)
	handler.sessionMgr = sessions
	handler.FFmpegPath = writeCompatTestFFmpeg(t)
	source := testCompatSource(handler.codec, testCompatVersion())
	source.SupportsDirectPlay = false
	handler.playbackStore.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", RouteItemID: encodedID, MediaSources: []PlaybackMediaSource{source},
	})

	req := httptest.NewRequest(http.MethodGet, "/Videos/"+encodedID+"/stream.mp4?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	rec := &headerSignalingRecorder{ResponseRecorder: httptest.NewRecorder(), wroteHeader: make(chan struct{})}
	served := make(chan struct{})
	go func() {
		defer close(served)
		handler.HandleVideoStream(rec, req.WithContext(ctx))
	}()

	select {
	case <-rec.wroteHeader:
	case <-served:
	}
	started := sessions.AllSessions()
	if len(started) != 1 {
		t.Fatalf("sessions while the remux streams = %d, want 1 (status %d)", len(started), rec.Code)
	}
	if err := sessions.StopSession(started[0].ID); err != nil {
		t.Fatal(err)
	}
	// Only a failing run waits this long.
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("the remux response outlived its stopped session")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("remux status = %d %s", rec.Code, rec.Body.String())
	}
}
