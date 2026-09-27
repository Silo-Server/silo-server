package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// dvStripProfile8Track is a Dolby Vision profile 8.1 video: HEVC Main 10
// with an HDR10 base layer (compatibility id 1).
func dvStripProfile8Track() models.VideoTrack {
	return models.VideoTrack{
		Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 1920, BitDepth: 10,
		VideoRange: "HDR", VideoRangeType: "DOVIWithHDR10", DolbyVision: "Dolby Vision Profile 8.1",
		DVProfile: 8, DVLevel: 6, DVBLCompatID: 1,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}
}

// androidTVRangeProfile mirrors the Jellyfin Android TV device profile, which
// rejects range types with a NotEquals condition gated by an InCollection
// ApplyCondition over the same list.
func androidTVRangeProfile(unsupportedHEVC ...string) string {
	values := strings.Join(unsupportedHEVC, "|")
	profile := map[string]any{
		"Name":                "AndroidTV-Default",
		"MaxStreamingBitrate": 120_000_000,
		"MaxStaticBitrate":    120_000_000,
		"DirectPlayProfiles": []map[string]any{{
			"Type": "Video", "Container": "mkv,mp4,ts", "VideoCodec": "h264,hevc,av1", "AudioCodec": "aac,ac3,eac3",
		}},
		"TranscodingProfiles": []map[string]any{
			{"Type": "Video", "Context": "Streaming", "Container": "ts", "Protocol": "hls", "VideoCodec": "hevc,h264", "AudioCodec": "aac,ac3,eac3"},
			{"Type": "Video", "Context": "Streaming", "Container": "mp4", "Protocol": "hls", "VideoCodec": "hevc,h264", "AudioCodec": "aac,ac3,eac3"},
		},
		"CodecProfiles": []map[string]any{{
			"Type": "Video", "Codec": "hevc",
			"Conditions":      []map[string]any{{"Condition": "NotEquals", "Property": "VideoRangeType", "Value": values, "IsRequired": false}},
			"ApplyConditions": []map[string]any{{"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": values, "IsRequired": false}},
		}},
	}
	body, _ := json.Marshal(map[string]any{"DeviceProfile": profile})
	return string(body)
}

var (
	androidTVDVProfile8Disabled = []string{"DOVIInvalid", "DOVIWithEL", "DOVIWithELHDR10Plus", "DOVI", "DOVIWithHDR10", "DOVIWithHDR10Plus"}
	androidTVSDRDisplay         = append(append([]string(nil), androidTVDVProfile8Disabled...), "HDR10Plus", "HDR10")
	androidTVDolbyVisionDisplay = []string{"DOVIInvalid", "DOVIWithEL", "DOVIWithELHDR10Plus"}
)

// newDVStripHandler serves one profile 8.1 version with tone mapping off (the
// server default), so a full HDR encode is never available.
func newDVStripHandler(t *testing.T, localStrip bool) (*PlaybackHandler, string) {
	t.Helper()
	handler, routeID := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	version.FilePath = "/media/movie.mkv"
	version.CodecVideo, version.CodecAudio = "hevc", "eac3"
	version.Bitrate = 15_183
	version.HDR = true
	version.VideoTracks = []models.VideoTrack{dvStripProfile8Track()}
	version.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}
	version.SubtitleTracks = nil
	handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{}}
	handler.compatDVRPUProbe = func(context.Context, string) bool { return true }
	handler.compatDVStripLocalProbe = func() bool { return localStrip }
	return handler, routeID
}

func TestPlaybackInfoStripsDolbyVisionForClientThatRejectsIt(t *testing.T) {
	handler, routeID := newDVStripHandler(t, true)

	response := postPlaybackInfo(t, handler, routeID, androidTVRangeProfile(androidTVDVProfile8Disabled...))

	if len(response.MediaSources) != 1 {
		t.Fatalf("media sources = %#v, want one", response.MediaSources)
	}
	dto := response.MediaSources[0]
	if dto.SupportsDirectPlay || dto.SupportsDirectStream || !dto.SupportsTranscoding ||
		!strings.HasPrefix(dto.TranscodingURL, "/Videos/"+routeID+"/remux-dv-v1/master.m3u8?") {
		t.Fatalf("media source = %+v, want only the remux-dv-v1 HLS route", dto)
	}
	stored, ok := handler.playbackStore.Get(response.PlaySessionID)
	if !ok || len(stored.MediaSources) != 1 {
		t.Fatalf("stored session = %#v, want one negotiated source", stored)
	}
	source := stored.MediaSources[0]
	if !source.DVStripToHDR10 || !source.HLSRemux || source.TranscodeAudio || source.DOVIVariant {
		t.Fatalf("stored source = %+v, want an HDR10 strip remux with copied audio", source)
	}
	if got := compatPrimaryVideoTrack(source.Version).VideoRangeType; got != "DOVIWithHDR10" {
		t.Fatalf("stored video range type = %q, want the original file described", got)
	}
}

func TestPlaybackInfoDoesNotStripWhenClientPlaysDolbyVision(t *testing.T) {
	handler, routeID := newDVStripHandler(t, true)

	response := postPlaybackInfo(t, handler, routeID, androidTVRangeProfile(androidTVDolbyVisionDisplay...))

	stored, _ := handler.playbackStore.Get(response.PlaySessionID)
	if len(response.MediaSources) != 1 || !response.MediaSources[0].SupportsDirectPlay ||
		stored == nil || stored.MediaSources[0].DVStripToHDR10 {
		t.Fatalf("media sources = %+v, want direct play of the untouched file", response.MediaSources)
	}
}

func TestPlaybackInfoWithoutStripOrToneMapStillHasNoRoute(t *testing.T) {
	for name, tc := range map[string]struct {
		unsupported []string
		localStrip  bool
	}{
		"SDR display rejects the HDR10 base layer": {unsupported: androidTVSDRDisplay, localStrip: true},
		"no executor has the dovi_rpu filter":      {unsupported: androidTVDVProfile8Disabled, localStrip: false},
	} {
		t.Run(name, func(t *testing.T) {
			handler, routeID := newDVStripHandler(t, tc.localStrip)

			rr := servePlaybackInfo(handler, routeID, androidTVRangeProfile(tc.unsupported...))

			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "PlaybackUnavailable") {
				t.Fatalf("status = %d body = %s, want PlaybackUnavailable", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPlaybackInfoSkipsStripWhenRPUCannotBeStripped(t *testing.T) {
	handler, routeID := newDVStripHandler(t, true)
	handler.compatDVRPUProbe = func(context.Context, string) bool { return false }

	rr := servePlaybackInfo(handler, routeID, androidTVRangeProfile(androidTVDVProfile8Disabled...))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want no strip route for an unparseable RPU", rr.Code, rr.Body.String())
	}
}

func TestCompatDVStripCandidateMatchesNativePlanner(t *testing.T) {
	for name, tc := range map[string]struct {
		track models.VideoTrack
		want  bool
	}{
		"profile 8.1":         {track: models.VideoTrack{Codec: "hevc", DVProfile: 8, DVBLCompatID: 1}, want: true},
		"profile 7":           {track: models.VideoTrack{Codec: "hevc", DVProfile: 7, DVBLCompatID: 6}, want: true},
		"profile 8.4 (HLG)":   {track: models.VideoTrack{Codec: "hevc", DVProfile: 8, DVBLCompatID: 4}},
		"profile 5 (no base)": {track: models.VideoTrack{Codec: "hevc", DVProfile: 5}},
		"AV1 profile 10":      {track: models.VideoTrack{Codec: "av1", DVProfile: 10, DVBLCompatID: 1}},
		"plain HDR10":         {track: models.VideoTrack{Codec: "hevc", VideoRangeType: "HDR10"}},
	} {
		t.Run(name, func(t *testing.T) {
			version := catalog.FileVersion{VideoTracks: []models.VideoTrack{tc.track}}
			if got := compatDVStripCandidate(version); got != tc.want {
				t.Fatalf("compatDVStripCandidate = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCompatHDR10BaseVersionDescribesTheBaseLayer(t *testing.T) {
	original := catalog.FileVersion{VideoTracks: []models.VideoTrack{dvStripProfile8Track()}}

	base := compatHDR10BaseVersion(original)

	if got := compatVideoRangeType(compatPrimaryVideoTrack(base), base.HDR); got != "HDR10" {
		t.Fatalf("base range type = %q, want HDR10", got)
	}
	if original.VideoTracks[0].DVProfile != 8 || original.VideoTracks[0].VideoRangeType != "DOVIWithHDR10" {
		t.Fatalf("original version was mutated: %+v", original.VideoTracks[0])
	}
	plus := dvStripProfile8Track()
	plus.HDR10Plus = true
	if got := compatHDR10BaseVersion(catalog.FileVersion{VideoTracks: []models.VideoTrack{plus}}).VideoTracks[0].VideoRangeType; got != "HDR10Plus" {
		t.Fatalf("HDR10+ base range type = %q, want HDR10Plus", got)
	}
}

func TestCompatCopyVideoRecipeStripsToHVC1(t *testing.T) {
	entry, filter := compatCopyVideoRecipe(PlaybackMediaSource{DVStripToHDR10: true}, 8)
	if entry != playback.VideoSampleEntryHVC1 || filter != playback.DV7ToHDR10BitstreamFilter {
		t.Fatalf("strip recipe = (%q, %q), want hvc1 with %q", entry, filter, playback.DV7ToHDR10BitstreamFilter)
	}
	entry, filter = compatCopyVideoRecipe(PlaybackMediaSource{}, 8)
	if entry != playback.VideoSampleEntryDVH1 || filter != "" {
		t.Fatalf("Dolby Vision copy recipe = (%q, %q), want dvh1 without a filter", entry, filter)
	}
}

func TestCompatCopyVideoMasterManifestForStripOmitsDolbyVision(t *testing.T) {
	source := PlaybackMediaSource{
		ID: "source-1",
		Version: catalog.FileVersion{
			Bitrate: 15_183,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 1920,
				DVProfile: 8, DVLevel: 6, DVBLCompatID: 1, VideoRangeType: "DOVIWithHDR10",
			}},
			AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
		},
		HLSRemux:       true,
		DVStripToHDR10: true,
	}

	got := string(generateCompatCopyVideoMasterManifest(source, "item-1", "play-1", ""))

	if !strings.Contains(got, "VIDEO-RANGE=PQ") || !strings.Contains(got, `CODECS="hvc1.2.4.L150.B0,ec-3"`) {
		t.Fatalf("strip master is missing HDR10 range/codec metadata:\n%s", got)
	}
	if strings.Contains(got, "SUPPLEMENTAL-CODECS") || strings.Contains(got, "dvh1") {
		t.Fatalf("strip master still advertises Dolby Vision:\n%s", got)
	}
}

func TestCompatCopyVideoMasterManifestForProfile7StripAdvertisesPQ(t *testing.T) {
	source := PlaybackMediaSource{
		ID: "source-1",
		Version: catalog.FileVersion{
			Bitrate: 60_000,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160,
				DVProfile: 7, DVLevel: 6, DVBLCompatID: 6, VideoRangeType: "DOVIWithEL",
			}},
			AudioTracks: []models.AudioTrack{{Codec: "aac", Profile: "LC", Default: true}},
		},
		HLSRemux:       true,
		DVStripToHDR10: true,
	}

	got := string(generateCompatCopyVideoMasterManifest(source, "item-1", "play-1", ""))

	if !strings.Contains(got, "VIDEO-RANGE=PQ") || strings.Contains(got, "dvh1") {
		t.Fatalf("profile 7 strip master must describe the HDR10 base layer:\n%s", got)
	}
}

func TestCompatWebOSDolbyVisionMPEGTSSkipsStrip(t *testing.T) {
	source := PlaybackMediaSource{
		HLSRemux:       true,
		DVStripToHDR10: true,
		Version:        catalog.FileVersion{VideoTracks: []models.VideoTrack{dvStripProfile8Track()}},
	}
	if compatWebOSDVMPEGTS("Mozilla/5.0 (Web0S; Linux/SmartTV)", source) {
		t.Fatal("an HDR10 strip must keep fMP4 packaging; MPEG-TS exists for webOS Dolby Vision decoding")
	}
}

func TestCompatDVStripRoutingRequiresCapableExecutors(t *testing.T) {
	capableServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
			Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1",
		}}})
	}))
	defer capableServer.Close()
	legacyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{})
	}))
	defer legacyServer.Close()
	capable := &nodepool.Node{URL: capableServer.URL, Enabled: true, Healthy: true}
	legacy := &nodepool.Node{URL: legacyServer.URL, Enabled: true, Healthy: true}
	handler := &PlaybackHandler{
		NodePlanner:             compatToneMapInventoryPlanner{urls: []string{capable.URL, legacy.URL}},
		compatDVStripLocalProbe: func() bool { return false },
	}

	eligible, excluded := handler.compatDVStripRouting(context.Background(), nil, map[string]struct{}{"other": {}})

	if !eligible(capable) || eligible(legacy) || eligible(nil) {
		t.Fatal("strip routing must accept only nodes advertising server_dv7_to_hdr10")
	}
	if _, ok := excluded[noderouting.ShapeHLSRemuxAPI]; !ok {
		t.Fatalf("excluded shapes = %v, want the API remux shape removed when local FFmpeg lacks dovi_rpu", excluded)
	}
	if _, ok := excluded["other"]; !ok {
		t.Fatalf("excluded shapes = %v, want existing exclusions kept", excluded)
	}

	handler.compatDVStripLocalProbe = func() bool { return true }
	if _, excluded = handler.compatDVStripRouting(context.Background(), nil, nil); len(excluded) != 0 {
		t.Fatalf("excluded shapes = %v, want the API remux shape kept when local FFmpeg can strip", excluded)
	}
}
