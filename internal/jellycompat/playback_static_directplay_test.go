package jellycompat

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// newStaticDirectPlayHandler builds a PlaybackHandler wired for an end-to-end
// HandleVideoStream direct-play serve: a real temp file (so ServeDirectPlay
// returns 200), an empty playback store (so resolvePlaybackRoute fails and the
// Static fallback is exercised), and a stub session manager (HandleVideoStream
// calls ensureUpstreamPlayback after the static session is created).
//
// NodePlanner/JWTSecret are left as zero values so the proxy-redirect branch is
// skipped and the handler serves directly. An empty DeviceProfile yields
// SupportsDirectPlay=true (no DirectPlayProfiles to reject), so the serve path
// stays "direct".
func newStaticDirectPlayHandler(t *testing.T) (*PlaybackHandler, string, string) {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("fake media bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	codec := NewResourceIDCodec()
	contentID := "movie-1"
	detail := &upstreamItemDetail{
		ContentID: contentID,
		Type:      "movie",
		Versions: []catalog.FileVersion{{
			FileID:    42,
			FilePath:  filePath,
			Container: "mkv",
			Duration:  3600,
			AddedAt:   time.Now(),
		}},
	}
	handler := &PlaybackHandler{
		codec:         codec,
		content:       &stubContentService{detail: detail},
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: filePath}},
		playbackStore: NewPlaybackSessionStore(time.Hour, nil),
		sessionMgr:    &testCompatSessionManager{},
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	return handler, encodedID, "fake media bytes"
}

// TestHandleVideoStream_LowercaseStaticServesFile proves the case-insensitive
// Static guard now matches SenPlayer's lowercase static=true: with an empty
// playback store the route resolves only via the static fallback, and the file
// is served end-to-end. Without the fix this returns 404 "Playback session not
// found".
func TestHandleVideoStream_LowercaseStaticServesFile(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "static=true")

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_LowercaseStaticWithMediaSourceId closes the
// SenPlayer-exact call shape: lowercase static=true alongside a mediaSourceId
// query param matching the source. The handler must still serve the file
// end-to-end (200 + body).
func TestHandleVideoStream_LowercaseStaticWithMediaSourceId(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	// FileID 42 (see newStaticDirectPlayHandler) -> the deterministic media
	// source id the static play session builds for this version.
	mediaSourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	rawQuery := "static=true&mediaSourceId=" + url.QueryEscape(mediaSourceID)
	rec := serveStaticStream(handler, encodedID, rawQuery)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_UppercaseStaticServesFile regression-guards the
// original Infuse path (Static=true, uppercase key): it must keep serving so a
// future over-narrow change to the guard is caught.
func TestHandleVideoStream_UppercaseStaticServesFile(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "Static=true")

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_NoStaticNoSessionReturns404 proves the static fallback
// does not fire unconditionally: with no Static param and an empty playback
// store, resolvePlaybackRoute correctly 404s.
func TestHandleVideoStream_NoStaticNoSessionReturns404(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "")

	if rec.Code != 404 {
		t.Fatalf("expected status 404; got %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleVideoStream_StaticDirectPlayReusesSessionAcrossRequests proves the
// ghost-session fix: a Static=true direct play repeats the client's own
// (server-unknown) PlaySessionId on every range request. These must reuse one
// upstream session via route lookup instead of minting a fresh, separately
// stream-capped session per request — the leak that piled up orphaned sessions
// and tripped the per-user stream limit (429). StartSession must run exactly
// once across the repeated requests.
func TestHandleVideoStream_StaticDirectPlayReusesSessionAcrossRequests(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	mgr := handler.sessionMgr.(*testCompatSessionManager)

	const clientPlaySessionID = "client-generated-psid"
	for i := 0; i < 3; i++ {
		rec := serveStaticStream(handler, encodedID, "Static=true&PlaySessionId="+clientPlaySessionID)
		if rec.Code != 200 {
			t.Fatalf("request %d: expected 200; got %d, body=%s", i, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != body {
			t.Fatalf("request %d: expected file content %q; got %q", i, body, got)
		}
	}

	if mgr.startCalls != 1 {
		t.Fatalf("StartSession ran %d times across 3 Static requests with the same PlaySessionId; want 1 (sessions must be reused, not leaked)", mgr.startCalls)
	}
}

// serveStaticStream issues a GET /Videos/{id}/stream with the given raw query
// (no leading "?"), the chi "id" route param, and a compat session in context.
func serveStaticStream(handler *PlaybackHandler, encodedID, rawQuery string) *httptest.ResponseRecorder {
	target := "/Videos/" + encodedID + "/stream"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest("GET", target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleVideoStream(rec, req)
	return rec
}
