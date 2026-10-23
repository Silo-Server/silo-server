package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
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

// withoutAACDecode drops every sign that the device decodes AAC, leaving a
// profile that only AC-3 or MP3 encodes can serve.
func withoutAACDecode(profile DeviceProfile) DeviceProfile {
	profile.DirectPlayProfiles = slices.Clone(profile.DirectPlayProfiles)
	for i := range profile.DirectPlayProfiles {
		codecs := strings.Split(profile.DirectPlayProfiles[i].AudioCodec, ",")
		profile.DirectPlayProfiles[i].AudioCodec = strings.Join(slices.DeleteFunc(codecs, func(codec string) bool { return codec == "aac" }), ",")
	}
	profile.CodecProfiles = slices.DeleteFunc(slices.Clone(profile.CodecProfiles), func(codec CodecProfile) bool { return codec.Codec == "aac" })
	return profile
}

func TestRokuProfileNegotiatesAC3WhenAACIsNotListed(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{MaxStreamingBitrate: 140_000_000}
	withAAC := withAACListed(rokuProfile(t))
	noAAC := withoutAACDecode(rokuProfile(t))
	for _, tc := range []struct {
		name          string
		profile       DeviceProfile
		allowAC3      bool
		audio         models.AudioTrack
		wantDirect    bool
		wantTranscode bool
		wantRemux     bool
		wantCopyAudio bool
		noDirect      bool
		wantCodec     string
		wantSegment   string
	}{
		// Every Roku decodes stereo AAC, so a Roku profile that leaves AAC
		// out of its transcoding profiles still gets AAC.
		{name: "AAC-LC direct plays", profile: rokuProfile(t), allowAC3: true, audio: models.AudioTrack{Codec: "aac", Profile: "LC", Channels: 2, Default: true}, wantDirect: true, wantTranscode: true},
		{name: "a Roku that decodes AAC remuxes DTS with AAC", profile: rokuProfile(t), allowAC3: true, audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}, wantTranscode: true, wantSegment: compatAudioV2PathSegment, wantRemux: true},
		{name: "DTS remuxes with AC-3", profile: noAAC, allowAC3: true, audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}, wantTranscode: true, wantCodec: compatAudioCodecAC3, wantSegment: compatAudioCodecV1PathSegment, wantRemux: true},
		{name: "TrueHD 7.1 remuxes with AC-3", profile: noAAC, allowAC3: true, audio: models.AudioTrack{Codec: "truehd", Channels: 8, Default: true}, wantTranscode: true, wantCodec: compatAudioCodecAC3, wantSegment: compatAudioCodecV1PathSegment, wantRemux: true},
		{name: "surround AC-3 on stereo output re-encodes", profile: noAAC, allowAC3: true, audio: models.AudioTrack{Codec: "ac3", Channels: 6, Default: true}, wantTranscode: true, wantCodec: compatAudioCodecAC3, wantSegment: compatAudioCodecV1PathSegment, wantRemux: true},
		// A burned-in subtitle turns the audio-copy remux into the full
		// encode, which then needs its AC-3 audio.
		{name: "AC-3 stereo copies into HLS", profile: noAAC, allowAC3: true, noDirect: true, audio: models.AudioTrack{Codec: "ac3", Channels: 2, Default: true}, wantTranscode: true, wantCodec: compatAudioCodecAC3, wantSegment: compatRemuxV1PathSegment, wantRemux: true, wantCopyAudio: true},
		{name: "no AC-3 executor offers nothing", profile: noAAC, audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}},
		// The AAC encode states its LC profile, so Roku's AudioProfile != Main
		// rule passes and a profile that lists AAC keeps getting AAC.
		{name: "AAC stays preferred when listed", profile: withAAC, allowAC3: true, audio: models.AudioTrack{Codec: "dts", Channels: 6, Default: true}, wantTranscode: true, wantSegment: compatAudioV2PathSegment, wantRemux: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := tc.profile
			if tc.allowAC3 {
				profile.extraAudioOutputs = profile.alternateAudioOutputs()
			}
			req := req
			if tc.noDirect {
				req.EnableDirectPlay = boolPtr(false)
			}
			source := h.buildPlaybackSource("item", "play", rokuTestVersion(tc.audio), profile, req, true)
			if source.SupportsDirectPlay != tc.wantDirect || source.SupportsTranscoding != tc.wantTranscode {
				t.Fatalf("direct=%v transcoding=%v, want direct=%v transcoding=%v", source.SupportsDirectPlay, source.SupportsTranscoding, tc.wantDirect, tc.wantTranscode)
			}
			if source.TargetAudioCodec != tc.wantCodec {
				t.Fatalf("TargetAudioCodec = %q, want %q", source.TargetAudioCodec, tc.wantCodec)
			}
			if tc.wantTranscode {
				if source.HLSRemux != tc.wantRemux || source.TranscodeAudio != (tc.wantRemux && !tc.wantCopyAudio) {
					t.Fatalf("HLSRemux=%v TranscodeAudio=%v, want remux=%v copied audio=%v", source.HLSRemux, source.TranscodeAudio, tc.wantRemux, tc.wantCopyAudio)
				}
				if got := compatHLSRoutePathSegment(source); got != tc.wantSegment {
					t.Fatalf("route segment = %q, want %q", got, tc.wantSegment)
				}
				if tc.wantCopyAudio {
					source.HLSRemux = false // as applyCompatSubtitleDelivery does for a burn-in
					if got := compatHLSRoutePathSegment(source); got != compatAudioCodecV1PathSegment {
						t.Fatalf("burn-in encode route = %q, want %q", got, compatAudioCodecV1PathSegment)
					}
				}
			}
		})
	}
}

func rokuAC3Source(t *testing.T) PlaybackMediaSource {
	t.Helper()
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	source.ID = "source"
	source.HLSRemux, source.TranscodeAudio = true, true
	source.TargetAudioCodec = compatAudioCodecAC3
	return source
}

func TestAC3PlaybackURLsRejectOlderAPIRouters(t *testing.T) {
	legacy := chi.NewRouter()
	reached := false
	for _, prefix := range []string{"", "/audio-v2", "/remux-v1", "/remux-ts-v1", "/remux-dv-v1", "/hevc-v1"} {
		for _, suffix := range []string{"/master.m3u8", "/hls/{playlistId}/stream.m3u8", "/hls/{playlistId}/{segmentId}.{segmentContainer}"} {
			legacy.Get("/Videos/{id}"+prefix+suffix, func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
		}
	}
	remux := rokuAC3Source(t)
	encode := rokuAC3Source(t)
	encode.HLSRemux, encode.TranscodeAudio, encode.TargetVideoCodec = false, false, compatVideoCodecHEVC
	for _, source := range []PlaybackMediaSource{remux, encode} {
		dto := (&PlaybackHandler{}).mediaSourceDTO("item", "play", "token", source)
		urls := []string{dto.TranscodingURL}
		for _, resource := range []string{"stream.m3u8", "init.mp4", "seg_00001.m4s"} {
			urls = append(urls, buildSegmentProxyPath("item", "play", source.ID, resource, compatHLSRoutePathSegment(source)))
		}
		for _, resourceURL := range urls {
			reached = false
			rec := httptest.NewRecorder()
			legacy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resourceURL, nil))
			if rec.Code != http.StatusNotFound || reached || !strings.Contains(resourceURL, "/audio-codec-v1/") {
				t.Errorf("AC-3 URL %q reached an older AAC handler (status %d) or lacks its route", resourceURL, rec.Code)
			}
		}
	}
}

func TestAC3HLSRoutesEnforceNegotiatedRecipe(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	aac := rokuAC3Source(t)
	aac.TargetAudioCodec = ""
	for _, test := range []struct {
		name   string
		source PlaybackMediaSource
		prefix string
	}{
		{name: "AAC route rejects AC-3", source: rokuAC3Source(t), prefix: "/audio-v2"},
		{name: "legacy route rejects AC-3", source: rokuAC3Source(t)},
		{name: "AC-3 route rejects AAC", source: aac, prefix: "/audio-codec-v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play", CompatToken: "token", RouteItemID: "item", UpstreamSessionID: "upstream",
				MediaSources: []PlaybackMediaSource{test.source},
			})
			sessions := NewSessionStore(time.Hour, nil)
			if err := sessions.Put(Session{Token: "token", StreamAppUserID: 1}); err != nil {
				t.Fatal(err)
			}
			router := NewRouter(Dependencies{Config: cfg, SessionStore: sessions, PlaybackStore: store})
			for _, suffix := range []string{"/master.m3u8", "/hls/play/stream.m3u8", "/hls/play/init.mp4", "/hls/play/seg_00001.m4s"} {
				req := httptest.NewRequest(http.MethodGet, "/Videos/item"+test.prefix+suffix+"?PlaySessionId=play&MediaSourceId=source", nil)
				req.Header.Set("X-Emby-Token", "token")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("%s status = %d, want 404 before starting playback: %s", suffix, rec.Code, rec.Body.String())
				}
			}
			stored, _ := store.Get("play")
			if stored == nil || stored.TranscodeStarted || stored.Recipe != nil || stored.UpstreamSessionID != "upstream" {
				t.Fatalf("mismatched route changed the session: %+v", stored)
			}
		})
	}
}

// An AC-3 encode reserves its own 448 kb/s, not AAC's, out of the ceiling.
func TestRokuAC3EncodeFitsTheBitrateCeiling(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	profile := withoutAACDecode(rokuProfile(t))
	profile.extraAudioOutputs = profile.alternateAudioOutputs()
	version := rokuTestVersion(models.AudioTrack{Codec: "dts", Channels: 6, Default: true})
	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxStreamingBitrate: 2_000_000}, true)
	if !source.SupportsTranscoding || compatHLSCopiesVideo(source) || source.TargetAudioCodec != compatAudioCodecAC3 {
		t.Fatalf("transcoding=%v copies video=%v audio=%q, want an AC-3 video encode", source.SupportsTranscoding, compatHLSCopiesVideo(source), source.TargetAudioCodec)
	}
	if got, want := source.TargetBitrateKbps, 2_000*95/100-compatAC3BitrateKbps; got != want {
		t.Fatalf("video bitrate = %d kb/s, want %d", got, want)
	}
}

// Roku sends AlwaysBurnInSubtitleWhenTranscoding=true, so a selected bitmap
// subtitle turns an audio-encoding remux into a full encode. It must stop
// copying video, or the subtitle is never drawn, and keep its AC-3 audio.
func TestRokuBurnInEncodesVideoWithAC3(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	profile := withoutAACDecode(rokuProfile(t))
	profile.extraAudioOutputs = profile.alternateAudioOutputs()
	version := rokuTestVersion(models.AudioTrack{Codec: "dts", Channels: 6, Default: true})
	version.SubtitleTracks = []catalog.VersionSubtitleTrack{{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "eng", Default: true}}
	source := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{MaxStreamingBitrate: 140_000_000}, true)
	selected := 2
	source.SelectedSubtitleStreamIndex = &selected
	applyCompatSubtitleDelivery(&source, profile, true)
	if !source.SubtitleBurnIn || !source.SupportsTranscoding || compatHLSCopiesVideo(source) {
		t.Fatalf("burn-in=%v transcoding=%v copies video=%v, want a video encode", source.SubtitleBurnIn, source.SupportsTranscoding, compatHLSCopiesVideo(source))
	}
	if got := compatHLSRoutePathSegment(source); got != compatAudioCodecV1PathSegment {
		t.Fatalf("route segment = %q, want %q", got, compatAudioCodecV1PathSegment)
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
	profile.extraAudioOutputs = profile.alternateAudioOutputs()
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
			// The profile leaves AAC out of its transcoding profiles, but this
			// Roku decodes stereo AAC, as every Roku does.
			if source.TargetAudioCodec != "" {
				t.Fatalf("TargetAudioCodec = %q, want AAC", source.TargetAudioCodec)
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

// Roku clients list AC-3 first for transcoding on every Roku. A device that
// cannot decode it fails the stream with "unsupported audio format", so when
// AAC is out, the alternate encode the device declares it decodes comes first.
func TestAlternateAudioOutputsPreferWhatTheDeviceDecodes(t *testing.T) {
	if got := withoutAACDecode(roku1080pProfile(t)).alternateAudioOutputs(); !slices.Equal(got, []string{compatAudioCodecMP3, compatAudioCodecAC3}) {
		t.Fatalf("no AAC or AC-3 decode: %v, want [mp3 ac3]", got)
	}
	if got := withoutAACDecode(rokuProfile(t)).alternateAudioOutputs(); !slices.Equal(got, []string{compatAudioCodecAC3, compatAudioCodecMP3}) {
		t.Fatalf("AC-3 decode without AAC: %v, want [ac3 mp3]", got)
	}
	for name, profile := range map[string]DeviceProfile{
		"Roku that decodes AAC":  roku1080pProfile(t),
		"profile that lists AAC": withAACListed(rokuProfile(t)),
	} {
		if got := profile.alternateAudioOutputs(); len(got) != 0 {
			t.Fatalf("%s: %v, want none", name, got)
		}
	}
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
	for _, tc := range []struct {
		userAgent     string
		wantContainer string
		wantSegment   string
	}{
		{userAgent: "Roku/DVP-16.0 (16.0.4.842-24)", wantContainer: "ts", wantSegment: "/" + compatRemuxTSV1PathSegment + "/"},
		{userAgent: "Mozilla/5.0", wantContainer: compatContainerMP4, wantSegment: "/" + compatAudioV2PathSegment + "/"},
	} {
		t.Run(tc.userAgent, func(t *testing.T) {
			handler, _ := newSubtitleSelectionHandler(t)
			handler.SubtitleRepo = nil
			version := rokuTestVersion(models.AudioTrack{Codec: "eac3", Channels: 6, Default: true})
			handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
			routeID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
			req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{"DeviceProfile":`+string(profile)+`}`))
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
