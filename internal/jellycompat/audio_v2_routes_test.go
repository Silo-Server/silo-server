package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMasterManifestKeepsRemuxV1AndLegacySessionsIsolated(t *testing.T) {
	tests := []struct {
		name       string
		source     PlaybackMediaSource
		serve      func(*PlaybackHandler, http.ResponseWriter, *http.Request)
		requestURL string
	}{
		{
			name: "legacy route rejects audio-copy remux",
			source: PlaybackMediaSource{
				ID: "source-1", FileID: 42, HLSRemux: true,
				HLSRemuxAudioStreamIndexes: []int{1},
				Version:                    testCompatVersion(),
			},
			serve:      (*PlaybackHandler).HandleMasterManifest,
			requestURL: "/Videos/item-1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name: "remux-v1 route rejects legacy transcode",
			source: func() PlaybackMediaSource {
				source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
				source.ID = "source-1"
				for index := range source.Version.AudioTracks {
					source.Version.AudioTracks[index].Channels = 2
				}
				return source
			}(),
			serve:      (*PlaybackHandler).HandleRemuxV1MasterManifest,
			requestURL: "/Videos/item-1/remux-v1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
				MediaSources: []PlaybackMediaSource{tt.source},
			})
			handler := &PlaybackHandler{playbackStore: store}
			request := httptest.NewRequest(http.MethodGet, tt.requestURL, nil)
			request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "token-1"}))
			recorder := httptest.NewRecorder()

			tt.serve(handler, recorder, request)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s; want route isolation 404", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMediaSourceDTOEmitsStableAudioV2HLSRoute(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	// Start on stereo. The fixed HLS URL still has to be v2 because the client
	// can select the surround track without requesting PlaybackInfo again.
	source.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))

	dto := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", "token-1", source)
	if !strings.HasPrefix(dto.TranscodingURL, "/Videos/item-1/audio-v2/master.m3u8?") {
		t.Fatalf("surround-capable HLS URL = %q, want audio-v2 route", dto.TranscodingURL)
	}
	if !strings.HasPrefix(dto.DirectStreamURL, "/Videos/item-1/stream?static=true") {
		t.Fatalf("static direct URL = %q, want legacy direct-file route", dto.DirectStreamURL)
	}
	for _, stream := range dto.MediaStreams {
		if strings.Contains(stream.DeliveryURL, "/audio-v2/") {
			t.Fatalf("subtitle delivery URL was unnecessarily versioned: %q", stream.DeliveryURL)
		}
	}

	stereoVersion := version
	stereoVersion.AudioTracks = append([]models.AudioTrack(nil), version.AudioTracks...)
	for index := range stereoVersion.AudioTracks {
		stereoVersion.AudioTracks[index].Channels = 2
	}
	stereo := testCompatSource(NewResourceIDCodec(), stereoVersion)
	stereoDTO := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-2", "token-1", stereo)
	if !strings.HasPrefix(stereoDTO.TranscodingURL, "/Videos/item-1/master.m3u8?") {
		t.Fatalf("all-stereo HLS URL = %q, want legacy route", stereoDTO.TranscodingURL)
	}
}
