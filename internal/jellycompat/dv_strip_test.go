package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
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
	return rangeProfileJSON(nil, unsupportedHEVC...)
}

// rangeProfileJSON builds the Android TV range profile with extra HEVC
// conditions, such as the VideoCodecTag requirement Jellyfin Web sends on
// Safari.
func rangeProfileJSON(extraHEVC []map[string]any, unsupportedHEVC ...string) string {
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
		}, {
			"Type": "Video", "Codec": "hevc", "Conditions": extraHEVC,
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

// dvStripNodePlanner lists and resolves pooled transcode nodes by URL, as
// *nodepool.Planner does.
type dvStripNodePlanner struct {
	compatToneMapInventoryPlanner
	nodes map[string]*nodepool.Node
}

func (p dvStripNodePlanner) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	node, ok := p.nodes[nodeURL]
	return node, ok
}

func dvStripNode(t *testing.T, url string, transformations ...playback.TransformationV3) *nodepool.Node {
	t.Helper()
	report, err := json.Marshal(playback.HWAccelInfo{Transformations: transformations})
	if err != nil {
		t.Fatal(err)
	}
	return &nodepool.Node{URL: url, Enabled: true, Healthy: true, Capabilities: report}
}

var dvStripTransformation = playback.TransformationV3{
	Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationServerDV7HDR10RecipeVersionV3,
}

func TestCompatDVStripRoutingRequiresCapableExecutors(t *testing.T) {
	capable := dvStripNode(t, "http://capable:8080", dvStripTransformation)
	legacy := dvStripNode(t, "http://legacy:8080")
	// A node still on recipe 1 strips the RPUs but keeps the enhancement-layer
	// NAL units, so it cannot run the current recipe.
	staleTransformation := dvStripTransformation
	staleTransformation.RecipeVersion = "1"
	stale := dvStripNode(t, "http://stale:8080", staleTransformation)
	handler := &PlaybackHandler{compatDVStripLocalProbe: func() bool { return false }}

	eligible, excluded := handler.compatDVStripRouting(context.Background(), nil, map[string]struct{}{"other": {}}, 0)

	if !eligible(capable) || eligible(legacy) || eligible(stale) || eligible(nil) {
		t.Fatal("strip routing must accept only nodes advertising the current server_dv7_to_hdr10 recipe")
	}
	if _, ok := excluded[noderouting.ShapeHLSRemuxAPI]; !ok {
		t.Fatalf("excluded shapes = %v, want the API remux shape removed when local FFmpeg lacks dovi_rpu", excluded)
	}
	if _, ok := excluded["other"]; !ok {
		t.Fatalf("excluded shapes = %v, want existing exclusions kept", excluded)
	}

	handler.compatDVStripLocalProbe = func() bool { return true }
	if _, excluded = handler.compatDVStripRouting(context.Background(), nil, nil, 0); len(excluded) != 0 {
		t.Fatalf("excluded shapes = %v, want the API remux shape kept when local FFmpeg can strip", excluded)
	}
}

func TestCompatDVStripDecisionsNeverWaitOnNodes(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	capable := dvStripNode(t, server.URL, dvStripTransformation)
	handler := &PlaybackHandler{
		NodePlanner: dvStripNodePlanner{
			compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{capable.URL}},
			nodes:                         map[string]*nodepool.Node{capable.URL: capable},
		},
		compatDVRPUProbe:        func(context.Context, string) bool { return true },
		compatDVStripLocalProbe: func() bool { return false },
	}

	if !handler.compatDVStripExecutable(context.Background(), catalog.FileVersion{FilePath: "/media/movie.mkv"}, 0) {
		t.Fatal("strip not executable although a pooled node's stored report advertises it")
	}
	eligible, _ := handler.compatDVStripRouting(context.Background(), nil, nil, 0)
	if !eligible(capable) {
		t.Fatal("capable node rejected during route selection")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("node requests = %d, want negotiation and route selection to read stored reports only", got)
	}

	handler.NodePlanner = dvStripNodePlanner{
		compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{"http://legacy:8080"}},
		nodes:                         map[string]*nodepool.Node{"http://legacy:8080": dvStripNode(t, "http://legacy:8080")},
	}
	if handler.compatDVStripExecutable(context.Background(), catalog.FileVersion{FilePath: "/media/movie.mkv"}, 0) {
		t.Fatal("strip executable with neither a local filter nor a capable node")
	}
}

func TestCompatDVStripRequiresAudioBoostOnTheSameNode(t *testing.T) {
	audioBoost := playback.TransformationV3{
		Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}
	stripOnly := dvStripNode(t, "http://strip:8080", dvStripTransformation)
	audioOnly := dvStripNode(t, "http://audio:8080", audioBoost)
	handler := &PlaybackHandler{
		NodePlanner: dvStripNodePlanner{
			compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{stripOnly.URL, audioOnly.URL}},
			nodes:                         map[string]*nodepool.Node{stripOnly.URL: stripOnly, audioOnly.URL: audioOnly},
		},
		compatDVRPUProbe:        func(context.Context, string) bool { return true },
		compatDVStripLocalProbe: func() bool { return false },
	}
	version := catalog.FileVersion{FilePath: "/media/movie.mkv"}

	if !handler.compatDVStripExecutable(context.Background(), version, 0) {
		t.Fatal("strip with copied audio should use the strip-capable node")
	}
	if handler.compatDVStripExecutable(context.Background(), version, 6) {
		t.Fatal("strip with a surround downmix needs one node with both recipes")
	}

	both := dvStripNode(t, "http://both:8080", dvStripTransformation, audioBoost)
	handler.NodePlanner = dvStripNodePlanner{
		compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{both.URL}},
		nodes:                         map[string]*nodepool.Node{both.URL: both},
	}
	if !handler.compatDVStripExecutable(context.Background(), version, 6) {
		t.Fatal("a node with both recipes should carry a downmixing strip")
	}
}

func TestPlaybackInfoStripsForClientRequiringHVC1(t *testing.T) {
	handler, routeID := newDVStripHandler(t, true)
	requireHVC1 := []map[string]any{{"Condition": "EqualsAny", "Property": "VideoCodecTag", "Value": "hvc1|dvh1", "IsRequired": true}}

	response := postPlaybackInfo(t, handler, routeID, rangeProfileJSON(requireHVC1, androidTVDVProfile8Disabled...))

	stored, _ := handler.playbackStore.Get(response.PlaySessionID)
	if stored == nil || len(stored.MediaSources) != 1 || !stored.MediaSources[0].DVStripToHDR10 {
		t.Fatalf("media sources = %+v, want the strip, whose output is tagged hvc1", response.MediaSources)
	}
}

func TestCompatDVStripProbesSourceOnlyWithAnExecutor(t *testing.T) {
	probed := false
	handler := &PlaybackHandler{
		compatDVRPUProbe:        func(context.Context, string) bool { probed = true; return true },
		compatDVStripLocalProbe: func() bool { return false },
	}

	if handler.compatDVStripExecutable(context.Background(), catalog.FileVersion{FilePath: "/media/movie.mkv"}, 0) {
		t.Fatal("strip executable without any executor")
	}
	if probed {
		t.Fatal("the source was probed although no executor could strip it")
	}
}

func TestCompatTranscodeNodeCanStripReadsStoredReport(t *testing.T) {
	capable := dvStripNode(t, "http://capable:8080", dvStripTransformation)
	legacy := dvStripNode(t, "http://legacy:8080")
	handler := &PlaybackHandler{NodePlanner: dvStripNodePlanner{
		nodes: map[string]*nodepool.Node{capable.URL: capable, legacy.URL: legacy},
	}}

	if !handler.compatTranscodeNodeCanStrip(capable.URL + "/") {
		t.Fatal("remote start rejected a node whose stored report advertises the strip")
	}
	if handler.compatTranscodeNodeCanStrip(legacy.URL) || handler.compatTranscodeNodeCanStrip("http://gone:8080") {
		t.Fatal("remote start accepted a node without the strip recipe")
	}
}

func TestCompatDVStripOnAPIHostRequiresLocalAudioRecipeForDownmix(t *testing.T) {
	localRegistry := func(audio bool) func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
		return func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
			specs := []playback.TransformationSpecV3{{Name: playback.TransformationServerDV7HDR10V3, RecipeVersion: playback.TransformationServerDV7HDR10RecipeVersionV3, Available: true}}
			if audio {
				specs = append(specs, playback.TransformationSpecV3{
					Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true,
				})
			}
			return playback.NewTransformationRegistryV3(specs), nil
		}
	}
	version := catalog.FileVersion{FilePath: "/media/movie.mkv"}
	for name, tc := range map[string]struct {
		audio    bool
		channels int
		want     bool
	}{
		"copied audio needs only the strip":     {audio: false, channels: 0, want: true},
		"downmix without local audio_to_aac v2": {audio: false, channels: 6, want: false},
		"downmix with local audio_to_aac v2":    {audio: true, channels: 6, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			handler := &PlaybackHandler{
				compatDVRPUProbe:         func(context.Context, string) bool { return true },
				compatAudioRegistryProbe: localRegistry(tc.audio),
			}
			if got := handler.compatDVStripExecutable(context.Background(), version, tc.channels); got != tc.want {
				t.Fatalf("compatDVStripExecutable = %t, want %t", got, tc.want)
			}
			// Route selection must agree, so a downmix never lands on an API
			// host that lacks the audio recipe.
			_, excluded := handler.compatDVStripRouting(context.Background(), nil, nil, tc.channels)
			if _, apiExcluded := excluded[noderouting.ShapeHLSRemuxAPI]; apiExcluded == tc.want {
				t.Fatalf("API remux shape excluded = %t, want %t", apiExcluded, !tc.want)
			}
		})
	}
}

func TestCompatDVStripIgnoresUnroutableNodes(t *testing.T) {
	unhealthy := dvStripNode(t, "http://unhealthy:8080", dvStripTransformation)
	unhealthy.Healthy = false
	disabled := dvStripNode(t, "http://disabled:8080", dvStripTransformation)
	disabled.Enabled = false
	handler := &PlaybackHandler{
		NodePlanner: dvStripNodePlanner{
			compatToneMapInventoryPlanner: compatToneMapInventoryPlanner{urls: []string{unhealthy.URL, disabled.URL}},
			nodes:                         map[string]*nodepool.Node{unhealthy.URL: unhealthy, disabled.URL: disabled},
		},
		compatDVRPUProbe:        func(context.Context, string) bool { return true },
		compatDVStripLocalProbe: func() bool { return false },
	}

	if handler.compatDVStripExecutable(context.Background(), catalog.FileVersion{FilePath: "/media/movie.mkv"}, 0) {
		t.Fatal("an unhealthy or disabled node made the strip executable")
	}
}
