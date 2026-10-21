package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestHEVCEncodingHonorsHLSCodecProfileConditions(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "vp9", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 2}},
	}
	for _, test := range []struct {
		name         string
		codecProfile CodecProfile
		wantCodec    string
	}{
		{
			name: "HLS MP4 video width limit",
			codecProfile: CodecProfile{Type: "Video", Codec: "hevc", Container: "hls", SubContainer: "mp4", Conditions: []ProfileCondition{
				{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true},
			}},
			wantCodec: "h264",
		},
		{
			name: "required output level is unknown",
			codecProfile: CodecProfile{Type: "Video", Codec: "hevc", Container: "hls", SubContainer: "mp4", Conditions: []ProfileCondition{
				{Condition: "LessThanEqual", Property: "VideoLevel", Value: "120", IsRequired: true},
			}},
			wantCodec: "h264",
		},
		{
			name: "HLS MP4 audio channel limit",
			codecProfile: CodecProfile{Type: "VideoAudio", Codec: "aac", Container: "hls", SubContainer: "mp4", Conditions: []ProfileCondition{
				{Condition: "LessThanEqual", Property: "AudioChannels", Value: "1", IsRequired: true},
			}},
			wantCodec: "h264",
		},
		{
			name: "output satisfies HLS MP4 condition",
			codecProfile: CodecProfile{Type: "Video", Codec: "hevc", Container: "hls", SubContainer: "mp4", Conditions: []ProfileCondition{
				{Condition: "LessThanEqual", Property: "Width", Value: "1920", IsRequired: true},
			}},
			wantCodec: "hevc",
		},
		{
			name: "condition belongs to a different subcontainer",
			codecProfile: CodecProfile{Type: "Video", Codec: "hevc", Container: "hls", SubContainer: "ts", Conditions: []ProfileCondition{
				{Condition: "LessThanEqual", Property: "Width", Value: "1280", IsRequired: true},
			}},
			wantCodec: "hevc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := DeviceProfile{
				TranscodingProfiles: []TranscodingProfile{
					{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac"},
					{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"},
				},
				CodecProfiles: []CodecProfile{test.codecProfile},
			}
			h := &PlaybackHandler{codec: NewResourceIDCodec()}
			source := h.buildPlaybackSource("item", "session", version, profile, playbackInfoRequest{
				EnableDirectPlay: boolPtr(false), AllowVideoStreamCopy: boolPtr(false),
			}, true, true)
			if !source.SupportsTranscoding || source.TargetVideoCodec != test.wantCodec {
				t.Fatalf("transcoding=%t, codec=%q; want supported %s", source.SupportsTranscoding, source.TargetVideoCodec, test.wantCodec)
			}
		})
	}
}
