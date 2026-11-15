package playback

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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
		{name: "more channels than the original delivery allows", method: PlayRemux, want: []string{AdaptationAudioChannelsUnsupported},
			adjust: func(in *PlannerInputV3) {
				surroundAudioV3(in.EffectiveFile)
				limitDeliveryChannelsV3(in, DeliveryClassOriginalHTTPV3, 2)
			}},
		// A remux under the server's limit that then failed on the device is
		// a failed copy, not the limit.
		{name: "a remux within the server limit that failed", method: PlayTranscode,
			want: []string{AdaptationDirectPlayFailed, AdaptationAudioCodecUnsupported},
			adjust: func(in *PlannerInputV3) {
				in.EffectiveFile.CodecAudio, in.EffectiveFile.AudioTracks[0].Codec = "dts", "dts"
				in.ServerBitrateCapKbps = 100_000
				failCopyRoutesV3(in)
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

// An audio-only file takes the remux for reasons of its own: the delivery the
// original file needs, or a channel ceiling on the progressive delivery.
func TestPlanPlaybackV3NamesWhyAnAudioOnlyRouteAdapts(t *testing.T) {
	for _, test := range []struct {
		name    string
		adjust  func(*PlannerInputV3)
		convert bool
		want    []string
	}{
		{name: "the original plays as it is"},
		{name: "a device that offers no original-file delivery", want: []string{AdaptationContainerUnsupported},
			adjust: func(in *PlannerInputV3) {
				delete(in.Request.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
			}},
		{name: "more channels than the original delivery allows", want: []string{AdaptationAudioChannelsUnsupported},
			adjust: func(in *PlannerInputV3) {
				surroundAudioV3(in.EffectiveFile)
				limitDeliveryChannelsV3(in, DeliveryClassOriginalHTTPV3, 2)
			}},
		{name: "more channels than the delivery allows", convert: true, want: []string{AdaptationAudioChannelsUnsupported},
			adjust: func(in *PlannerInputV3) {
				surroundAudioV3(in.EffectiveFile)
				limitDeliveryChannelsV3(in, DeliveryClassOriginalHTTPV3, 2)
				limitDeliveryChannelsV3(in, DeliveryClassProgressiveV3, 2)
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := audioOnlyFixtureFileV3()
			req := validStartRequestV3()
			req.FileID = file.ID
			req.Capabilities.Containers = []string{"mp4"}
			input := PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
			}
			if test.adjust != nil {
				test.adjust(&input)
			}
			result := PlanPlaybackV3(input)
			if result.Plan == nil {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if result.TranscodeAudio != test.convert || !slices.Equal(result.AdaptationReasons, test.want) {
				t.Fatalf("convert %v reasons %v, want %v %v (delivery %s)", result.TranscodeAudio, result.AdaptationReasons,
					test.convert, test.want, result.Plan.Delivery)
			}
		})
	}
}

// surroundAudioV3 makes a fixture's only audio track 5.1.
func surroundAudioV3(file *models.MediaFile) {
	file.AudioChannels, file.AudioTracks[0].Channels, file.AudioTracks[0].Layout = 6, 6, "5.1"
}

func limitDeliveryChannelsV3(in *PlannerInputV3, class string, channels int) {
	delivery := in.Request.ClientPlaybackContext.Deliveries[class]
	delivery.MaxChannels = new(channels)
	in.Request.ClientPlaybackContext.Deliveries[class] = delivery
}

// failCopyRoutesV3 marks each route the planner picks as attempted until it
// picks an encode, as replans after failed copies do.
func failCopyRoutesV3(in *PlannerInputV3) {
	for range 4 {
		result := PlanPlaybackV3(*in)
		if result.Plan == nil || result.PlayMethod == PlayTranscode {
			return
		}
		in.AttemptedKeys = append(in.AttemptedKeys, PlanAttemptKeyV3(*result.Plan, in.Request.ClientPlaybackContext.Output.OutputContextID, nil))
	}
}
