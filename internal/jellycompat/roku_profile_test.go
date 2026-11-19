package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// An HEVC encode is held to the profile's HEVC limits, not its H.264 ones.
func TestHEVCEncodeUsesItsOwnLimits(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "vp9", CodecAudio: "aac", Bitrate: 20000, Resolution: "1080p",
		VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	limit := func(property, value string) []ProfileCondition {
		return []ProfileCondition{{Condition: "LessThanEqual", Property: property, Value: value, IsRequired: true}}
	}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{EnableDirectPlay: new(false), AllowVideoStreamCopy: new(false)}
	for _, tc := range []struct {
		name           string
		hevc, h264     []ProfileCondition
		wantBitrate    int
		wantResolution string
	}{
		{name: "an HEVC width limit scales the HEVC encode", hevc: limit("Width", "1280"), wantResolution: "720p"},
		{name: "an H.264 bitrate limit leaves HEVC alone", h264: limit("VideoBitrate", "4000000")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{
				{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac", Conditions: tc.hevc},
				{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac", Conditions: tc.h264},
			}}
			source := h.buildPlaybackSource("item", "play", version, profile, req, true, true)
			if !source.SupportsTranscoding || source.TargetVideoCodec != compatVideoCodecHEVC {
				t.Fatalf("transcoding=%v codec=%q, want an HEVC encode", source.SupportsTranscoding, source.TargetVideoCodec)
			}
			if source.TargetBitrateKbps != tc.wantBitrate || source.TargetResolution != tc.wantResolution {
				t.Fatalf("HEVC encode = %d kb/s at %q, want %d kb/s at %q", source.TargetBitrateKbps, source.TargetResolution, tc.wantBitrate, tc.wantResolution)
			}
		})
	}
}

// roku1080pProfile is the profile the Roku client stored for a 1080p,
// H.264-only Roku without Dolby decoding: required VideoBitrate <= 10 Mb/s on
// H.264, required Height <= 1080 and Width <= 1920 on both HLS video
// transcoding profiles, and AC-3 listed for transcoding but nowhere else.
func roku1080pProfile(t *testing.T) DeviceProfile {
	t.Helper()
	data, err := os.ReadFile("testdata/roku_1080p_device_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile DeviceProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

// Jellyfin encodes to a profile's LessThanEqual limits instead of rejecting
// the output, so a file this Roku cannot play as is gets an H.264 encode that
// fits them rather than a 400.
func TestRoku1080pProfileEncodesWithinItsLimits(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{MaxStreamingBitrate: 140_000_000}
	for _, tc := range []struct {
		name           string
		video          models.VideoTrack
		audio          models.AudioTrack
		bitrateKbps    int
		wantDirect     bool
		wantRemux      bool
		wantResolution string
	}{
		{name: "H.264 AAC direct plays", video: models.VideoTrack{Codec: "h264", Profile: "High", Level: 40, Width: 1920, Height: 1080}, audio: models.AudioTrack{Codec: "aac", Profile: "LC", Channels: 2}, bitrateKbps: 8000, wantDirect: true},
		{name: "H.264 E-AC-3 remuxes", video: models.VideoTrack{Codec: "h264", Profile: "High", Level: 40, Width: 1920, Height: 1080}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 8000, wantRemux: true},
		{name: "H.264 over 10 Mb/s encodes", video: models.VideoTrack{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080}, audio: models.AudioTrack{Codec: "aac", Profile: "LC", Channels: 2}, bitrateKbps: 20000},
		{name: "HEVC encodes", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 120, Width: 1920, Height: 1080}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 7000},
		{name: "4K HEVC scales to 1080p", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 2160}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 20000, wantResolution: "1080p"},
		{name: "4K scope fits the width", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 1608}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 20000, wantResolution: "804p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			video, audio := tc.video, tc.audio
			video.BitDepth, video.VideoRangeType, video.Bitrate = 8, "SDR", tc.bitrateKbps-400
			audio.Default, audio.Bitrate = true, 384
			version := catalog.FileVersion{FileID: 1, Container: "mkv", CodecVideo: video.Codec, CodecAudio: audio.Codec, Bitrate: tc.bitrateKbps, Duration: 2700,
				VideoTracks: []models.VideoTrack{video}, AudioTracks: []models.AudioTrack{audio}}
			source := h.buildPlaybackSource("item", "play", version, roku1080pProfile(t), req, true)
			if source.SupportsDirectPlay != tc.wantDirect || !source.SupportsTranscoding || source.HLSRemux != tc.wantRemux {
				t.Fatalf("direct=%v transcoding=%v remux=%v, want direct=%v transcoding=true remux=%v", source.SupportsDirectPlay, source.SupportsTranscoding, source.HLSRemux, tc.wantDirect, tc.wantRemux)
			}
			if !tc.wantRemux && !tc.wantDirect {
				if source.TargetBitrateKbps != 10_000 || source.TargetResolution != tc.wantResolution {
					t.Fatalf("encode = %d kb/s at %q, want 10000 kb/s at %q", source.TargetBitrateKbps, source.TargetResolution, tc.wantResolution)
				}
				if reasons := compatTranscodeReasons(source); reasons[0] == "ContainerNotSupported" {
					t.Fatalf("an encode reports copy reasons %v", reasons)
				}
			}
		})
	}
}

// After a failed direct play, Moonfin for Roku asks again with no
// DirectPlayProfiles. That must not offer the same direct play again.
func TestRokuRetryWithoutDirectPlayProfilesGetsAnEncode(t *testing.T) {
	profile := roku1080pProfile(t)
	profile.DirectPlayProfiles = nil
	version := catalog.FileVersion{FileID: 42, Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3", Bitrate: 5000, Duration: 3600,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main", Level: 120, BitDepth: 8, Width: 1920, Height: 1080, VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}}
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxStreamingBitrate: 140_000_000}, true)
	if source.SupportsDirectPlay || source.SupportsDirectStream || !source.SupportsTranscoding || compatHLSCopiesVideo(source) {
		t.Fatalf("direct=%v stream=%v transcoding=%v copies video=%v, want only an encode", source.SupportsDirectPlay, source.SupportsDirectStream, source.SupportsTranscoding, compatHLSCopiesVideo(source))
	}
}

// The retry profile answers its own request. Stored over the full profile, it
// would refuse direct play to every later PlaybackInfo the device sends
// without one. A device with nothing stored keeps it like any other profile,
// and it is held to the stored-profile limits either way.
func TestRokuRetryProfileStorage(t *testing.T) {
	full := roku1080pProfile(t)
	retry := full
	retry.DirectPlayProfiles = nil
	for _, tc := range []struct {
		name       string
		posts      []DeviceProfile
		wantDirect bool
	}{
		{name: "a retry keeps the stored full profile", posts: []DeviceProfile{full, retry}, wantDirect: true},
		{name: "a first transcode-only profile is stored", posts: []DeviceProfile{retry}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, routeID := newSubtitleSelectionHandler(t)
			for _, profile := range tc.posts {
				body, err := json.Marshal(map[string]any{"DeviceProfile": profile})
				if err != nil {
					t.Fatal(err)
				}
				postPlaybackInfoForDeviceBody(t, handler, routeID, "roku-1", string(body))
			}
			stored, ok, err := handler.deviceProfiles.GetForDevice(context.Background(), "token-1", "roku-1")
			if err != nil || !ok || (len(stored.DirectPlayProfiles) > 0) != tc.wantDirect {
				t.Fatalf("stored profile ok=%v err=%v direct-play profiles=%d, want direct play=%v", ok, err, len(stored.DirectPlayProfiles), tc.wantDirect)
			}
		})
	}
	t.Run("an oversized retry is refused", func(t *testing.T) {
		handler, _ := newSubtitleSelectionHandler(t)
		oversized := DeviceProfile{TranscodingProfiles: slices.Repeat([]TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"}}, maxDeviceProfileEntries+1)}
		body, err := json.Marshal(map[string]any{"DeviceProfile": oversized})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = handler.parsePlaybackRequest(httptest.NewRequest(http.MethodPost, "/Items/item/PlaybackInfo", strings.NewReader(string(body))), "token-1")
		if httpErr, ok := errors.AsType[*HTTPError](err); !ok || httpErr.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("err = %v, want 413", err)
		}
	})
}
