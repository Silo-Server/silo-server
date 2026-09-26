package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestResolveSubtitlePolicyV3PreservesTextThroughSidecarOnlyRenderer(t *testing.T) {
	file := detailedFixtureFileV3()
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}
	request := validStartRequestV3()
	request.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	request.SubtitleTrackIndex = new(0)
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: DeliverySubtitleCapabilitiesV3{SidecarText: true},
	}
	result := ResolveSubtitlePolicyV3(file, request, false, DeliveryClassOriginalHTTPV3, nil)
	if result.Terminal != nil || result.Decision.Mode != SubtitleRenderV3 || result.Decision.Embedded != nil {
		t.Fatalf("a text sidecar renderer must not require embedded decoding support: %#v", result)
	}
}

func TestPlanPlaybackV3SubtitleSupportOnAdaptedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, delivery string
		want           DeliveryV3
		external       bool
	}{
		{"external_ass_progressive", DeliveryClassProgressiveV3, DeliveryRemuxProgressiveV3, true},
		{"external_ass_hls", DeliveryClassHLSV3, DeliveryRemuxHLSV3, true},
		{"embedded_text_progressive", DeliveryClassProgressiveV3, DeliveryRemuxProgressiveV3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := detailedFixtureFileV3()
			file.VideoTracks[0].VideoRange = "SDR"
			file.VideoTracks[0].VideoRangeType = "SDR"
			file.VideoTracks[0].ColorTransfer = "bt709"
			if tc.external {
				file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.ass", Format: "ass"}}
			} else {
				file.SubtitleTracks = []models.SubtitleTrack{{Codec: "subrip"}}
			}
			request := validStartRequestV3()
			request.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
			request.SubtitleTrackIndex = new(0)
			request.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", 0)
			request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
				Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153},
				BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60,
				MaxBitrateKbps: 80_000, Hardware: true,
			}}
			for delivery, capability := range request.ClientPlaybackContext.Deliveries {
				capability.Subtitles = DeliverySubtitleCapabilitiesV3{}
				if delivery == tc.delivery {
					capability.Subtitles = DeliverySubtitleCapabilitiesV3{
						EmbeddedText: true, SidecarText: true, ASSStyling: true, FontAttachments: true,
					}
				}
				request.ClientPlaybackContext.Deliveries[delivery] = capability
			}
			result := PlanPlaybackV3(PlannerInputV3{
				Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: false}, Registry: testTransformationRegistryV3(),
			})
			if result.Plan == nil || result.Plan.Delivery != tc.want {
				t.Fatalf("expected subtitle-capable %s route, got %s", tc.want, ExplainPlannerResultV3(result))
			}
			if result.Plan.Subtitle.Mode != SubtitleRenderV3 || result.Plan.SelectedTracks.Subtitle == nil ||
				result.Plan.SelectedTracks.Subtitle.ID != request.SubtitleTrackID || result.SubtitleTrackIndex != 0 {
				t.Fatalf("adapted route lost the subtitle selection: %#v", result)
			}
		})
	}
}
