package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
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
		{
			name: "remux-v1 route rejects MPEG-TS remux",
			source: PlaybackMediaSource{
				ID: "source-1", FileID: 42, HLSRemux: true, HLSRemuxMPEGTS: true,
				HLSRemuxAudioStreamIndexes: []int{1},
				Version:                    testCompatVersion(),
			},
			serve:      (*PlaybackHandler).HandleRemuxV1MasterManifest,
			requestURL: "/Videos/item-1/remux-v1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name: "audio-v2 route rejects MPEG-TS remux",
			source: PlaybackMediaSource{
				ID: "source-1", FileID: 42, HLSRemux: true, HLSRemuxMPEGTS: true, TranscodeAudio: true,
				Version: testCompatVersion(),
			},
			serve:      (*PlaybackHandler).HandleAudioV2MasterManifest,
			requestURL: "/Videos/item-1/audio-v2/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name: "remux-v1 route rejects Dolby Vision strip",
			source: PlaybackMediaSource{
				ID: "source-1", FileID: 42, HLSRemux: true, DVStripToHDR10: true,
				HLSRemuxAudioStreamIndexes: []int{1},
				Version:                    testCompatVersion(),
			},
			serve:      (*PlaybackHandler).HandleRemuxV1MasterManifest,
			requestURL: "/Videos/item-1/remux-v1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name: "audio-v2 route rejects Dolby Vision strip",
			source: PlaybackMediaSource{
				ID: "source-1", FileID: 42, HLSRemux: true, TranscodeAudio: true, DVStripToHDR10: true,
				Version: testCompatVersion(),
			},
			serve:      (*PlaybackHandler).HandleAudioV2MasterManifest,
			requestURL: "/Videos/item-1/audio-v2/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name:       "remux-dv-v1 route rejects remux without the strip",
			source:     PlaybackMediaSource{ID: "source-1", FileID: 42, HLSRemux: true, Version: testCompatVersion()},
			serve:      (*PlaybackHandler).HandleRemuxDVV1MasterManifest,
			requestURL: "/Videos/item-1/remux-dv-v1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
		},
		{
			name:       "remux-ts-v1 route rejects fMP4 remux",
			source:     PlaybackMediaSource{ID: "source-1", FileID: 42, HLSRemux: true, Version: testCompatVersion()},
			serve:      (*PlaybackHandler).HandleRemuxTSV1MasterManifest,
			requestURL: "/Videos/item-1/remux-ts-v1/master.m3u8?PlaySessionId=play-1&MediaSourceId=source-1",
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

	dto := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", source)
	if dto.TranscodingURL != "" {
		t.Fatalf("direct-play source TranscodingURL = %q, want none so clients take the direct URL", dto.TranscodingURL)
	}
	transcodeOnly := source
	transcodeOnly.SupportsDirectPlay, transcodeOnly.SupportsDirectStream = false, false
	hlsDTO := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", transcodeOnly)
	if !strings.HasPrefix(hlsDTO.TranscodingURL, "/Videos/item-1/audio-v2/master.m3u8?") {
		t.Fatalf("surround-capable HLS URL = %q, want audio-v2 route", hlsDTO.TranscodingURL)
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
	stereo.SupportsDirectPlay, stereo.SupportsDirectStream = false, false
	stereoDTO := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-2", stereo)
	if !strings.HasPrefix(stereoDTO.TranscodingURL, "/Videos/item-1/master.m3u8?") {
		t.Fatalf("all-stereo HLS URL = %q, want legacy route", stereoDTO.TranscodingURL)
	}
}

func TestMediaSourceDTOEmitsDedicatedMPEGTSRemuxRoute(t *testing.T) {
	for _, transcodeAudio := range []bool{false, true} {
		source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
		source.SupportsDirectPlay, source.SupportsDirectStream = false, false
		source.HLSRemux = true
		source.HLSRemuxMPEGTS = true
		source.TranscodeAudio = transcodeAudio

		dto := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", source)
		if !strings.HasPrefix(dto.TranscodingURL, "/Videos/item-1/remux-ts-v1/master.m3u8?") {
			t.Fatalf("TranscodeAudio=%v MPEG-TS URL = %q, want dedicated remux-ts-v1 route", transcodeAudio, dto.TranscodingURL)
		}
	}
}

func TestMediaSourceDTOEmitsDedicatedDolbyVisionStripRoute(t *testing.T) {
	for _, transcodeAudio := range []bool{false, true} {
		source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
		source.SupportsDirectPlay, source.SupportsDirectStream = false, false
		source.HLSRemux = true
		source.TranscodeAudio = transcodeAudio
		source.DVStripToHDR10 = true

		dto := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", source)
		if !strings.HasPrefix(dto.TranscodingURL, "/Videos/item-1/remux-dv-v1/master.m3u8?") {
			t.Fatalf("TranscodeAudio=%v strip URL = %q, want dedicated remux-dv-v1 route", transcodeAudio, dto.TranscodingURL)
		}
	}
}

// A HEAD on the master playlist answers with the playlist's headers, or 410 for
// a play whose upstream session was denied, and starts nothing: the handler has
// no session manager or node planner, so reaching the transcode start would
// fail the request.
func TestMasterManifestHeadStartsNothing(t *testing.T) {
	tests := []struct {
		name     string
		denied   bool
		wantCode int
	}{
		{name: "live play", wantCode: http.StatusOK},
		{name: "denied upstream", denied: true, wantCode: http.StatusGone},
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
				fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: source.FileID}},
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
