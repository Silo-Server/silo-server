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

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// rokuProfile is the DeviceProfile Moonfin for Roku (a jellyfin-roku fork)
// builds in source/utils/deviceCapabilities.bs for a 4K Roku on stereo output.
// Its video transcoding profiles list AC-3 but not AAC, and its AAC codec
// profile requires AudioProfile != Main.
func rokuProfile(t *testing.T) DeviceProfile {
	t.Helper()
	data, err := os.ReadFile("testdata/roku_device_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile DeviceProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func rokuTestVersion(audio models.AudioTrack) catalog.FileVersion {
	return catalog.FileVersion{
		FileID: 7, Container: "mkv", CodecVideo: "h264", CodecAudio: audio.Codec, Resolution: "1080p", Bitrate: 8000, Duration: 3600,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, BitDepth: 8, Width: 1920, Height: 1080, VideoRangeType: "SDR", Bitrate: 7_500_000}},
		AudioTracks: []models.AudioTrack{audio},
	}
}

// Every Roku decodes stereo AAC, so a Roku profile that leaves AAC out of its
// transcoding profiles still gets AAC encodes.
func TestRokuProfileGetsAACWhenItIsNotListed(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{MaxStreamingBitrate: 140_000_000}
	for _, tc := range []struct {
		name        string
		profile     DeviceProfile
		audio       models.AudioTrack
		wantDirect  bool
		wantRemux   bool
		wantSegment string
	}{
		{name: "AAC-LC direct plays", profile: rokuProfile(t), audio: models.AudioTrack{Codec: "aac", Profile: "LC", Channels: 2, Default: true}, wantDirect: true},
		{name: "DTS remuxes with AAC", profile: rokuProfile(t), audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}, wantSegment: compatAudioV2PathSegment, wantRemux: true},
		// The AAC encode states its LC profile, so Roku's AudioProfile != Main
		// rule passes when the profile lists AAC too.
		{name: "a profile that lists AAC remuxes DTS with AAC", profile: withAACListed(rokuProfile(t)), audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}, wantSegment: compatAudioV2PathSegment, wantRemux: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := h.buildPlaybackSource("item", "play", rokuTestVersion(tc.audio), tc.profile, req, true)
			if source.SupportsDirectPlay != tc.wantDirect || !source.SupportsTranscoding {
				t.Fatalf("direct=%v transcoding=%v, want direct=%v transcoding=true", source.SupportsDirectPlay, source.SupportsTranscoding, tc.wantDirect)
			}
			if source.HLSRemux != tc.wantRemux || source.TranscodeAudio != tc.wantRemux {
				t.Fatalf("HLSRemux=%v TranscodeAudio=%v, want remux=%v", source.HLSRemux, source.TranscodeAudio, tc.wantRemux)
			}
			if got := compatHLSRoutePathSegment(source); got != tc.wantSegment {
				t.Fatalf("route segment = %q, want %q", got, tc.wantSegment)
			}
		})
	}
}

// Roku sends AlwaysBurnInSubtitleWhenTranscoding=true, so a selected bitmap
// subtitle turns an audio-encoding remux into a full encode. It must stop
// copying video, or the subtitle is never drawn.
func TestRokuBurnInEncodesVideo(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	profile := rokuProfile(t)
	version := rokuTestVersion(models.AudioTrack{Codec: "dts", Channels: 6, Default: true})
	version.SubtitleTracks = []catalog.VersionSubtitleTrack{{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "eng", Default: true}}
	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxStreamingBitrate: 140_000_000}, true)
	selected := 2
	source.SelectedSubtitleStreamIndex = &selected
	applyCompatSubtitleDelivery(&source, profile, true)
	if !source.SubtitleBurnIn || !source.SupportsTranscoding || compatHLSCopiesVideo(source) {
		t.Fatalf("burn-in=%v transcoding=%v copies video=%v, want a video encode", source.SubtitleBurnIn, source.SupportsTranscoding, compatHLSCopiesVideo(source))
	}
}

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
		{name: "HEVC encodes", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 120, Width: 1920, Height: 1080}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 5000},
		{name: "4K HEVC scales to 1080p", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 2160}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 20000, wantResolution: "1080p"},
		{name: "4K scope fits the width", video: models.VideoTrack{Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 1608}, audio: models.AudioTrack{Codec: "eac3", Channels: 6}, bitrateKbps: 20000, wantResolution: "804p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			video, audio := tc.video, tc.audio
			video.BitDepth, video.VideoRangeType, video.Bitrate = 8, "SDR", (tc.bitrateKbps-400)*1000
			audio.Default, audio.Bitrate = true, 384_000
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

// A stored profile must read back as it was negotiated: IsRequired defaults
// to true when the key is missing, so false has to be written.
func TestDeviceProfileRoundTripKeepsOptionalConditions(t *testing.T) {
	profile := DeviceProfile{CodecProfiles: []CodecProfile{{Type: "Video", Codec: "h264", Conditions: []ProfileCondition{
		{Condition: "NotEquals", Property: "IsAnamorphic", Value: "true", IsRequired: false},
		{Condition: "LessThanEqual", Property: "VideoBitrate", Value: "10000000", IsRequired: true},
	}}}}
	data, err := encodeDeviceProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	var stored DeviceProfile
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if got := stored.CodecProfiles[0].Conditions; got[0].IsRequired || !got[1].IsRequired {
		t.Fatalf("stored IsRequired = %v, %v; want false, true", got[0].IsRequired, got[1].IsRequired)
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

func withAACListed(profile DeviceProfile) DeviceProfile {
	profile.TranscodingProfiles = slices.Clone(profile.TranscodingProfiles)
	for i := range profile.TranscodingProfiles {
		if profile.TranscodingProfiles[i].Type == "Video" {
			profile.TranscodingProfiles[i].AudioCodec = "aac," + profile.TranscodingProfiles[i].AudioCodec
		}
	}
	return profile
}

// Roku players can play the audio of an fMP4 HLS copy silently, so a Roku's
// video-copy stream is MPEG-TS, which its profile lists first. Other clients
// keep fMP4.
func TestRokuVideoCopyUsesMPEGTS(t *testing.T) {
	profile, err := os.ReadFile("testdata/roku_1080p_device_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	// A profile whose MPEG-TS rules refuse the copied video keeps the copy in
	// the fMP4 it was negotiated for.
	var tsRefusesH264 DeviceProfile
	if err := json.Unmarshal(profile, &tsRefusesH264); err != nil {
		t.Fatal(err)
	}
	for i, transcoding := range tsRefusesH264.TranscodingProfiles {
		if transcoding.Type == "Video" && transcoding.Container == compatContainerTS {
			tsRefusesH264.TranscodingProfiles[i].VideoCodec = "mpeg2video"
		}
	}
	tsRefusesH264JSON, err := json.Marshal(tsRefusesH264)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		userAgent     string
		profile       []byte
		wantContainer string
		wantSegment   string
	}{
		{name: "Roku", userAgent: "Roku/DVP-16.0 (16.0.4.842-24)", profile: profile, wantContainer: "ts", wantSegment: "/" + compatRemuxTSV1PathSegment + "/"},
		{name: "browser", userAgent: "Mozilla/5.0", profile: profile, wantContainer: compatContainerMP4, wantSegment: "/" + compatAudioV2PathSegment + "/"},
		{name: "Roku without H.264 in MPEG-TS", userAgent: "Roku/DVP-16.0 (16.0.4.842-24)", profile: tsRefusesH264JSON, wantContainer: compatContainerMP4, wantSegment: "/" + compatAudioV2PathSegment + "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := newSubtitleSelectionHandler(t)
			handler.SubtitleRepo = nil
			version := rokuTestVersion(models.AudioTrack{Codec: "eac3", Channels: 6, Default: true})
			handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
			routeID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
			req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{"DeviceProfile":`+string(tc.profile)+`}`))
			req.Header.Set("User-Agent", tc.userAgent)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", routeID)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
			rec := httptest.NewRecorder()
			handler.HandlePlaybackInfo(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			source := decodePlaybackInfo(t, rec.Body.Bytes()).MediaSources[0]
			if source.TranscodingContainer != tc.wantContainer || !strings.Contains(source.TranscodingURL, tc.wantSegment) {
				t.Fatalf("transcoding %s at %s, want %s on %s", source.TranscodingContainer, source.TranscodingURL, tc.wantContainer, tc.wantSegment)
			}
			// Moonfin shows text subtitles itself only when every reason is
			// an audio or container reason; a video reason makes it ask for a
			// burn-in, which restarts the copy as a full encode.
			if !strings.Contains(source.TranscodingURL, "&TranscodeReasons=ContainerNotSupported,AudioChannelsNotSupported") {
				t.Fatalf("TranscodingURL %s lacks the copy's reasons", source.TranscodingURL)
			}
		})
	}
}
