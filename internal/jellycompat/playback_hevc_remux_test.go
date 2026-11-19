package jellycompat

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestHLSRemuxCodecProfileKeepsLiteralContainerListWithHLSToken(t *testing.T) {
	// Jellyfin substitutes SubContainer only when Container is exactly "hls".
	// A multi-token list containing hls keeps its literal token list, so this
	// incompatible profile never applies to the MP4 remux output.
	profile, err := decodeDeviceProfile(strings.NewReader(`{
		"TranscodingProfiles": [{
			"Type": "Video", "Protocol": "hls", "Container": "mp4", "VideoCodec": "hevc", "AudioCodec": "eac3"
		}],
		"CodecProfiles": [{
			"Type": "Video", "Container": "hls,dash", "SubContainer": "mp4", "Codec": "hevc",
			"Conditions": [{
				"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": "SDR", "IsRequired": false
			}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	if !profile.SupportsHLSRemuxForAudioStream(version, defaultAudioStreamIndex(version)) {
		t.Fatal("HLS MP4 remux was rejected by an hls,dash-scoped codec profile that Jellyfin would never apply to MP4 output")
	}
}

func TestBuildPlaybackSourceHLSRemuxTranscodesAudioWhenCopyIsDisabled(t *testing.T) {
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type: "Video", Container: "mp4", VideoCodec: "hevc", AudioCodec: "eac3",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
		}},
	}
	version := catalog.FileVersion{
		FileID: 42, Resolution: "2160p", Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8, VideoRangeType: "DOVIWithHDR10"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := h.buildPlaybackSource(
		"item",
		"play",
		version,
		profile,
		playbackInfoRequest{AllowAudioStreamCopy: boolPtr(false)},
		false,
	)
	if source.SupportsDirectPlay || source.SupportsDirectStream {
		t.Fatalf("source unexpectedly supports direct playback: direct=%v stream=%v", source.SupportsDirectPlay, source.SupportsDirectStream)
	}
	if !source.HLSRemux || !source.TranscodeAudio || !source.SupportsTranscoding {
		t.Fatalf(
			"source route = remux %v transcode-audio %v supports-transcoding %v, want video-copy HLS with AAC audio transcode",
			source.HLSRemux,
			source.TranscodeAudio,
			source.SupportsTranscoding,
		)
	}

	dto := h.mediaSourceDTO("item", "play", source)
	if dto.TranscodingURL == "" || dto.TranscodingContainer != "mp4" {
		t.Fatalf("transcoding route = URL %q container %q, want usable MP4 HLS route", dto.TranscodingURL, dto.TranscodingContainer)
	}
}
