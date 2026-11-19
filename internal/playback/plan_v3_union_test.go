package playback

import (
	"testing"
)

func staticHLSRegistryV3(registry *TransformationRegistryV3) func() *TransformationRegistryV3 {
	return func() *TransformationRegistryV3 { return registry }
}

// A deployment whose API host lacks the H.264/AAC toolchain must still plan
// an HLS transcode when pooled transcode nodes advertise it, and must keep
// the terminal when nothing does.
func TestPlanPlaybackV3TranscodeOffloadsToNodeToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.QualityPreference = "480p"
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	local := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "2"},
		{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
	})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "conversion_tool_unavailable" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
		{Name: "audio_to_aac", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
	})
	withNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSVideoRegistry: staticHLSRegistryV3(union)})
	if withNodes.Plan == nil || withNodes.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(withNodes))
	}
}
