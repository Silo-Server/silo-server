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
		// An H.264 encode is High profile at the level its frame size, rate and
		// bitrate need, never below the streaming libx264 pin of 4.1; here 4.1.
		{"required level the encode meets", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "51", IsRequired: true}, 0, true},
		{"required level below the encode", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "40", IsRequired: true}, 0, false},
		{"required profile the encode meets", ProfileCondition{Condition: "EqualsAny", Property: "VideoProfile", Value: "high|main|baseline", IsRequired: true}, 0, true},
		{"required baseline profile", ProfileCondition{Condition: "Equals", Property: "VideoProfile", Value: "baseline", IsRequired: true}, 0, false},
		// A 720p encode at 4 Mbps fits level 3.1, but libx264 writes 4.1.
		{"required level below the libx264 pin", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "31", IsRequired: true}, 4000000, false},
		// An optional profile or level rule is an encode target, as in Jellyfin.
		// At 120 Mbps the encode is stated at level 5.1, above an optional 4.2.
		{"optional level below the encode", ProfileCondition{Condition: "LessThanEqual", Property: "VideoLevel", Value: "42"}, 120_000_000, true},
		{"optional baseline profile", ProfileCondition{Condition: "Equals", Property: "VideoProfile", Value: "baseline"}, 0, true},
		// The encoder chooses reference frames, so a rule on them is an encode
		// target either way, as in Jellyfin.
		{"required reference frame limit", ProfileCondition{Condition: "LessThanEqual", Property: "RefFrames", Value: "4", IsRequired: true}, 0, true},
		{"required reference frame minimum", ProfileCondition{Condition: "GreaterThanEqual", Property: "RefFrames", Value: "2", IsRequired: true}, 0, true},
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
		name          string
		width, height int
		resolution    string
	}{
		{"smaller source", 854, 480, ""},
		{"matching source", 1280, 720, ""},
		{"larger source", 1920, 1080, "720p"},
		{"scope source keeps its shape", 1920, 800, "532p"},
		{"unknown source", 0, 0, "720p"},
		{"height known, width not, fits", 0, 480, ""},
		{"height known, width not, too tall", 0, 1080, "720p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := testCompatVersion()
			version.VideoTracks[0].Width, version.VideoTracks[0].Height = tc.width, tc.height
			h := &PlaybackHandler{codec: NewResourceIDCodec()}
			source := h.buildPlaybackSource("item", "play", version, DefaultDeviceProfile(), playbackInfoRequest{MaxStreamingBitrate: 4000000}, true)
			if !source.SupportsTranscoding || source.TargetResolution != tc.resolution {
				t.Fatalf("height=%d: transcoding=%v resolution=%q, want %q", tc.height, source.SupportsTranscoding, source.TargetResolution, tc.resolution)
			}
		})
	}
}

// A transcoding profile's own RefFrames rule describes the same encode as a
// codec profile's, so an H.264 encode treats it as an encode target too and
// an HEVC encode fails it closed.
func TestTranscodingProfileRefFramesRuleOnEncode(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "vp9", CodecAudio: "aac", Bitrate: 8000, Resolution: "1080p",
		VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	refFrames := []ProfileCondition{{Condition: "LessThanEqual", Property: "RefFrames", Value: "4", IsRequired: true}}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{EnableDirectPlay: new(false), AllowVideoStreamCopy: new(false)}

	profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{
		{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Conditions: refFrames},
	}}
	if source := h.buildPlaybackSource("item", "play", version, profile, req, true); !source.SupportsTranscoding {
		t.Fatal("SupportsTranscoding = false for an H.264 encode under a transcoding profile's RefFrames rule")
	}

	profile = DeviceProfile{TranscodingProfiles: []TranscodingProfile{
		{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac", Conditions: refFrames},
		{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"},
	}}
	if source := h.buildPlaybackSource("item", "play", version, profile, req, true, true); !source.SupportsTranscoding || source.TargetVideoCodec != "h264" {
		t.Fatalf("transcoding=%v codec=%q, want an H.264 fallback from the HEVC profile's RefFrames rule", source.SupportsTranscoding, source.TargetVideoCodec)
	}
}

// The streaming encoder's maximum rate is the requested cap, not the source's
// rate, so a client's level rule is checked at the cap: a 4,000 kbps source
// capped near 60,000 kbps needs level 5.0, above a required 4.1.
func TestH264EncodeLevelUsesTheConfiguredRate(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Bitrate: 4000, Resolution: "1080p",
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 40, Width: 1920, Height: 1080, FrameRate: "24"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	profile := DeviceProfile{
		TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"}},
		CodecProfiles: []CodecProfile{{Type: "Video", Codec: "h264", Conditions: []ProfileCondition{
			{Condition: "LessThanEqual", Property: "VideoLevel", Value: "41", IsRequired: true},
		}}},
	}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{EnableDirectPlay: new(false), EnableDirectStream: new(false), MaxStreamingBitrate: 64_000_000}
	source := h.buildPlaybackSource("item", "play", version, profile, req, true)
	if source.TargetBitrateKbps < 60_000 {
		t.Fatalf("target rate %d kbps, want a cap of at least 60,000", source.TargetBitrateKbps)
	}
	if source.SupportsTranscoding {
		t.Fatalf("SupportsTranscoding = true: a %d kbps cap needs level 5.0, above the required 4.1", source.TargetBitrateKbps)
	}

	// Uncapped, the encode is estimated at the source's rate: level 4.0, so 4.1.
	req.MaxStreamingBitrate = 0
	if source := h.buildPlaybackSource("item", "play", version, profile, req, true); !source.SupportsTranscoding {
		t.Fatal("SupportsTranscoding = false for an uncapped encode of a 4,000 kbps 1080p24 source")
	}
}
