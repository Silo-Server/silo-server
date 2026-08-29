package jellycompat

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
