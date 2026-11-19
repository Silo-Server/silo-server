package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestBuildPlaybackSourceHEVCEncodingRequiresFlagAndFMP4Profile(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "vp9", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}
	hevcFMP4 := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{
		Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
	}}}
	h264TS := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{
		Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
	}}}
	tests := []struct {
		name    string
		profile DeviceProfile
		flag    bool
		want    string
	}{
		{name: "HEVC fMP4 profile and enabled flag", profile: hevcFMP4, flag: true, want: "hevc"},
		{name: "flag disabled keeps no unverified HEVC route", profile: hevcFMP4, want: "h264"},
		{name: "TS HEVC profile cannot authorize fMP4 HEVC", profile: DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "hevc", AudioCodec: "aac"}}}, flag: true, want: "h264"},
		{name: "implicit profile cannot authorize HEVC", profile: DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", VideoCodec: "hevc", AudioCodec: "aac"}}}, flag: true, want: "h264"},
		{name: "H264 profile remains fallback", profile: h264TS, flag: true, want: "h264"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource("item", "play", version, tt.profile, playbackInfoRequest{}, true, tt.flag)
			if source.TargetVideoCodec != tt.want {
				t.Fatalf("TargetVideoCodec = %q, want %q", source.TargetVideoCodec, tt.want)
			}
			if tt.want == "hevc" {
				if !source.SupportsTranscoding {
					t.Fatal("HEVC fMP4 route not advertised")
				}
				dto := (&PlaybackHandler{}).mediaSourceDTO("item", "play", source)
				if dto.TranscodingContainer != "mp4" {
					t.Fatalf("TranscodingContainer = %q, want mp4", dto.TranscodingContainer)
				}
			}
		})
	}
}

func TestHEVCNegotiationRejectsStaleH264Recipe(t *testing.T) {
	source := testRemoteTranscodeSource()
	source.TargetVideoCodec = compatVideoCodecHEVC
	stale := playback.NewRecipeCard(7, "profile", source.FileID, "", playback.TranscodeOpts{
		TargetCodecVideo:  compatTargetVideoCodec,
		TargetCodecAudio:  compatTargetAudioCodec,
		AudioTrackIndex:   compatAudioTrackIndexOrDefault(source),
		RequireAudioTrack: true,
	})
	if compatRecipeMatchesSource(&stale, source) {
		t.Fatal("HEVC source accepted stale H264 reconstruction recipe")
	}
	fresh := stale
	fresh.TargetCodecVideo = compatVideoCodecHEVC
	if !compatRecipeMatchesSource(&fresh, source) {
		t.Fatal("HEVC source rejected matching reconstruction recipe")
	}
	copySource := source
	copySource.HLSRemux = true
	copy := fresh
	copy.TargetCodecVideo = compatCopyCodec
	if !compatRecipeMatchesSource(&copy, copySource) {
		t.Fatal("HEVC HLS remux rejected matching copy reconstruction recipe")
	}
	if got := compatSourceVideoSampleEntry(source); got != playback.VideoSampleEntryHVC1 {
		t.Fatalf("local HEVC sample entry = %q, want hvc1", got)
	}
	if got := compatSourceVideoSampleEntry(copySource); got != "" {
		t.Fatalf("copy-remux sample entry = %q, want copy path override", got)
	}
	legacySource := testRemoteTranscodeSource()
	legacy := fresh
	legacy.TargetCodecVideo = ""
	if !compatRecipeMatchesSource(&legacy, legacySource) {
		t.Fatal("legacy target-less H264 recipe was rejected")
	}
	if compatRecipeMatchesSource(&legacy, source) {
		t.Fatal("HEVC source accepted target-less legacy recipe")
	}
}
