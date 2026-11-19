package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMonoPlaybackBitrateBudget(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := h.buildPlaybackSource("item", "play", testCompatVersion(), DefaultDeviceProfile(), playbackInfoRequest{
		EnableDirectPlay: new(false), EnableDirectStream: new(false), MaxAudioChannels: 1, MaxStreamingBitrate: 4000000,
	}, true)
	// The 4 Mbps ceiling reserves 5% for mux overhead and 128 kbps for mono AAC.
	if !source.SupportsTranscoding || source.TargetBitrateKbps != 3672 {
		t.Fatalf("transcoding=%v video bitrate=%d, want 3672 kbps", source.SupportsTranscoding, source.TargetBitrateKbps)
	}
}

// An encode is sized by the source, not by a client's generous ceiling, but
// only a ceiling the source really exceeds is reported as a bitrate problem or
// lowers the resolution.
func TestEncodeTargetStopsAtTheSourceRate(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tc := range []struct {
		name           string
		codec          string
		sourceKbps     int
		videoKbps      int
		maxBitrate     int64
		wantKbps       int
		wantResolution string
		wantReason     string
	}{
		{name: "ceiling far above the source", codec: "mpeg2video", sourceKbps: 23_400, maxBitrate: 120_000_000, wantKbps: 23_400, wantReason: "VideoCodecNotSupported"},
		{name: "the video track's rate leaves out the audio", codec: "mpeg2video", sourceKbps: 7000, videoKbps: 2900, maxBitrate: 120_000_000, wantKbps: 2900, wantReason: "VideoCodecNotSupported"},
		{name: "a track rate above the file's is ignored", codec: "mpeg2video", sourceKbps: 9000, videoKbps: 38_000, maxBitrate: 120_000_000, wantKbps: 9000, wantReason: "VideoCodecNotSupported"},
		{name: "a track rate far below the file's is ignored", codec: "mpeg2video", sourceKbps: 20_000, videoKbps: 50, maxBitrate: 120_000_000, wantKbps: 20_000, wantReason: "VideoCodecNotSupported"},
		{name: "a tiny source stops at the encoder's minimum", codec: "mpeg2video", sourceKbps: 30, maxBitrate: 120_000_000, wantKbps: compatMinEncodeKbps, wantReason: "VideoCodecNotSupported"},
		{name: "low-bitrate source keeps its size", codec: "mpeg2video", sourceKbps: 1500, maxBitrate: 120_000_000, wantKbps: 1500, wantReason: "VideoCodecNotSupported"},
		{name: "HEVC source gets H.264 headroom", codec: "hevc", sourceKbps: 6000, maxBitrate: 120_000_000, wantKbps: 10_000, wantReason: "VideoCodecNotSupported"},
		{name: "ceiling below the source still wins", codec: "mpeg2video", sourceKbps: 23_400, maxBitrate: 8_820_000, wantKbps: 8187, wantReason: "VideoBitrateNotSupported"},
		{name: "unknown source rate targets the ceiling", codec: "mpeg2video", maxBitrate: 8_820_000, wantKbps: 8187, wantReason: "VideoCodecNotSupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := catalog.FileVersion{
				FileID: 42, Container: "mkv", CodecVideo: tc.codec, CodecAudio: "aac", Bitrate: tc.sourceKbps, Resolution: "1080p",
				VideoTracks: []models.VideoTrack{{Codec: tc.codec, Width: 1920, Height: 1080, FrameRate: "24", Bitrate: tc.videoKbps}},
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
			}
			profile := DeviceProfile{
				TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"}},
			}
			req := playbackInfoRequest{EnableDirectPlay: new(false), EnableDirectStream: new(false), MaxStreamingBitrate: tc.maxBitrate}
			source := h.buildPlaybackSource("item", "play", version, profile, req, true)
			if !source.SupportsTranscoding || source.TargetBitrateKbps != tc.wantKbps || source.TargetResolution != tc.wantResolution {
				t.Fatalf("transcoding=%v target=%d kbps at %q, want %d kbps at %q", source.SupportsTranscoding, source.TargetBitrateKbps, source.TargetResolution, tc.wantKbps, tc.wantResolution)
			}
			if reasons := compatTranscodeReasons(source); reasons[0] != tc.wantReason {
				t.Fatalf("reasons = %v, want %s", reasons, tc.wantReason)
			}
		})
	}
}
