package jellycompat

import (
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEncodedAudioBitrateConditions(t *testing.T) {
	version := testCompatVersion()
	version.CodecVideo = "h264"
	version.CodecAudio = "eac3"
	version.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6}}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, container := range []string{"ts", "mp4"} {
		for _, conditionScope := range []string{"codec", "transcoding"} {
			for _, tc := range []struct {
				name      string
				channels  int
				condition string
				bitrate   int
				want      bool
			}{
				{"mono within ceiling", 1, "LessThanEqual", 128000, true},
				{"mono below minimum", 1, "GreaterThanEqual", 192000, false},
				{"stereo within ceiling", 2, "LessThanEqual", 192000, true},
				{"stereo exceeds ceiling", 2, "LessThanEqual", 128000, false},
			} {
				t.Run(container+"/"+conditionScope+"/"+tc.name, func(t *testing.T) {
					profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{
						Type: "Video", Protocol: "hls", Container: container, VideoCodec: "h264", AudioCodec: "aac",
					}}}
					condition := ProfileCondition{Property: "AudioBitrate", Condition: tc.condition, Value: strconv.Itoa(tc.bitrate), IsRequired: true}
					if conditionScope == "codec" {
						profile.CodecProfiles = []CodecProfile{{Type: "VideoAudio", Codec: "aac", Conditions: []ProfileCondition{condition}}}
					} else {
						profile.TranscodingProfiles[0].Conditions = []ProfileCondition{condition}
					}
					source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{
						EnableDirectPlay: new(false), MaxAudioChannels: tc.channels,
					}, true)
					if source.SupportsTranscoding != tc.want {
						t.Fatalf("transcoding=%v, want %v for %+v", source.SupportsTranscoding, tc.want, condition)
					}
					if tc.want && (source.HLSRemux != (container == "mp4") || source.TargetAudioChannels != tc.channels) {
						t.Fatalf("wrong output: remux=%v channels=%d", source.HLSRemux, source.TargetAudioChannels)
					}
				})
			}
		}
	}
}

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

func TestEncodedOutputConditionsDoNotUseSourceCodecFacts(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac",
		Bitrate: 10000, Resolution: "1080p", HDR: true,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153,
			Width: 1920, Height: 1080, BitDepth: 10, VideoRangeType: "HDR10", ReferenceFrames: 8}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	for _, tc := range []struct {
		name      string
		condition ProfileCondition
		bitrate   int64
		want      bool
	}{
		{"source codec level", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "51"}, 0, true},
		{"source reference frames", ProfileCondition{Condition: "LessThanEqual", Property: "RefFrames", Value: "4"}, 0, true},
		{"tone mapped range", ProfileCondition{Condition: "Equals", Property: "VideoRangeType", Value: "SDR", IsRequired: true}, 0, true},
		{"scaled width", ProfileCondition{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true}, 4000000, true},
		{"scaled height", ProfileCondition{Condition: "LessThanEqual", Property: "Height", Value: "720", IsRequired: true}, 4000000, true},
		{"known width exceeds constraint", ProfileCondition{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true}, 0, false},
		{"required encoder level unknown", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "51", IsRequired: true}, 0, false},
		{"required encoder profile unknown", ProfileCondition{Condition: "EqualsAny", Property: "VideoProfile", Value: "high|main|baseline", IsRequired: true}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := DeviceProfile{
				TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"}},
				CodecProfiles:       []CodecProfile{{Type: "Video", Codec: "h264", Conditions: []ProfileCondition{tc.condition}}},
			}
			h := &PlaybackHandler{codec: NewResourceIDCodec()}
			source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{
				EnableDirectPlay: new(false), EnableDirectStream: new(false), MaxStreamingBitrate: tc.bitrate,
			}, true)
			if source.SupportsTranscoding != tc.want || source.CanBurnSubtitle != tc.want {
				t.Fatalf("transcoding=%v burn=%v, want %v for %+v", source.SupportsTranscoding, source.CanBurnSubtitle, tc.want, tc.condition)
			}
		})
	}
}

func TestPlaybackBitrateResolutionDoesNotUpscale(t *testing.T) {
	for _, tc := range []struct {
		name       string
		height     int
		resolution string
	}{
		{"smaller source", 480, ""},
		{"matching source", 720, ""},
		{"larger source", 1080, "720p"},
		{"unknown source", 0, "720p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := testCompatVersion()
			version.VideoTracks[0].Height = tc.height
			h := &PlaybackHandler{codec: NewResourceIDCodec()}
			source := h.buildPlaybackSource("item", "play", version, DefaultDeviceProfile(), playbackInfoRequest{MaxStreamingBitrate: 4000000}, true)
			if !source.SupportsTranscoding || source.TargetResolution != tc.resolution {
				t.Fatalf("height=%d: transcoding=%v resolution=%q, want %q", tc.height, source.SupportsTranscoding, source.TargetResolution, tc.resolution)
			}
		})
	}
}
