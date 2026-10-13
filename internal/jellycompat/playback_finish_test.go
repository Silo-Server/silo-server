package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
