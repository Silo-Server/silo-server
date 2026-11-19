package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A HEAD on the master playlist answers with the playlist's headers, 410 for a
// play whose upstream session was denied, or a retryable 503 when the file
// lookup fails, and starts nothing: the handler has
// no session manager or node planner, so reaching the transcode start would
// fail the request.
func TestMasterManifestHeadStartsNothing(t *testing.T) {
	tests := []struct {
		name     string
		denied   bool
		fileErr  error
		wantCode int
	}{
		{name: "live play", wantCode: http.StatusOK},
		{name: "denied upstream", denied: true, wantCode: http.StatusGone},
		{name: "file lookup fails", fileErr: errors.New("database unavailable"), wantCode: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			source.ID = "source-1"
			for index := range source.Version.AudioTracks {
				source.Version.AudioTracks[index].Channels = 2
			}
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
				UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
				MediaSources: []PlaybackMediaSource{source},
			})
			rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			handler := &PlaybackHandler{
				playbackStore: store,
				fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: source.FileID}, err: tt.fileErr},
				StreamDeny:    playback.NewStreamDeny(rdb),
			}
			if tt.denied {
				handler.StreamDeny.Deny(t.Context(), "upstream-1")
			}
			request := httptest.NewRequest(http.MethodHead, "/Videos/item-1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1", nil)
			request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "token-1"}))
			recorder := httptest.NewRecorder()

			handler.HandleMasterManifest(recorder, request)

			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, body = %s; want %d", recorder.Code, recorder.Body.String(), tt.wantCode)
			}
			if tt.wantCode == http.StatusOK && recorder.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
				t.Fatalf("Content-Type = %q, want the playlist type", recorder.Header().Get("Content-Type"))
			}
		})
	}
}
