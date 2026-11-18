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

// TestHandleVideoStream_StaticBypassesNegotiatedCapabilityRejection covers
// clients that explicitly request the static/original file after PlaybackInfo
// created a session whose selected source was marked non-direct-playable by
// codec negotiation. Static=true is a direct-file request, so it must not be
// rejected with "Media source requires transcoding".
func TestHandleVideoStream_StaticBypassesNegotiatedCapabilityRejection(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	sourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	handler.playbackStore.Put(PlaybackSession{
		ID:          "play-1",
		CompatToken: "token-1",
		ItemID:      "movie-1",
		RouteItemID: encodedID,
		UserID:      "user-1",
		MediaSources: []PlaybackMediaSource{{
			ID:                   sourceID,
			FileID:               42,
			Version:              catalog.FileVersion{FileID: 42, Container: "mkv", Duration: 3600},
			SupportsDirectPlay:   false,
			SupportsDirectStream: false,
			SupportsTranscoding:  true,
		}},
	})

	queries := []string{
		"Static=true&PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(sourceID),
		"static=true&PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(sourceID),
	}
	for _, rawQuery := range queries {
		rec := serveStaticStream(handler, encodedID, rawQuery)
		if rec.Code != 200 {
			t.Fatalf("query %q: expected status 200; got %d, body=%s", rawQuery, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != body {
			t.Errorf("query %q: expected file content %q; got %q", rawQuery, body, got)
		}
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
