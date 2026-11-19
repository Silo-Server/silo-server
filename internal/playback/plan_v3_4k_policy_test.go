package playback

import (
	"testing"
)

func TestPlanPlaybackV3DirectPlays4KWhen4KTranscodeDisabled(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "UHD"
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Containers = []string{"mkv"}
	direct.VideoCodecs = []string{"hevc"}
	direct.AudioDecodeCodecs = []string{"aac"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3(),
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("a directly playable 4K source was blocked by the transcode policy: %s", ExplainPlannerResultV3(result))
	}
}
