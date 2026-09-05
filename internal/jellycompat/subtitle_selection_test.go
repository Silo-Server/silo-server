package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func ptrString(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.Itoa(*p)
}

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

func TestPlaybackInfoRequest_AcceptsStringSubtitleStreamIndex(t *testing.T) {
	var req playbackInfoRequest
	if err := json.Unmarshal([]byte(`{"SubtitleStreamIndex":"3"}`), &req); err != nil {
		t.Fatalf("unmarshal playback request: %v", err)
	}
	if req.SubtitleStreamIndex == nil {
		t.Fatal("expected subtitle stream index")
	}
	if got := int(*req.SubtitleStreamIndex); got != 3 {
		t.Fatalf("SubtitleStreamIndex = %d, want 3", got)
	}
}

func TestIsValidCompatSubtitleStreamIndex(t *testing.T) {
	version := subtitleSelectionVersion()
	const downloadedCount = 1 // downloaded subtitle occupies stream index 5 (after the bitmap track at 4)

	cases := []struct {
		name        string
		streamIndex int
		want        bool
	}{
		{"video stream", 0, false},
		{"audio stream", 1, false},
		{"embedded text subtitle", 2, true},
		{"external text subtitle", 3, true},
		{"bitmap subtitle (native decoder)", 4, true},
		{"downloaded subtitle", 5, true},
		{"out of range", 6, false},
		{"negative", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidCompatSubtitleStreamIndex(version, downloadedCount, tc.streamIndex); got != tc.want {
				t.Fatalf("isValidCompatSubtitleStreamIndex(%d) = %v, want %v", tc.streamIndex, got, tc.want)
			}
		})
	}
}

func TestIsValidCompatSubtitleStreamIndex_IncludesBitmapSubtitle(t *testing.T) {
	// A version whose only subtitle is bitmap (needs burn-in). Its computed
	// stream index remains selectable for original-file native decoders.
	version := catalog.FileVersion{
		FileID: 7,
		VideoTracks: []models.VideoTrack{
			{Codec: "h264"},
		},
		AudioTracks: []models.AudioTrack{
			{Codec: "aac"},
		},
		SubtitleTracks: []catalog.VersionSubtitleTrack{
			{Codec: "dvd_subtitle", Language: "eng"},
		},
	}
	// Bitmap track would otherwise compute to stream index 2.
	if !isValidCompatSubtitleStreamIndex(version, 0, 2) {
		t.Fatal("bitmap subtitle stream index should remain selectable")
	}
}

func TestExternalSubtitleRouteIndexSkipsEmbeddedStreamIndexGaps(t *testing.T) {
	file := &models.MediaFile{
		VideoTracks: []models.VideoTrack{{Codec: "hevc"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3"}},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 4, Codec: "subrip"},
			{Index: 0, Codec: "hdmv_pgs_subtitle"},
		},
		ExternalSubtitles: []models.ExternalSubtitle{
			{Format: "srt"},
			{Format: "ass"},
		},
	}

	if got := externalSubtitleRouteIndex(file, 0); got != 5 {
		t.Fatalf("first external route index = %d, want 5", got)
	}
	if got := externalSubtitleRouteIndex(file, 1); got != 6 {
		t.Fatalf("second external route index = %d, want 6", got)
	}
}

func TestResolveSelectedSubtitleStreamIndex(t *testing.T) {
	version := subtitleSelectionVersion()
	const downloadedCount = 1
	mediaDefault := intPtr(2)

	cases := []struct {
		name            string
		downloadedKnown bool
		requested       *int
		want            *int
	}{
		{"no request falls back to media default", true, nil, intPtr(2)},
		{"explicit off", true, intPtr(-1), intPtr(-1)},
		{"valid embedded selection", true, intPtr(2), intPtr(2)},
		{"valid external selection", true, intPtr(3), intPtr(3)},
		{"valid downloaded selection", true, intPtr(5), intPtr(5)},
		{"invalid selection falls back to media default", true, intPtr(99), intPtr(2)},
		// When the downloaded list could not be loaded, an embedded/external
		// selection still resolves, but an index we cannot validate is honored
		// rather than downgraded to the media default.
		{"lookup failure honors embedded selection", false, intPtr(3), intPtr(3)},
		{"lookup failure honors unverifiable selection", false, intPtr(5), intPtr(5)},
		{"lookup failure still respects off", false, intPtr(-1), intPtr(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A failed lookup yields no enumerable downloaded subtitles.
			count := downloadedCount
			if !tc.downloadedKnown {
				count = 0
			}
			got := resolveSelectedSubtitleStreamIndex(version, count, tc.downloadedKnown, tc.requested, mediaDefault)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("resolveSelectedSubtitleStreamIndex = %v, want %v", ptrString(got), ptrString(tc.want))
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("resolveSelectedSubtitleStreamIndex = %d, want %d", *got, *tc.want)
			}
		})
	}
}

func TestEffectiveCompatSubtitleStreamIndex(t *testing.T) {
	cases := []struct {
		name   string
		source PlaybackMediaSource
		want   *int
	}{
		{
			name:   "selected overrides default",
			source: PlaybackMediaSource{SelectedSubtitleStreamIndex: intPtr(3), DefaultSubtitleStreamIndex: intPtr(2)},
			want:   intPtr(3),
		},
		{
			name:   "off yields none",
			source: PlaybackMediaSource{SelectedSubtitleStreamIndex: intPtr(-1), DefaultSubtitleStreamIndex: intPtr(2)},
			want:   nil,
		},
		{
			name:   "no selection falls back to default",
			source: PlaybackMediaSource{DefaultSubtitleStreamIndex: intPtr(2)},
			want:   intPtr(2),
		},
		{
			name:   "no selection no default",
			source: PlaybackMediaSource{},
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveCompatSubtitleStreamIndex(tc.source)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("effectiveCompatSubtitleStreamIndex = %v, want %v", ptrString(got), ptrString(tc.want))
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("effectiveCompatSubtitleStreamIndex = %d, want %d", *got, *tc.want)
			}
		})
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

func postPlaybackInfo(t *testing.T, handler *PlaybackHandler, routeID, body string) playbackInfoResponseDTO {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	rr := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rr, req)
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

func defaultSubtitleStreamFromResponse(t *testing.T, resp playbackInfoResponseDTO) (index int, found bool) {
	t.Helper()
	for _, stream := range resp.MediaSources[0].MediaStreams {
		if stream.Type == "Subtitle" && stream.IsDefault {
			if found {
				t.Fatal("more than one subtitle stream marked default")
			}
			index = stream.Index
			found = true
		}
	}
	return index, found
}

func TestHandlePlaybackInfo_DefaultsToEmbeddedDefaultSubtitle(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{}`)

	if resp.MediaSources[0].DefaultSubtitleStreamIndex == nil {
		t.Fatal("expected DefaultSubtitleStreamIndex to be set")
	}
	if got := *resp.MediaSources[0].DefaultSubtitleStreamIndex; got != 2 {
		t.Fatalf("DefaultSubtitleStreamIndex = %d, want 2", got)
	}
	index, found := defaultSubtitleStreamFromResponse(t, resp)
	if !found || index != 2 {
		t.Fatalf("default subtitle stream = (%d, %v), want (2, true)", index, found)
	}
}

func TestHandlePlaybackInfo_DeliversEmbeddedTextAsProfileRequestedExternalVTT(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{
		"SubtitleStreamIndex": 2,
		"DeviceProfile": {
			"SubtitleProfiles": [
				{"Format": "vtt", "Method": "External"}
			]
		}
	}`)

	for _, stream := range resp.MediaSources[0].MediaStreams {
		if stream.Type != "Subtitle" || stream.Index != 2 {
			continue
		}
		if stream.DeliveryMethod != "External" {
			t.Fatalf("DeliveryMethod = %q, want External", stream.DeliveryMethod)
		}
		if !stream.SupportsExternalStream {
			t.Fatal("SupportsExternalStream = false, want true")
		}
		if !strings.Contains(stream.DeliveryURL, "/Subtitles/2/stream.vtt?") {
			t.Fatalf("DeliveryURL = %q, want VTT subtitle route", stream.DeliveryURL)
		}
		return
	}
	t.Fatal("embedded subtitle stream 2 not found")
}

func TestHandlePlaybackInfo_DeliversExternalSRTAsProfileRequestedVTT(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{
		"SubtitleStreamIndex": 3,
		"DeviceProfile": {
			"SubtitleProfiles": [
				{"Format": "vtt", "Method": "External"}
			]
		}
	}`)

	for _, stream := range resp.MediaSources[0].MediaStreams {
		if stream.Type != "Subtitle" || stream.Index != 3 {
			continue
		}
		if stream.DeliveryMethod != "External" {
			t.Fatalf("DeliveryMethod = %q, want External", stream.DeliveryMethod)
		}
		if !stream.SupportsExternalStream {
			t.Fatal("SupportsExternalStream = false, want true")
		}
		if !strings.Contains(stream.DeliveryURL, "/Subtitles/3/stream.vtt?") {
			t.Fatalf("DeliveryURL = %q, want VTT subtitle route", stream.DeliveryURL)
		}
		if !strings.HasSuffix(stream.Path, "/Subtitles/3/stream.vtt") {
			t.Fatalf("Path = %q, want VTT subtitle path", stream.Path)
		}
		return
	}
	t.Fatal("external subtitle stream 3 not found")
}

func TestHandlePlaybackInfo_DeliversDownloadedSRTAsProfileRequestedVTT(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{
		"SubtitleStreamIndex": 5,
		"DeviceProfile": {
			"SubtitleProfiles": [
				{"Format": "vtt", "Method": "External"}
			]
		}
	}`)

	for _, stream := range resp.MediaSources[0].MediaStreams {
		if stream.Type != "Subtitle" || stream.Index != 5 {
			continue
		}
		if !strings.Contains(stream.DeliveryURL, "/Subtitles/5/stream.vtt?") {
			t.Fatalf("DeliveryURL = %q, want VTT subtitle route", stream.DeliveryURL)
		}
		return
	}
	t.Fatal("downloaded subtitle stream 5 not found")
}

func TestHandlePlaybackInfo_HonorsSelectedExternalSubtitle(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{"SubtitleStreamIndex":3}`)

	if resp.MediaSources[0].DefaultSubtitleStreamIndex == nil {
		t.Fatal("expected DefaultSubtitleStreamIndex to be set")
	}
	if got := *resp.MediaSources[0].DefaultSubtitleStreamIndex; got != 3 {
		t.Fatalf("DefaultSubtitleStreamIndex = %d, want 3", got)
	}
	index, found := defaultSubtitleStreamFromResponse(t, resp)
	if !found || index != 3 {
		t.Fatalf("default subtitle stream = (%d, %v), want (3, true)", index, found)
	}
}

func TestHandlePlaybackInfo_HonorsSelectedDownloadedSubtitle(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	// The downloaded subtitle lands at stream index 5 (after the bitmap track at 4).
	resp := postPlaybackInfo(t, handler, routeID, `{"SubtitleStreamIndex":5}`)

	if resp.MediaSources[0].DefaultSubtitleStreamIndex == nil {
		t.Fatal("expected DefaultSubtitleStreamIndex to be set")
	}
	if got := *resp.MediaSources[0].DefaultSubtitleStreamIndex; got != 5 {
		t.Fatalf("DefaultSubtitleStreamIndex = %d, want 5", got)
	}
	index, found := defaultSubtitleStreamFromResponse(t, resp)
	if !found || index != 5 {
		t.Fatalf("default subtitle stream = (%d, %v), want (5, true)", index, found)
	}
}

// erroringSubtitleRepository simulates a transient failure of the downloaded
// subtitle lookup while satisfying the rest of the repository interface.
type erroringSubtitleRepository struct {
	fakeSubtitleRepository
}

func (erroringSubtitleRepository) ListDownloadedSubtitles(context.Context, int) ([]subtitles.DownloadedSubtitle, error) {
	return nil, errors.New("subtitle store unavailable")
}

func TestHandlePlaybackInfo_HonorsExternalSelectionWhenDownloadedLookupFails(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	routeID := codec.EncodeStringID(EncodedIDItem, contentID)
	handler := &PlaybackHandler{
		content: &stubContentService{detail: &upstreamItemDetail{
			ContentID: contentID,
			Versions:  []catalog.FileVersion{subtitleSelectionVersion()},
		}},
		codec:          codec,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		playbackStore:  NewPlaybackSessionStore(time.Hour, nil),
		SubtitleRepo:   erroringSubtitleRepository{},
	}

	// A failed downloaded-subtitle lookup must not discard a valid
	// embedded/external selection (the primary case for external SRT).
	resp := postPlaybackInfo(t, handler, routeID, `{"SubtitleStreamIndex":3}`)
	if resp.MediaSources[0].DefaultSubtitleStreamIndex == nil {
		t.Fatal("expected DefaultSubtitleStreamIndex to be set")
	}
	if got := *resp.MediaSources[0].DefaultSubtitleStreamIndex; got != 3 {
		t.Fatalf("DefaultSubtitleStreamIndex = %d, want 3", got)
	}
	index, found := defaultSubtitleStreamFromResponse(t, resp)
	if !found || index != 3 {
		t.Fatalf("default subtitle stream = (%d, %v), want (3, true)", index, found)
	}
}

func TestHandlePlaybackInfo_SubtitlesOff(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID, `{"SubtitleStreamIndex":-1}`)

	if resp.MediaSources[0].DefaultSubtitleStreamIndex != nil {
		t.Fatalf("expected DefaultSubtitleStreamIndex to be unset, got %d", *resp.MediaSources[0].DefaultSubtitleStreamIndex)
	}
	if index, found := defaultSubtitleStreamFromResponse(t, resp); found {
		t.Fatalf("expected no default subtitle stream, got index %d", index)
	}
}

func TestDownloadedSubtitleAlwaysBurnTransportConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"direct original remains available", `{"SubtitleStreamIndex":5,"AlwaysBurnInSubtitleWhenTranscoding":true}`, 200},
		{"progressive video copy remains available", `{"SubtitleStreamIndex":5,"EnableDirectPlay":false,"AlwaysBurnInSubtitleWhenTranscoding":true}`, 200},
		{"bitrate ceiling cannot retain direct stream", `{"SubtitleStreamIndex":5,"MaxStreamingBitrate":4000000,"AlwaysBurnInSubtitleWhenTranscoding":true}`, 400},
		{"unsupported source video cannot retain direct stream", `{"SubtitleStreamIndex":5,"AlwaysBurnInSubtitleWhenTranscoding":true,"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","VideoCodec":"hevc","AudioCodec":"aac"}],"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":"ts","VideoCodec":"h264","AudioCodec":"aac"}]}}`, 400},
		{"external text during ordinary full encode", `{"SubtitleStreamIndex":5,"EnableDirectPlay":false,"AllowVideoStreamCopy":false}`, 200},
		{"mandatory burn cannot use downloaded text in full encode", `{"SubtitleStreamIndex":5,"EnableDirectPlay":false,"AllowVideoStreamCopy":false,"AlwaysBurnInSubtitleWhenTranscoding":true}`, 400},
		{"video copy remux needs no burn", `{"SubtitleStreamIndex":5,"EnableDirectPlay":false,"AlwaysBurnInSubtitleWhenTranscoding":true,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":"mp4","VideoCodec":"h264","AudioCodec":"aac"}]}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, item := newSubtitleSelectionHandler(t)
			detail := h.content.(*stubContentService).detail
			detail.Versions[0].CodecVideo = "h264"
			detail.Versions[0].CodecAudio = "aac"
			req := httptest.NewRequest("POST", "/Items/"+item+"/PlaybackInfo", strings.NewReader(tc.body))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", item)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
			rr := httptest.NewRecorder()
			h.HandlePlaybackInfo(rr, req.WithContext(ctx))
			if rr.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 400 {
				if !strings.Contains(rr.Body.String(), "PlaybackUnavailable") {
					t.Fatalf("error: %s", rr.Body.String())
				}
				if _, _, ok := h.playbackStore.FindByRoute("token-1", item); ok {
					t.Fatal("stored unplayable negotiation")
				}
				return
			}
			var response playbackInfoResponseDTO
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			source := response.MediaSources[0]
			if tc.name == "progressive video copy remains available" {
				session, ok := h.playbackStore.Get(response.PlaySessionID)
				if !ok || session.MediaSources[0].HLSRemux || !source.SupportsDirectStream || source.SupportsTranscoding || !strings.Contains(source.DirectStreamURL, "static=false") {
					t.Fatalf("progressive copy: %+v", source)
				}
			}
			if source.DirectStreamURL == "" && source.TranscodingURL == "" {
				t.Fatalf("no transport: %+v", source)
			}
			index, found := defaultSubtitleStreamFromResponse(t, response)
			if !found || index != 5 {
				t.Fatalf("selection %d %v", index, found)
			}
		})
	}
}

func TestPlaybackInfoSidecarRequiresExternalSubtitleDelivery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		index    int
		profiles string
		status   int
	}{
		{"sidecar cannot be embedded in original", 3, `[{"Format":"srt","Method":"Embed"}]`, 400},
		{"sidecar delivered externally", 3, `[{"Format":"srt","Method":"External"}]`, 200},
		{"sidecar client accepts both", 3, `[{"Format":"srt","Method":"Embed"},{"Format":"srt","Method":"External"}]`, 200},
		{"embedded original retains embed support", 2, `[{"Format":"srt","Method":"Embed"}]`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, item := newSubtitleSelectionHandler(t)
			body := fmt.Sprintf(`{"SubtitleStreamIndex":%d,"DeviceProfile":{"SubtitleProfiles":%s}}`, tc.index, tc.profiles)
			req := httptest.NewRequest("POST", "/Items/"+item+"/PlaybackInfo", strings.NewReader(body))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", item)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
			rr := httptest.NewRecorder()
			h.HandlePlaybackInfo(rr, req.WithContext(ctx))
			if rr.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 400 {
				if !strings.Contains(rr.Body.String(), "PlaybackUnavailable") {
					t.Fatalf("error: %s", rr.Body.String())
				}
				if _, _, ok := h.playbackStore.FindByRoute("token-1", item); ok {
					t.Fatal("persisted unusable subtitle negotiation")
				}
				return
			}
			var response playbackInfoResponseDTO
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			source := response.MediaSources[0]
			if !source.SupportsDirectPlay || source.DirectStreamURL == "" {
				t.Fatalf("valid direct playback lost: %+v", source)
			}
			index, found := defaultSubtitleStreamFromResponse(t, response)
			if !found || index != tc.index {
				t.Fatalf("selection %d %v", index, found)
			}
			if tc.index == 3 {
				for _, stream := range source.MediaStreams {
					if stream.Type == "Subtitle" && stream.Index == 3 && (stream.DeliveryURL == "" || stream.DeliveryMethod != "External") {
						t.Fatalf("sidecar delivery: %+v", stream)
					}
				}
			}
		})
	}
}
