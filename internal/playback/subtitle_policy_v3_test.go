package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// subtitlePolicyForTrackV3 selects the only subtitle track, of codec, on the
// fixture file (or an ordinal past it) and resolves the original_http
// subtitle policy with or without transcoding allowed.
func subtitlePolicyForTrackV3(codec string, index int) func(transcodeAllowed bool) SubtitlePolicyResultV3 {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: codec}}
	req := validStartRequestV3()
	req.SubtitleTrackIndex = &index
	return func(transcodeAllowed bool) SubtitlePolicyResultV3 {
		return ResolveSubtitlePolicyV3(file, req, transcodeAllowed, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	}
}

func TestResolveSubtitlePolicyV3RendersCEA608AsTextArtifact(t *testing.T) {
	result := subtitlePolicyForTrackV3("eia_608", 0)(true)
	if result.Terminal != nil || result.RequiresBurn || result.Decision.Mode != SubtitleRenderV3 {
		t.Fatalf("CEA-608 should render as a client-styled text artifact: %#v", result)
	}
	if result.Claims.Reason != "client_render_supported" || result.Claims.BitmapOverlay {
		t.Fatalf("CEA-608 claims = %#v", result.Claims)
	}
}

func TestResolveSubtitlePolicyV3DoesNotOfferDVBTeletextAsClientBitmap(t *testing.T) {
	result := subtitlePolicyForTrackV3("dvb_teletext", 0)(true)
	if result.Terminal != nil || !result.RequiresBurn || result.Decision.Mode != SubtitleBurnInV3 {
		t.Fatalf("DVB teletext must stay on the server fallback: %#v", result)
	}
	if !result.Claims.BitmapOverlay {
		t.Fatalf("DVB teletext burn-in must retain bitmap overlay semantics: %#v", result.Claims)
	}
}

func TestResolveSubtitlePolicyV3UnknownCodecIsExplicitlyUnsupported(t *testing.T) {
	for codec, message := range map[string]string{
		"arib_caption": "Subtitle format arib_caption has no validated rendering or burn-in route.",
		// A format the probe couldn't name has no name to give.
		"": "The subtitle track's format was not recognized, so it has no validated rendering or burn-in route.",
	} {
		resolve := subtitlePolicyForTrackV3(codec, 0)
		for _, transcodeAllowed := range []bool{true, false} {
			result := resolve(transcodeAllowed)
			if result.Terminal == nil || result.Terminal.Reason != "subtitle_codec_unsupported" || result.Terminal.Message != message {
				t.Fatalf("codec %q, transcodeAllowed=%v: unknown codec must be explicitly unsupported, got %#v", codec, transcodeAllowed, result.Terminal)
			}
		}
	}
}

// The inventory marks exactly the tracks the policy refuses as unsupported,
// so a client can list them as unavailable instead of offering them.
func TestSubtitleInventoryV3MarksTracksWithoutARouteUnplayable(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = []models.ExternalSubtitle{{Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 2, Codec: ""}, {Index: 3, Codec: "webvtt", IndexOnly: true}, {Index: 4, Codec: "dvd_subtitle"}, {Index: 5, Codec: "arib_caption"}}
	want := []bool{false, true, false, false, true}
	items := BuildSubtitleInventoryV3(file, nil)
	for i, item := range items {
		if item.Unplayable != want[i] {
			t.Errorf("track %d (%s %q): unplayable = %v, want %v", i, item.Source, item.Codec, item.Unplayable, want[i])
		}
	}
	if len(items) != len(want) {
		t.Fatalf("inventory has %d tracks, want %d", len(items), len(want))
	}
}

// A track FFmpeg can't demux from the source (Matroska S_TEXT/WEBVTT) renders
// from its sidecar, which the server reads through the index, but can't be
// burned in: burn-in demuxes the source.
func TestResolveSubtitlePolicyV3KeepsIndexOnlyTracksOffBurnIn(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 2, ContainerTrackID: "3", Codec: "webvtt", IndexOnly: true}}
	req := validStartRequestV3()
	req.SubtitleTrackIndex = new(0)
	resolve := func() SubtitlePolicyResultV3 {
		return ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	}
	if got := resolve(); got.Terminal != nil || got.RequiresBurn || got.Decision.Mode != SubtitleRenderV3 || got.Codec != "webvtt" {
		t.Fatalf("sidecar: %#v", got)
	}
	caps := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	caps.Subtitles.SidecarText = false
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = caps
	if got := resolve(); got.Terminal == nil || got.Terminal.Reason != "subtitle_burn_in_source_unsupported" || got.RequiresBurn {
		t.Fatalf("without a sidecar: %#v", got)
	}
	file.SubtitleTracks[0].IndexOnly = false
	if got := resolve(); !got.RequiresBurn {
		t.Fatalf("an ordinary WebVTT track without a sidecar burns in: %#v", got)
	}
}

func TestResolveSubtitlePolicyV3BurnsFFmpegBitmapAliases(t *testing.T) {
	result := subtitlePolicyForTrackV3("dvdsub", 0)(true)
	if result.Terminal != nil || !result.RequiresBurn || result.Decision.Mode != SubtitleBurnInV3 || !result.Claims.BitmapOverlay {
		t.Fatalf("dvdsub alias must burn in like dvd_subtitle: %#v", result)
	}
}

func TestPlanPlaybackV3TranscodeUsesHLSDeliverySubtitleCapabilities(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "srt"}}
	req := validStartRequestV3()
	// A fixed rung forces the HLS transcode route; the fixture's direct
	// original HTTP renders embedded text while the HLS delivery cannot.
	req.QualityPreference = "1080p"
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	index := 0
	req.SubtitleTrackIndex = &index
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.Subtitle.Mode == SubtitleRenderV3 {
		t.Fatalf("HLS transcode plan claims client render the HLS engine cannot honor: %#v", result.Plan.Subtitle)
	}
	if result.Plan.Subtitle.Mode != SubtitleBurnInV3 || result.Plan.Claims.Subtitles.Reason != "server_burn_in_required" {
		t.Fatalf("subtitle decision = %#v claims = %#v", result.Plan.Subtitle, result.Plan.Claims.Subtitles)
	}
	if result.SubtitleTrackIndex != index {
		t.Fatalf("subtitle track index = %d", result.SubtitleTrackIndex)
	}
}

// A selection carried over from another file — the next episode routinely has
// a different track layout — must degrade to subtitles-off, not refuse
// playback. This is the direct-start path; the version-switch remap in the
// handlers degrades the same way.
func TestResolveSubtitlePolicyV3DegradesOutOfRangeSelectionToOff(t *testing.T) {
	result := subtitlePolicyForTrackV3("subrip", 7)(true)
	if result.Terminal != nil {
		t.Fatalf("an out-of-range subtitle selection must not refuse playback: %#v", result.Terminal)
	}
	if result.Decision.Mode != SubtitleOffV3 || result.SelectedIndex != -1 {
		t.Fatalf("expected subtitles-off fallback: %#v", result)
	}
	if !result.Degraded {
		t.Fatal("the fallback was not reported as a degradation")
	}
}

// Malformed selections stay terminal: a bad identity is a client bug, and
// swallowing it would make that bug harder to find.
func TestResolveSubtitlePolicyV3StillRejectsInvalidIdentity(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.SubtitleTrackID = "not-a-track-id"

	result := ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	if result.Terminal == nil || result.Terminal.Reason != "subtitle_track_invalid" {
		t.Fatalf("a malformed identity must stay terminal: %#v", result)
	}
}
