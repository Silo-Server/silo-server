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

func TestMatchesCodecProfileContainerUsesJellyfinLiteralTokens(t *testing.T) {
	tests := []struct {
		name              string
		profileContainers string
		inputContainers   string
		want              bool
	}{
		{name: "asterisk is a literal", profileContainers: "*", inputContainers: "mkv", want: false},
		{name: "ts does not alias mpegts", profileContainers: "ts", inputContainers: "mpegts", want: false},
		{name: "negative ts allows mpegts", profileContainers: "-ts", inputContainers: "mpegts", want: true},
		{name: "case insensitive literal", profileContainers: "MKV", inputContainers: "mkv", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesCodecProfileContainer(tt.profileContainers, tt.inputContainers); got != tt.want {
				t.Fatalf("matchesCodecProfileContainer(%q, %q) = %v, want %v",
					tt.profileContainers, tt.inputContainers, got, tt.want)
			}
		})
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

func TestBuildPlaybackSourceCodecProfiles_WebOSAnamorphicCondition(t *testing.T) {
	tests := []struct {
		name             string
		version          catalog.FileVersion
		directProfile    DirectPlayProfile
		codecProfile     CodecProfile
		allow4KTranscode bool
		wantTranscoding  bool
	}{
		{
			name: "non-anamorphic h264 remains directly playable",
			version: catalog.FileVersion{
				FileID:      1,
				Resolution:  "1080p",
				Container:   "mp4",
				CodecVideo:  "h264",
				CodecAudio:  "aac",
				VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 42, Width: 1920, Height: 1080, VideoRangeType: "SDR"}},
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Default: true}},
			},
			directProfile: DirectPlayProfile{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"},
			codecProfile: CodecProfile{
				Type:  "Video",
				Codec: "h264",
				Conditions: []ProfileCondition{
					{Condition: "NotEquals", Property: "IsAnamorphic", Value: "true", IsRequired: false},
					{Condition: "EqualsAny", Property: "VideoProfile", Value: "high|main|baseline|constrained baseline", IsRequired: false},
					{Condition: "EqualsAny", Property: "VideoRangeType", Value: "SDR", IsRequired: false},
					{Condition: "LessThanEqual", Property: "VideoLevel", Value: "51", IsRequired: false},
				},
			},
			allow4KTranscode: true,
			wantTranscoding:  true,
		},
		{
			name: "non-anamorphic 4k hevc mkv remains playable when 4k transcode is disabled",
			version: catalog.FileVersion{
				FileID:      2,
				Resolution:  "2160p",
				Container:   "mkv",
				CodecVideo:  "hevc",
				CodecAudio:  "aac",
				VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160, VideoRangeType: "SDR"}},
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Default: true}},
			},
			directProfile: DirectPlayProfile{Type: "Video", Container: "mkv", VideoCodec: "hevc", AudioCodec: "aac"},
			codecProfile: CodecProfile{
				Type:  "Video",
				Codec: "hevc",
				Conditions: []ProfileCondition{
					{Condition: "NotEquals", Property: "IsAnamorphic", Value: "true", IsRequired: false},
					{Condition: "EqualsAny", Property: "VideoProfile", Value: "main|main 10", IsRequired: false},
					{Condition: "EqualsAny", Property: "VideoRangeType", Value: "SDR|HDR10|HLG|DOVI|DOVIWithHDR10|DOVIWithHLG|DOVIWithSDR", IsRequired: false},
					{Condition: "LessThanEqual", Property: "VideoLevel", Value: "183", IsRequired: false},
				},
			},
			allow4KTranscode: false,
			wantTranscoding:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := DeviceProfile{
				DirectPlayProfiles: []DirectPlayProfile{tt.directProfile},
				TranscodingProfiles: []TranscodingProfile{{
					Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
				}},
				CodecProfiles: []CodecProfile{tt.codecProfile},
			}

			source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource(
				"item", "play", tt.version, profile, playbackInfoRequest{}, tt.allow4KTranscode,
			)
			if !source.SupportsDirectPlay {
				t.Fatal("SupportsDirectPlay = false, want true")
			}
			if !source.SupportsDirectStream {
				t.Fatal("SupportsDirectStream = false, want true")
			}
			if source.SupportsTranscoding != tt.wantTranscoding {
				t.Fatalf("SupportsTranscoding = %v, want %v", source.SupportsTranscoding, tt.wantTranscoding)
			}
		})
	}
}

func TestConditionMatchesUnknownPropertyHonorsIsRequired(t *testing.T) {
	tests := []struct {
		name       string
		isRequired bool
		want       bool
	}{
		{name: "optional unknown property is satisfied", want: true},
		{name: "required unknown property fails", isRequired: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := ProfileCondition{
				Condition:  "Equals",
				Property:   "UnsupportedProperty",
				Value:      "value",
				IsRequired: tt.isRequired,
			}
			if got := conditionMatches(condition, conditionValues{}); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeProfileConditionIsRequiredDefault(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "omitted IsRequired decodes as required",
			payload: `{"Condition":"NotEquals","Property":"AudioProfile","Value":"HE-AAC"}`,
			want:    true,
		},
		{
			name:    "explicit false stays optional",
			payload: `{"Condition":"NotEquals","Property":"AudioProfile","Value":"HE-AAC","IsRequired":false}`,
			want:    false,
		},
		{
			name:    "explicit true stays required",
			payload: `{"Condition":"NotEquals","Property":"AudioProfile","Value":"HE-AAC","IsRequired":true}`,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var condition ProfileCondition
			if err := json.Unmarshal([]byte(tt.payload), &condition); err != nil {
				t.Fatalf("unmarshal ProfileCondition: %v", err)
			}
			if condition.IsRequired != tt.want {
				t.Fatalf("IsRequired = %v, want %v", condition.IsRequired, tt.want)
			}
		})
	}
}

func TestConditionMatchesDerivedIsAnamorphic(t *testing.T) {
	tests := []struct {
		name       string
		track      models.VideoTrack
		isRequired bool
		want       bool
	}{
		{
			name:  "anamorphic dvd track fails an optional NotEquals true",
			track: models.VideoTrack{Codec: "h264", Width: 720, Height: 480, AspectRatio: "16:9"},
			want:  false,
		},
		{
			name:  "square pixel 1080p track passes",
			track: models.VideoTrack{Codec: "h264", Width: 1920, Height: 1080, AspectRatio: "16:9"},
			want:  true,
		},
		{
			name:  "rounded display aspect ratio stays inside the tolerance",
			track: models.VideoTrack{Codec: "h264", Width: 1920, Height: 816, AspectRatio: "40:17"},
			want:  true,
		},
		{
			name:  "unknown aspect ratio satisfies an optional condition",
			track: models.VideoTrack{Codec: "h264", Width: 1920, Height: 1080},
			want:  true,
		},
		{
			name:       "unknown aspect ratio fails a required condition",
			track:      models.VideoTrack{Codec: "h264", Width: 1920, Height: 1080},
			isRequired: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition := ProfileCondition{
				Condition:  "NotEquals",
				Property:   "IsAnamorphic",
				Value:      "true",
				IsRequired: tt.isRequired,
			}
			values := buildConditionValues(catalog.FileVersion{VideoTracks: []models.VideoTrack{tt.track}}, nil)
			if got := conditionMatches(condition, values); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionMatchesUsesRealTrackValues(t *testing.T) {
	tests := []struct {
		name      string
		version   catalog.FileVersion
		condition ProfileCondition
		want      bool
	}{
		{
			name:      "interlaced track fails an optional IsInterlaced NotEquals true",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 720, Height: 576, Interlaced: true}}},
			condition: ProfileCondition{Condition: "NotEquals", Property: "IsInterlaced", Value: "true"},
			want:      false,
		},
		{
			name:      "progressive track passes IsInterlaced NotEquals true",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "NotEquals", Property: "IsInterlaced", Value: "true"},
			want:      true,
		},
		{
			name:      "60fps track fails a 30fps framerate cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080, FrameRate: "60/1"}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoFramerate", Value: "30"},
			want:      false,
		},
		{
			name:      "23.976fps track passes a 30fps framerate cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080, FrameRate: "24000/1001"}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoFramerate", Value: "30"},
			want:      true,
		},
		{
			name:      "missing framerate leaves an optional cap satisfied",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoFramerate", Value: "30"},
			want:      true,
		},
		{
			name:      "track bitrate fails a video bitrate cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080, Bitrate: 20_000_000}}},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "VideoBitrate", Value: "10000000"},
			want:      false,
		},
		{
			name: "version bitrate is the video bitrate fallback",
			version: catalog.FileVersion{
				Bitrate:     20_000,
				VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "VideoBitrate", Value: "10000000"},
			want:      false,
		},
		{
			name:      "track bitrate passes a video bitrate cap it fits under",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080, Bitrate: 4_000_000}}},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "VideoBitrate", Value: "10000000"},
			want:      true,
		},
		{
			name: "96kHz audio fails a 48kHz sample rate cap",
			version: catalog.FileVersion{
				CodecAudio:  "flac",
				AudioTracks: []models.AudioTrack{{Codec: "flac", Channels: 2, SampleRate: 96_000, Default: true}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "AudioSampleRate", Value: "48000"},
			want:      false,
		},
		{
			name: "48kHz audio passes a 48kHz sample rate cap",
			version: catalog.FileVersion{
				CodecAudio:  "aac",
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, SampleRate: 48_000, Default: true}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "AudioSampleRate", Value: "48000"},
			want:      true,
		},
		{
			name: "audio bitrate cap is enforced",
			version: catalog.FileVersion{
				CodecAudio:  "truehd",
				AudioTracks: []models.AudioTrack{{Codec: "truehd", Channels: 8, Bitrate: 4_000_000, Default: true}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "AudioBitrate", Value: "640000"},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := buildConditionValues(tt.version, nil)
			if got := conditionMatches(tt.condition, values); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

// jellyfin-web sends "AudioProfile NotEquals HE-AAC" without an IsRequired key,
// which Jellyfin treats as required.
func TestConditionMatchesAudioProfileWithOmittedIsRequired(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		want    bool
	}{
		{name: "he-aac track fails", profile: "HE-AAC", want: false},
		{name: "lc track passes", profile: "LC", want: true},
		{name: "unknown profile fails because the condition is required", profile: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var condition ProfileCondition
			payload := `{"Condition":"NotEquals","Property":"AudioProfile","Value":"HE-AAC"}`
			if err := json.Unmarshal([]byte(payload), &condition); err != nil {
				t.Fatalf("unmarshal ProfileCondition: %v", err)
			}
			if !condition.IsRequired {
				t.Fatal("IsRequired = false, want true for an omitted key")
			}

			values := buildConditionValues(catalog.FileVersion{
				CodecAudio:  "aac",
				AudioTracks: []models.AudioTrack{{Codec: "aac", Profile: tt.profile, Channels: 2, Default: true}},
			}, nil)
			if got := conditionMatches(condition, values); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionMatchesLegacyValuesOnlyWhenKnown(t *testing.T) {
	tests := []struct {
		name      string
		version   catalog.FileVersion
		condition ProfileCondition
		want      bool
	}{
		{
			name:      "level 0 satisfies an optional level cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: 0}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "183"},
			want:      true,
		},
		{
			name:      "level 0 fails a required level cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: 0}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "183", IsRequired: true},
			want:      false,
		},
		{
			name:      "level -99 sentinel satisfies an optional level cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: -99}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "183"},
			want:      true,
		},
		{
			name:      "level -99 sentinel fails a required level cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: -99}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "183", IsRequired: true},
			want:      false,
		},
		{
			name:      "known level 120 passes a looser cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: 120}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "183"},
			want:      true,
		},
		{
			name:      "known level 120 fails a tighter cap",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, Level: 120}}},
			condition: ProfileCondition{Condition: "LessThanOrEqual", Property: "VideoLevel", Value: "100"},
			want:      false,
		},
		{
			name: "channels 0 satisfies an optional channel cap",
			version: catalog.FileVersion{
				CodecAudio:  "aac",
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 0, Default: true}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "AudioChannels", Value: "2"},
			want:      true,
		},
		{
			name: "channels 0 fails a required channel cap",
			version: catalog.FileVersion{
				CodecAudio:  "aac",
				AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 0, Default: true}},
			},
			condition: ProfileCondition{Condition: "LessThanEqual", Property: "AudioChannels", Value: "2", IsRequired: true},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := buildConditionValues(tt.version, nil)
			if got := conditionMatches(tt.condition, values); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionMatchesIsAVC(t *testing.T) {
	tests := []struct {
		name      string
		version   catalog.FileVersion
		condition ProfileCondition
		want      bool
	}{
		{
			name:      "h264 track passes Equals true",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "Equals", Property: "IsAVC", Value: "true"},
			want:      true,
		},
		{
			name:      "hevc track passes NotEquals true",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "NotEquals", Property: "IsAVC", Value: "true"},
			want:      true,
		},
		{
			name:      "hevc track fails Equals true",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "Equals", Property: "IsAVC", Value: "true"},
			want:      false,
		},
		{
			name:      "empty codec satisfies an optional IsAVC condition",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "Equals", Property: "IsAVC", Value: "true"},
			want:      true,
		},
		{
			name:      "empty codec fails a required IsAVC condition",
			version:   catalog.FileVersion{VideoTracks: []models.VideoTrack{{Width: 1920, Height: 1080}}},
			condition: ProfileCondition{Condition: "Equals", Property: "IsAVC", Value: "true", IsRequired: true},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := buildConditionValues(tt.version, nil)
			if got := conditionMatches(tt.condition, values); got != tt.want {
				t.Fatalf("conditionMatches() = %v, want %v", got, tt.want)
			}
		})
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
