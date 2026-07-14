package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveSubtitlePolicyV3RendersCEA608AsTextArtifact(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "eia_608"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	result := ResolveSubtitlePolicyV3(file, req, true, EngineMedia3DirectV3, nil)

	if result.Terminal != nil || result.RequiresBurn || result.Decision.Mode != SubtitleRenderV3 {
		t.Fatalf("CEA-608 should render as a client-styled text artifact: %#v", result)
	}
	if result.Claims.Reason != "client_render_supported" || result.Claims.BitmapOverlay {
		t.Fatalf("CEA-608 claims = %#v", result.Claims)
	}
}

func TestResolveSubtitlePolicyV3DoesNotOfferDVBTeletextAsClientBitmap(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "dvb_teletext"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	result := ResolveSubtitlePolicyV3(file, req, true, EngineMedia3DirectV3, nil)

	if result.Terminal != nil || !result.RequiresBurn || result.Decision.Mode != SubtitleBurnInV3 {
		t.Fatalf("DVB teletext must stay on the server fallback: %#v", result)
	}
	if !result.Claims.BitmapOverlay {
		t.Fatalf("DVB teletext burn-in must retain bitmap overlay semantics: %#v", result.Claims)
	}
}

func TestResolveSubtitlePolicyV3UnknownCodecIsExplicitlyUnsupported(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "arib_caption"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	for _, transcodeAllowed := range []bool{true, false} {
		result := ResolveSubtitlePolicyV3(file, req, transcodeAllowed, EngineMedia3DirectV3, nil)
		if result.Terminal == nil || result.Terminal.Reason != "subtitle_codec_unsupported" {
			t.Fatalf("transcodeAllowed=%v: unknown codec must be explicitly unsupported, got %#v", transcodeAllowed, result)
		}
	}
}

func TestResolveSubtitlePolicyV3BurnsFFmpegBitmapAliases(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = nil
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "dvdsub"}}
	req := validStartRequestV3()
	index := 0
	req.SubtitleTrackIndex = &index

	result := ResolveSubtitlePolicyV3(file, req, true, EngineMedia3DirectV3, nil)
	if result.Terminal != nil || !result.RequiresBurn || result.Decision.Mode != SubtitleBurnInV3 || !result.Claims.BitmapOverlay {
		t.Fatalf("dvdsub alias must burn in like dvd_subtitle: %#v", result)
	}
}

func TestPlanPlaybackV3TranscodeUsesHLSEngineSubtitleCapabilities(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "srt"}}
	req := validStartRequestV3()
	// A fixed rung forces the HLS transcode route; the fixture's direct
	// engine renders embedded text while the HLS engine cannot.
	req.QualityPreference = "1080p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
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
	if result.Plan.Subtitle.Mode != SubtitleConvertV3 || result.Plan.Claims.Subtitles.Reason != "server_text_conversion" {
		t.Fatalf("subtitle decision = %#v claims = %#v", result.Plan.Subtitle, result.Plan.Claims.Subtitles)
	}
	if result.SubtitleTrackIndex != index {
		t.Fatalf("subtitle track index = %d", result.SubtitleTrackIndex)
	}
}

func TestClientRenderableBitmapSubtitleV3UsesExactCodecFamilies(t *testing.T) {
	for _, codec := range []string{"pgs", "pgssub", "hdmv_pgs_subtitle", "dvd_subtitle", "dvdsub", "dvb_subtitle", "dvbsub", "vobsub"} {
		if !isClientRenderableBitmapSubtitleV3(codec) {
			t.Errorf("expected client-renderable bitmap codec: %s", codec)
		}
	}
	for _, codec := range []string{"dvb_teletext", "hdmv_text_subtitle", "arib_caption", "eia_608"} {
		if isClientRenderableBitmapSubtitleV3(codec) {
			t.Errorf("must not advertise unsupported client bitmap codec: %s", codec)
		}
	}
}
