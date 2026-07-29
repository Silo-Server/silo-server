package jellycompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDecodeDeviceProfileKeepsCodecProfiles(t *testing.T) {
	profile, err := decodeDeviceProfile(strings.NewReader(`{
		"DeviceProfile": {
			"CodecProfiles": [{
				"Type": "Video",
				"Codec": "hevc",
				"Conditions": [{
					"Condition": "LessThanEqual",
					"Property": "VideoLevel",
					"Value": 153,
					"IsRequired": false
				}],
				"ApplyConditions": [{
					"Condition": "EqualsAny",
					"Property": "VideoProfile",
					"Value": "main 10"
				}]
			}]
		}
	}`))
	if err != nil {
		t.Fatalf("decodeDeviceProfile: %v", err)
	}
	if !profile.HasData() {
		t.Fatal("HasData = false, want true for CodecProfiles-only payload")
	}
	if len(profile.CodecProfiles) != 1 {
		t.Fatalf("CodecProfiles length = %d, want 1", len(profile.CodecProfiles))
	}
	condition := profile.CodecProfiles[0].Conditions[0]
	if condition.Value != "153" {
		t.Fatalf("condition Value = %q, want numeric value stringified", condition.Value)
	}
}

func TestBuildPlaybackSourceCodecProfiles(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	baseVersion := catalog.FileVersion{
		FileID:      1,
		Resolution:  "1080p",
		Container:   "mkv",
		CodecVideo:  "hevc",
		CodecAudio:  "truehd",
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153, Width: 1920, Height: 1080, BitDepth: 10}},
		AudioTracks: []models.AudioTrack{{Codec: "truehd", Channels: 8, Default: true}},
	}
	directProfile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type:       "Video",
			Container:  "mkv",
			VideoCodec: "hevc",
			AudioCodec: "truehd",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type:       "Video",
			Protocol:   "hls",
			Container:  "ts",
			VideoCodec: "h264",
			AudioCodec: "aac",
		}},
	}

	tests := []struct {
		name               string
		version            catalog.FileVersion
		codecProfiles      []CodecProfile
		wantDirectPlay     bool
		wantDirectStream   bool
		wantTranscoding    bool
		wantTranscodeAudio bool
	}{
		{
			name: "unsupported dovi enhancement layer blocks video copy",
			version: withVideoTrack(baseVersion, models.VideoTrack{
				Codec:          "hevc",
				Profile:        "Main 10",
				Level:          153,
				Width:          3840,
				Height:         2160,
				BitDepth:       10,
				VideoRangeType: "DOVIWithEL",
			}, "2160p"),
			codecProfiles:      []CodecProfile{unsupportedRangeProfile("hevc", "DOVIInvalid|DOVIWithEL|DOVIWithELHDR10Plus")},
			wantDirectPlay:     false,
			wantDirectStream:   false,
			wantTranscoding:    true,
			wantTranscodeAudio: false,
		},
		{
			name: "dolby vision profile 8 hdr10 is direct playable when not excluded",
			version: withVideoTrack(baseVersion, models.VideoTrack{
				Codec:          "hevc",
				Profile:        "Main 10",
				Level:          153,
				Width:          3840,
				Height:         2160,
				BitDepth:       10,
				VideoRangeType: "DOVIWithHDR10",
			}, "2160p"),
			codecProfiles:      []CodecProfile{unsupportedRangeProfile("hevc", "DOVIInvalid|DOVIWithEL|DOVIWithELHDR10Plus")},
			wantDirectPlay:     true,
			wantDirectStream:   true,
			wantTranscoding:    true,
			wantTranscodeAudio: false,
		},
		{
			name: "audio channel limit preserves video copy and transcodes audio",
			codecProfiles: []CodecProfile{{
				Type: "VideoAudio",
				Conditions: []ProfileCondition{{
					Condition: "LessThanEqual",
					Property:  "AudioChannels",
					Value:     "2",
				}},
			}},
			wantDirectPlay:     false,
			wantDirectStream:   false,
			wantTranscoding:    true,
			wantTranscodeAudio: true,
		},
		{
			name: "hevc level limit blocks video copy",
			codecProfiles: []CodecProfile{{
				Type:  "Video",
				Codec: "hevc",
				Conditions: []ProfileCondition{{
					Condition: "LessThanEqual",
					Property:  "VideoLevel",
					Value:     "150",
				}},
				ApplyConditions: []ProfileCondition{{
					Condition: "Equals",
					Property:  "VideoProfile",
					Value:     "main 10",
				}},
			}},
			wantDirectPlay:     false,
			wantDirectStream:   false,
			wantTranscoding:    true,
			wantTranscodeAudio: false,
		},
		{
			name: "width limit blocks video copy",
			codecProfiles: []CodecProfile{{
				Type:  "Video",
				Codec: "hevc",
				Conditions: []ProfileCondition{{
					Condition: "LessThanEqual",
					Property:  "Width",
					Value:     "1280",
				}},
			}},
			wantDirectPlay:     false,
			wantDirectStream:   false,
			wantTranscoding:    true,
			wantTranscodeAudio: false,
		},
		{
			name: "bit depth limit blocks video copy",
			codecProfiles: []CodecProfile{{
				Type:  "Video",
				Codec: "hevc",
				Conditions: []ProfileCondition{{
					Condition: "LessThanEqual",
					Property:  "VideoBitDepth",
					Value:     "8",
				}},
			}},
			wantDirectPlay:     false,
			wantDirectStream:   false,
			wantTranscoding:    true,
			wantTranscodeAudio: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version := tt.version
			if version.FileID == 0 {
				version = baseVersion
			}
			profile := directProfile
			profile.CodecProfiles = tt.codecProfiles

			source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
			if source.SupportsDirectPlay != tt.wantDirectPlay {
				t.Fatalf("SupportsDirectPlay = %v, want %v", source.SupportsDirectPlay, tt.wantDirectPlay)
			}
			if source.SupportsDirectStream != tt.wantDirectStream {
				t.Fatalf("SupportsDirectStream = %v, want %v", source.SupportsDirectStream, tt.wantDirectStream)
			}
			if source.SupportsTranscoding != tt.wantTranscoding {
				t.Fatalf("SupportsTranscoding = %v, want %v", source.SupportsTranscoding, tt.wantTranscoding)
			}
			if source.TranscodeAudio != tt.wantTranscodeAudio {
				t.Fatalf("TranscodeAudio = %v, want %v", source.TranscodeAudio, tt.wantTranscodeAudio)
			}
		})
	}
}

func TestBuildPlaybackSourceCodecProfiles_UnsupportedDOVIWithELRespects4KGate(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	version := catalog.FileVersion{
		FileID:     1,
		Resolution: "2160p",
		Container:  "mkv",
		CodecVideo: "hevc",
		CodecAudio: "truehd",
		VideoTracks: []models.VideoTrack{{
			Codec:          "hevc",
			Profile:        "Main 10",
			Level:          153,
			Width:          3840,
			Height:         2160,
			BitDepth:       10,
			VideoRangeType: "DOVIWithEL",
		}},
		AudioTracks: []models.AudioTrack{{Codec: "truehd", Channels: 8, Default: true}},
	}
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type:       "Video",
			Container:  "mkv",
			VideoCodec: "hevc",
			AudioCodec: "truehd",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type:       "Video",
			Protocol:   "hls",
			Container:  "ts",
			VideoCodec: "h264",
			AudioCodec: "aac",
		}},
		CodecProfiles: []CodecProfile{unsupportedRangeProfile("hevc", "DOVIWithEL")},
	}

	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, false)
	if source.SupportsDirectPlay || source.SupportsDirectStream || source.SupportsTranscoding {
		t.Fatalf("source supports playback unexpectedly: direct=%v stream=%v transcode=%v", source.SupportsDirectPlay, source.SupportsDirectStream, source.SupportsTranscoding)
	}
}

func TestBuildMediaStreamsUsesJellyfinVideoRangeType(t *testing.T) {
	version := catalog.FileVersion{
		HDR: true,
		VideoTracks: []models.VideoTrack{{
			Codec:       "hevc",
			DolbyVision: "Profile 7",
			VideoRange:  "DolbyVision",
		}},
	}

	streams := buildMediaStreams("item", "source", version)
	if len(streams) != 1 {
		t.Fatalf("streams length = %d, want 1", len(streams))
	}
	if streams[0].VideoRange != "HDR" {
		t.Fatalf("VideoRange = %q, want HDR", streams[0].VideoRange)
	}
	if streams[0].VideoRangeType != "DOVIWithEL" {
		t.Fatalf("VideoRangeType = %q, want DOVIWithEL", streams[0].VideoRangeType)
	}
}

func TestBuildMediaStreamsPreservesColorRange(t *testing.T) {
	tests := []struct {
		name           string
		colorRange     string
		wantColorRange string
		wantJSONField  bool
	}{
		{name: "limited", colorRange: "tv", wantColorRange: "tv", wantJSONField: true},
		{name: "full", colorRange: "pc", wantColorRange: "pc", wantJSONField: true},
		{name: "internal unknown sentinel", colorRange: "unknown"},
		{name: "missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version := catalog.FileVersion{
				VideoTracks: []models.VideoTrack{{
					Codec:      "h264",
					ColorRange: tt.colorRange,
				}},
			}

			streams := buildMediaStreams("item", "source", version)
			if len(streams) != 1 {
				t.Fatalf("streams length = %d, want 1", len(streams))
			}
			if got := streams[0].ColorRange; got != tt.wantColorRange {
				t.Fatalf("ColorRange = %q, want %q", got, tt.wantColorRange)
			}

			payload, err := json.Marshal(streams[0])
			if err != nil {
				t.Fatalf("marshal media stream: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("unmarshal media stream: %v", err)
			}
			got, present := decoded["ColorRange"]
			if present != tt.wantJSONField {
				t.Fatalf("JSON ColorRange present = %v, want %v (value %#v)", present, tt.wantJSONField, got)
			}
			if tt.wantJSONField && got != tt.wantColorRange {
				t.Fatalf("JSON ColorRange = %#v, want %q", got, tt.wantColorRange)
			}
		})
	}
}

func TestCodecProfileAVCRefFramesConstraint(t *testing.T) {
	version := catalog.FileVersion{
		FileID:     1,
		Resolution: "1080p",
		Container:  "mp4",
		CodecVideo: "h264",
		CodecAudio: "aac",
		VideoTracks: []models.VideoTrack{{
			Codec:           "h264",
			Profile:         "High",
			ReferenceFrames: 8,
			Width:           1920,
			Height:          1080,
		}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Default: true}},
	}
	profile := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{
			Type:       "Video",
			Container:  "mp4",
			VideoCodec: "h264",
			AudioCodec: "aac",
		}},
		TranscodingProfiles: []TranscodingProfile{{
			Type:       "Video",
			Protocol:   "hls",
			Container:  "ts",
			VideoCodec: "h264",
			AudioCodec: "aac",
		}},
		CodecProfiles: []CodecProfile{{
			Type:  "Video",
			Codec: "h264",
			Conditions: []ProfileCondition{{
				Condition: "LessThanEqual",
				Property:  "RefFrames",
				Value:     "4",
			}},
			ApplyConditions: []ProfileCondition{{
				Condition: "GreaterThanEqual",
				Property:  "Width",
				Value:     "1900",
			}},
		}},
	}

	source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
	if source.SupportsDirectPlay || source.SupportsDirectStream {
		t.Fatalf("video copy was allowed unexpectedly: direct=%v stream=%v", source.SupportsDirectPlay, source.SupportsDirectStream)
	}
	if !source.SupportsTranscoding {
		t.Fatal("SupportsTranscoding = false, want true")
	}
}

func withVideoTrack(version catalog.FileVersion, track models.VideoTrack, resolution string) catalog.FileVersion {
	version.VideoTracks = []models.VideoTrack{track}
	version.Resolution = resolution
	return version
}

func unsupportedRangeProfile(codec, ranges string) CodecProfile {
	return CodecProfile{
		Type:  "Video",
		Codec: codec,
		Conditions: []ProfileCondition{{
			Condition: "NotEquals",
			Property:  "VideoRangeType",
			Value:     ranges,
		}},
		ApplyConditions: []ProfileCondition{{
			Condition: "EqualsAny",
			Property:  "VideoRangeType",
			Value:     ranges,
		}},
	}
}
