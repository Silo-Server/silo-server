package playback

import (
	"slices"
	"testing"
)

// The admin activity views say why a session adapts from these reasons, so
// each cause has to name itself rather than the quality step a plan's
// decision_reason reports.
func TestPlanPlaybackV3NamesWhyTheRouteAdapts(t *testing.T) {
	for _, test := range []struct {
		name   string
		adjust func(*PlannerInputV3)
		method PlayMethod
		want   []string
	}{
		{name: "the original plays as it is", method: PlayDirect},
		{name: "evidence that cannot confirm the decode", method: PlayTranscode, want: []string{AdaptationVideoCapabilityUnconfirmed},
			adjust: func(in *PlannerInputV3) {
				in.Request.Capabilities.VideoEvidence = EvidenceExactV3
				in.Request.Capabilities.VideoDecode = nil
			}},
		{name: "a video codec the device cannot decode", method: PlayTranscode, want: []string{AdaptationVideoCodecUnsupported},
			adjust: func(in *PlannerInputV3) {
				in.EffectiveFile.CodecVideo, in.EffectiveFile.VideoTracks[0].Codec = "hevc", "hevc"
				in.EffectiveFile.VideoTracks[0].Profile = "Main"
			}},
		// The replan after the original failed passes it over for a remux.
		{name: "an original the device failed to play", method: PlayRemux, want: []string{AdaptationDirectPlayFailed},
			adjust: func(in *PlannerInputV3) {
				original := PlanPlaybackV3(*in)
				in.AttemptedKeys = []string{PlanAttemptKeyV3(*original.Plan, in.Request.ClientPlaybackContext.Output.OutputContextID, nil)}
			}},
		{name: "a container the device cannot open", method: PlayRemux, want: []string{AdaptationContainerUnsupported},
			adjust: func(in *PlannerInputV3) { in.Request.Capabilities.Containers = []string{"mp4"} }},
		{name: "an audio codec the device cannot play", method: PlayRemux, want: []string{AdaptationAudioCodecUnsupported},
			adjust: func(in *PlannerInputV3) {
				in.EffectiveFile.CodecAudio, in.EffectiveFile.AudioTracks[0].Codec = "dts", "dts"
			}},
		{name: "a source taller than the device allows", method: PlayTranscode, want: []string{AdaptationResolutionLimit},
			adjust: func(in *PlannerInputV3) {
				in.Request.QualityPreference = QualityAutoV3
				in.Request.Capabilities.MaxResolution = "720p"
			}},
		{name: "a probed connection too slow for the source", method: PlayTranscode, want: []string{AdaptationBandwidthLimit},
			adjust: func(in *PlannerInputV3) {
				in.Request.QualityPreference = QualityAutoV3
				in.Request.BandwidthEstimateKbps = new(3_000)
				in.Request.BandwidthEstimateSource = BandwidthEstimateProbeV3
			}},
		{name: "buffering lowered the estimate", method: PlayTranscode, want: []string{AdaptationBuffering},
			adjust: func(in *PlannerInputV3) {
				in.Request.QualityPreference = QualityAutoV3
				in.Request.BandwidthEstimateKbps = new(3_000)
				in.Request.BandwidthEstimateSource = BandwidthEstimateObservedRebufferV3
			}},
		{name: "the request's own bandwidth cap", method: PlayTranscode, want: []string{AdaptationBandwidthCap},
			adjust: func(in *PlannerInputV3) {
				in.Request.QualityPreference = QualityAutoV3
				in.Request.BandwidthCapKbps = new(4_000)
			}},
		{name: "the server's lower bitrate limit", method: PlayTranscode, want: []string{AdaptationServerBitrateLimit},
			adjust: func(in *PlannerInputV3) {
				in.Request.QualityPreference = QualityAutoV3
				in.Request.BandwidthCapKbps = new(8_000)
				in.ServerBitrateCapKbps = 4_000
			}},
		{name: "a quality the viewer chose", method: PlayTranscode, want: []string{AdaptationQualitySelected},
			adjust: func(in *PlannerInputV3) { in.Request.QualityPreference = "720p" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.Capabilities.CodecsVideo = []string{"h264"}
			req.Capabilities.VideoEvidence = EvidencePlatformAttestedV3
			req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, BitDepths: []int{8}}}
			file := activeQualityFixtureV3()
			input := PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
				LocalNetwork: true,
			}
			if test.adjust != nil {
				test.adjust(&input)
			}
			result := PlanPlaybackV3(input)
			if result.Plan == nil {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if result.PlayMethod != test.method || !slices.Equal(result.AdaptationReasons, test.want) {
				t.Fatalf("method %s reasons %v, want %s %v (decision_reason %s)", result.PlayMethod, result.AdaptationReasons,
					test.method, test.want, result.Plan.DecisionReason)
			}
			// A seek reanchor rebuilds the session from the frozen recipe.
			if thawed := FreezeExecutableRecipeV3(result).PlannerResult(result.Plan); !slices.Equal(thawed.AdaptationReasons, test.want) {
				t.Fatalf("reasons after a seek reanchor = %v, want %v", thawed.AdaptationReasons, test.want)
			}
		})
	}
}
