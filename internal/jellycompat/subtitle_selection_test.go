package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// subtitleSelectionVersion is a fixture with one video, one audio, one embedded
// default text subtitle (stream index 2), one external text subtitle (stream
// index 3), and one bitmap subtitle that requires burn-in (not streamable).
func subtitleSelectionVersion() catalog.FileVersion {
	return catalog.FileVersion{
		FileID:    42,
		Duration:  3600,
		Container: "mkv",
		Bitrate:   8000,
		VideoTracks: []models.VideoTrack{
			{Codec: "h264", Width: 1920, Height: 1080},
		},
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true, Title: "Main"},
		},
		SubtitleTracks: []catalog.VersionSubtitleTrack{
			{Index: 2, Codec: "subrip", Language: "eng", Title: "English", Default: true},
			{Codec: "srt", Language: "spa", Title: "Spanish", External: true},
			{Codec: "dvd_subtitle", Language: "fre", Title: "French (bitmap)"},
		},
	}
}

func newSubtitleSelectionHandler(t *testing.T) (*PlaybackHandler, string) {
	t.Helper()
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	version := subtitleSelectionVersion()

	handler := &PlaybackHandler{
		content: &stubContentService{detail: &upstreamItemDetail{
			ContentID: contentID,
			Versions:  []catalog.FileVersion{version},
		}},
		codec:          codec,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		playbackStore:  NewPlaybackSessionStore(time.Hour, nil),
		SubtitleRepo: fakeSubtitleRepository{downloaded: map[int][]subtitles.DownloadedSubtitle{
			42: {
				{MediaFileID: 42, Language: "deu", Format: subtitles.FormatSRT, Provider: "opensubtitles"},
			},
		}},
	}
	return handler, routeID
}

// servePlaybackInfo posts body to /Items/{routeID}/PlaybackInfo as the
// token-1 session and returns the recorded response.
func servePlaybackInfo(handler *PlaybackHandler, routeID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	rr := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rr, req)
	return rr
}

func postPlaybackInfo(t *testing.T, handler *PlaybackHandler, routeID, body string) playbackInfoResponseDTO {
	t.Helper()
	rr := servePlaybackInfo(handler, routeID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp playbackInfoResponseDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.MediaSources) != 1 {
		t.Fatalf("media sources = %d, want 1", len(resp.MediaSources))
	}
	return resp
}

// erroringSubtitleRepository simulates a transient failure of the downloaded
// subtitle lookup while satisfying the rest of the repository interface.
type erroringSubtitleRepository struct {
	fakeSubtitleRepository
}

func (erroringSubtitleRepository) ListDownloadedSubtitles(context.Context, int) ([]subtitles.DownloadedSubtitle, error) {
	return nil, errors.New("subtitle store unavailable")
}

// noRoute marks a case PlaybackInfo answers with Jellyfin's NoCompatibleStream.
const noRoute = -1

// A server cap above the file's bitrate isn't why a sidecar the profile takes
// only embedded has no route, so the answer is NoCompatibleStream, not the
// bitrate-limit error: the check without the cap applies subtitle delivery too.
func TestPlaybackInfoSubtitleRefusalUnderServerCapIsNoCompatibleStream(t *testing.T) {
	h, item := newSubtitleSelectionHandler(t)
	h.ScopeResolver = &stubScopeResolver{scope: access.Scope{MaxLocalStreamBitrateKbps: 20_000, MaxRemoteStreamBitrateKbps: 20_000}}
	rr := servePlaybackInfo(h, item, `{"SubtitleStreamIndex":3,"DeviceProfile":{"SubtitleProfiles":[{"Format":"srt","Method":"Embed"}]}}`)
	assertNoCompatibleStream(t, h, item, rr.Code, rr.Body.Bytes())
}

func TestPlaybackInfoDownloadedLookupFailureIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"downloaded external", `{"SubtitleStreamIndex":5,"DeviceProfile":{"SubtitleProfiles":[{"Format":"srt","Method":"External"}]}}`, 503},
		{"legacy downloaded", `{"SubtitleStreamIndex":5}`, 200},
		{"sidecar", `{"SubtitleStreamIndex":3,"DeviceProfile":{"SubtitleProfiles":[{"Format":"srt","Method":"External"}]}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, item := newSubtitleSelectionHandler(t)
			h.SubtitleRepo = erroringSubtitleRepository{}
			router := chi.NewRouter()
			router.Post("/Items/{id}/PlaybackInfo", h.HandlePlaybackInfo)
			req := httptest.NewRequest(http.MethodPost, "/Items/"+item+"/PlaybackInfo", strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == http.StatusServiceUnavailable {
				if !strings.Contains(rr.Body.String(), "PlaybackUnavailable") {
					t.Fatalf("unexpected error: %s", rr.Body.String())
				}
				if _, _, ok := h.playbackStore.FindByRoute("token-1", item); ok {
					t.Fatal("stored negotiation without required subtitle metadata")
				}
			}
		})
	}
}
