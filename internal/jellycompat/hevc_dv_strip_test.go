package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDVStripKeepsNegotiatedHEVCForSubtitleBurnIn(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac", HDR: true,
		VideoTracks:    []models.VideoTrack{dvStripProfile8Track()},
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Channels: 2}},
		SubtitleTracks: []catalog.VersionSubtitleTrack{{Index: 2, Codec: "hdmv_pgs_subtitle"}},
	}
	profile := DeviceProfile{
		TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac"}},
		CodecProfiles: []CodecProfile{{Type: "Video", Codec: "hevc", Conditions: []ProfileCondition{
			{Property: "VideoRangeType", Condition: "EqualsAny", Value: "HDR10|SDR", IsRequired: true},
		}}},
	}
	h := &PlaybackHandler{
		codec:                   NewResourceIDCodec(),
		compatDVRPUProbe:        func(context.Context, string) bool { return true },
		compatDVStripLocalProbe: func() bool { return true },
	}
	req := playbackInfoRequest{EnableDirectPlay: boolPtr(false)}
	source := h.buildPlaybackSource("item", "play", version, profile, req, true, true)
	if source.TargetVideoCodec != "hevc" || !source.CanBurnSubtitle {
		t.Fatalf("initial HEVC encoding not available: %+v", source)
	}
	source = h.applyCompatDVStrip(t.Context(), "item", "play", source, profile, req, true)
	if !source.HLSRemux || !source.DVStripToHDR10 || compatSourceTargetVideoCodec(source) != "copy" {
		t.Fatalf("HDR10 strip remux not selected: %+v", source)
	}
	source.SelectedSubtitleStreamIndex = intPtr(2)
	applyCompatSubtitleDelivery(&source, profile, false)
	if !source.SupportsTranscoding || !source.SubtitleBurnIn || source.HLSRemux || compatSourceTargetVideoCodec(source) != "hevc" {
		t.Fatalf("subtitle burn-in lost negotiated HEVC: supported=%t burn=%t remux=%t codec=%q", source.SupportsTranscoding, source.SubtitleBurnIn, source.HLSRemux, compatSourceTargetVideoCodec(source))
	}
}
