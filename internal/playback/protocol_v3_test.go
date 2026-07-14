package playback

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func hasDegradationWarningV3(warnings []DegradationWarningV3, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestStartRequestV3Validation(t *testing.T) {
	index := 1
	req := validStartRequestV3()
	req.AudioTrackIndex = &index
	req.AudioTrackID = TrackIDV3(req.FileID, "audio", index)
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}

	req.AudioTrackID = TrackIDV3(req.FileID, "audio", 2)
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("mismatched track id/index accepted")
	}
}

func TestStartRequestV3UnknownQualityFallsBackToAuto(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "future-super-quality"
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if req.QualityPreference != "auto" || len(warnings) != 1 || warnings[0].Code != "quality_preference_normalized" {
		t.Fatalf("quality=%q warnings=%#v", req.QualityPreference, warnings)
	}
}

func TestReplanRequestV3OperationDefaultsAndValidates(t *testing.T) {
	start := validStartRequestV3()
	request := ReplanRequestV3{
		ProtocolVersion:       ProtocolV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "replan-operation-0001",
		FailedPlanID:          "plan:operation-0001",
		PlanAttemptID:         "plan-attempt-operation-0001",
		PlanAttemptKey:        "v3:0000000000000001",
		AttemptCount:          1,
		QualityPreference:     start.QualityPreference,
		OutputRouteGeneration: start.OutputRouteGeneration,
		Failure:               FailureV3{Classification: "parser_failure"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
	if request.EffectiveOperation() != ReplanOperationFailureRecoveryV3 {
		t.Fatalf("missing operation = %q", request.EffectiveOperation())
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("backward-compatible default operation: %v", err)
	}

	request.Operation = ReplanOperationSeekReanchorV3
	request.Failure.Classification = ""
	if err := request.Validate(); err != nil {
		t.Fatalf("seek reanchor operation without fake failure: %v", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["operation"] != string(ReplanOperationSeekReanchorV3) {
		t.Fatalf("serialized operation = %#v", wire["operation"])
	}

	request.Operation = ReplanOperationSeekFailureRecoveryV3
	if err := request.Validate(); err == nil {
		t.Fatal("seek failure recovery without a classification was accepted")
	}
	request.Failure.Classification = "decoder_failure"
	if err := request.Validate(); err != nil {
		t.Fatalf("seek failure recovery operation: %v", err)
	}

	request.Operation = "future_operation"
	if err := request.Validate(); err == nil {
		t.Fatal("unknown replan operation was accepted")
	}
}

func TestReplanRequestV3RejectsInvalidNetworkAndTrackEvidence(t *testing.T) {
	start := validStartRequestV3()
	request := ReplanRequestV3{
		ProtocolVersion:       ProtocolV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "replan-validation-0001",
		FailedPlanID:          "plan:validation-0001",
		PlanAttemptID:         "plan-attempt-validation-0001",
		PlanAttemptKey:        "v3:0000000000000001",
		AttemptCount:          1,
		QualityPreference:     start.QualityPreference,
		OutputRouteGeneration: start.OutputRouteGeneration,
		Failure:               FailureV3{Classification: "parser_failure"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}

	negative := -1
	request.SelectedTracks.Subtitle = &TrackIdentityV3{ID: "file:42:subtitle:-1", Index: &negative}
	if err := request.Validate(); err == nil {
		t.Fatal("negative subtitle index was accepted")
	}

	request.SelectedTracks.Subtitle = nil
	tooLow := 99
	request.BandwidthEstimateKbps = &tooLow
	if err := request.Validate(); err == nil {
		t.Fatal("out-of-range bandwidth estimate was accepted")
	}
}

func TestPlanAttemptKeyV3KotlinFixture(t *testing.T) {
	type fixture struct {
		Name                  string             `json:"name"`
		PlanID                string             `json:"plan_id"`
		Delivery              DeliveryV3         `json:"delivery"`
		StreamProtocol        StreamProtocolV3   `json:"stream_protocol"`
		Container             string             `json:"container"`
		VideoCodec            string             `json:"video_codec"`
		AudioCodec            string             `json:"audio_codec"`
		Width                 int                `json:"width"`
		Height                int                `json:"height"`
		BitrateKbps           int                `json:"bitrate_kbps"`
		DynamicRange          string             `json:"dynamic_range"`
		SubtitleMode          SubtitleModeV3     `json:"subtitle_mode"`
		Transformations       []TransformationV3 `json:"transformations"`
		AppliedQuirks         []AppliedQuirkV3   `json:"applied_quirks"`
		RuntimeCorrections    []string           `json:"runtime_corrections"`
		OutputRouteGeneration int64              `json:"output_route_generation"`
		LocalMutations        []string           `json:"local_mutations"`
		Expected              string             `json:"expected"`
	}
	body, err := os.ReadFile("testdata/protocol_v3/attempt_keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, value := range fixtures {
		t.Run(value.Name, func(t *testing.T) {
			plan := PlanV3{
				PlanID:          value.PlanID,
				Delivery:        value.Delivery,
				Stream:          StreamV3{Protocol: value.StreamProtocol, Container: value.Container},
				EffectiveRecipe: EffectiveRecipeV3{VideoCodec: value.VideoCodec, AudioCodec: value.AudioCodec, Width: &value.Width, Height: &value.Height, BitrateKbps: &value.BitrateKbps, DynamicRange: value.DynamicRange},
				Subtitle:        SubtitleDecisionV3{Mode: value.SubtitleMode},
			}
			plan.Transformations = append(plan.Transformations, value.Transformations...)
			// The quirk identity is conditionally omitted from the preimage
			// when no quirks or runtime corrections apply; fixtures pin both
			// arities so the Kotlin client reproduces the omission exactly.
			plan.AppliedQuirks = append(plan.AppliedQuirks, value.AppliedQuirks...)
			plan.RuntimeCorrections = append(plan.RuntimeCorrections, value.RuntimeCorrections...)
			if got := PlanAttemptKeyV3(plan, value.OutputRouteGeneration, value.LocalMutations); got != value.Expected {
				t.Fatalf("key = %q, want %q", got, value.Expected)
			}
		})
	}
}

func TestProtocolV3GoldenWireFixtures(t *testing.T) {
	startBody, err := os.ReadFile("testdata/protocol_v3/start_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var start StartRequestV3
	if err := json.Unmarshal(startBody, &start); err != nil {
		t.Fatal(err)
	}
	if _, err := start.NormalizeAndValidate(); err != nil {
		t.Fatalf("golden start request: %v", err)
	}
	replanBody, err := os.ReadFile("testdata/protocol_v3/replan_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var replan ReplanRequestV3
	if err := json.Unmarshal(replanBody, &replan); err != nil {
		t.Fatal(err)
	}
	if err := replan.Validate(); err != nil {
		t.Fatalf("golden replan request: %v", err)
	}
	if replan.BandwidthEstimateKbps == nil || *replan.BandwidthEstimateKbps != 3_500 ||
		replan.BandwidthCapKbps == nil || *replan.BandwidthCapKbps != 4_000 || !replan.Metered {
		t.Fatalf("golden replan network evidence = %#v", replan)
	}
	responseBody, err := os.ReadFile("testdata/protocol_v3/decision_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var response DecisionResponseV3
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != ProtocolV3 || response.Outcome != OutcomePlayableV3 || response.PlaybackPlan == nil || response.PlaybackPlan.Stream.Protocol != StreamHTTPProgressiveV3 {
		t.Fatalf("golden response = %#v", response)
	}
}

func TestPlanPlaybackV3DirectRequiresDetailedEvidence(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	req.ClientFeatures = []string{FeaturePlaybackPlanV3, FeatureMedia3Only}
	req.ClientPlaybackContext.Features = req.ClientFeatures
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true}})
	if result.Terminal == nil || result.Terminal.Reason != "transcoding_disabled" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestSourceDescriptorV3NormalizesLegacyHEVCMetadata(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].BitDepth = 0
	file.VideoTracks[0].PixelFormat = "yuv420p10le"
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVELPresent = false
	file.VideoTracks[0].DVEnhancementLayer = ""
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"

	source := SourceDescriptorFromFileV3(file, 0)
	if source.BitDepth != 10 {
		t.Fatalf("bit depth = %d, want inferred 10", source.BitDepth)
	}
	if source.DVEnhancementLayer != EnhancementUnknownV3 {
		t.Fatalf("enhancement layer = %q, want unknown", source.DVEnhancementLayer)
	}
}

func TestPlanPlaybackV3DirectPlaysLegacyHDR10WithInferredBitDepth(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].BitDepth = 0
	file.VideoTracks[0].PixelFormat = "yuv420p10le"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.HDR10 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3DirectPlaysLegacyDolbyVisionProfile8(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].BitDepth = 0
	file.VideoTracks[0].PixelFormat = "yuv420p10le"
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithHDR10"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.DolbyVision {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.RequestedMediaFileID != file.ID || result.Plan.EffectiveMediaFileID != file.ID {
		t.Fatalf("source ids = requested %d effective %d", result.Plan.RequestedMediaFileID, result.Plan.EffectiveMediaFileID)
	}
}

func TestPlanPlaybackV3RejectsTrulyIncompleteVideoMetadata(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].BitDepth = 0
	file.VideoTracks[0].PixelFormat = ""
	file.VideoTracks[0].Profile = ""
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "source_metadata_incomplete" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3BlocksUltrawide4KTranscode(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "2160p"
	file.VideoTracks[0].Width = 3840
	file.VideoTracks[0].Height = 1626
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "no_alternate_version" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3Profile7FallsBackToHDR10WithoutNativeP7(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 6
	file.VideoTracks[0].DVELPresent = false
	file.VideoTracks[0].DVEnhancementLayer = ""
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: registry})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.Plan.Claims.Video.HDR10 || result.Plan.Claims.Video.DolbyVision {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != "server_dv7_to_hdr10" {
		t.Fatalf("transformations = %#v", result.Plan.Transformations)
	}
}

func TestPlanPlaybackV3Profile7UsesVersionedClientTransformationsOnSameFile(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVELPresent = true
	file.VideoTracks[0].DVEnhancementLayer = "unknown"
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureClientVideoTransforms)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureClientVideoTransforms)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	direct := req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
	direct.Transformations = []TransformationV3{
		{Name: ClientDV7ToDV81V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3},
		{Name: ClientDV7ToHDR10V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3},
	}
	req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = direct

	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if first.Plan == nil || first.Plan.Delivery != DeliveryOriginalHTTPV3 || first.Plan.EffectiveMediaFileID != file.ID || first.Plan.DecisionReason != "client_dv7_to_dv81" {
		t.Fatalf("first = %#v", first)
	}
	if got := first.Plan.Transformations; len(got) != 1 || got[0].Name != ClientDV7ToDV81V3 || got[0].Executor != "client" || got[0].RecipeVersion != "1" {
		t.Fatalf("first transformations = %#v", got)
	}

	failedKey := PlanAttemptKeyV3(*first.Plan, req.OutputRouteGeneration, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(), AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryOriginalHTTPV3 || second.Plan.EffectiveMediaFileID != file.ID || second.Plan.DecisionReason != "client_dv7_to_hdr10" || !second.Plan.Claims.Video.HDR10 {
		t.Fatalf("second = %#v", second)
	}
}

func TestPlanPlaybackV3Profile7DoesNotInferClientTransformFromHDRProfile(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{7, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: false}})
	if result.Plan != nil && result.Plan.Delivery == DeliveryOriginalHTTPV3 {
		t.Fatalf("Profile 7 must not direct-play from codec claims alone: %#v", result.Plan)
	}
}

func TestPlanPlaybackV3AudioAdaptationCopiesVideo(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetVideoCodec != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3FallsBackFromProgressiveToHLSWithoutRepeatingKey(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("first = %s", ExplainPlannerResultV3(first))
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.OutputRouteGeneration, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryRemuxHLSV3 || second.TargetVideoCodec != "copy" {
		t.Fatalf("second = %#v", second)
	}
	secondKey := PlanAttemptKeyV3(*second.Plan, req.OutputRouteGeneration, nil)
	third := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(), AttemptedKeys: []string{failedKey, secondKey}})
	if third.Plan == nil || third.Plan.Delivery != DeliveryTranscodeHLSV3 || third.Plan.DecisionReason != "copy_routes_exhausted" {
		t.Fatalf("third = %#v", third)
	}
}

func TestPlanPlaybackV3NeverClaimsUnimplementedHDRTranscode(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3Profile7StripFallsBackToValidatedHLSCopy(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVELPresent = true
	file.VideoTracks[0].DVEnhancementLayer = "mel"
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{7}}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 || len(first.Plan.Transformations) != 1 {
		t.Fatalf("first = %#v", first)
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.OutputRouteGeneration, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry, AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryRemuxHLSV3 || second.TargetVideoCodec != "copy" || len(second.Plan.Transformations) != 1 || second.Plan.Transformations[0].Name != "server_dv7_to_hdr10" {
		t.Fatalf("second = %#v", second)
	}
}

func TestPlanPlaybackV3Profile8CompatibleBaseLayerStripsToHDR10(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVI"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.Plan.EffectiveRecipe.DynamicRange != "hdr10" || len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != "server_dv7_to_hdr10" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3PassthroughRequiresExactLayoutEntry(t *testing.T) {
	file := detailedFixtureFileV3()
	file.CodecAudio = "truehd"
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.Capabilities.AudioPassthrough = &AudioPassthroughV3{PassthroughCodecs: []string{"truehd"}, MaxChannels: 8, Entries: []AudioPassthroughEntryV3{{Codec: "truehd"}}}
	withoutLayout := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if withoutLayout.Plan == nil || !withoutLayout.TranscodeAudio {
		t.Fatalf("without exact layout = %#v", withoutLayout)
	}
	req.Capabilities.AudioPassthrough.Entries[0].ChannelCounts = []int{8}
	req.Capabilities.AudioPassthrough.Entries[0].Layouts = []string{"7.1"}
	withLayout := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if withLayout.Plan == nil || withLayout.Plan.Delivery != DeliveryOriginalHTTPV3 || !withLayout.Plan.Claims.Audio.Passthrough {
		t.Fatalf("with exact layout = %#v", withLayout)
	}
}

func TestPlanPlaybackV3DownloadedSubtitleUsesFrozenCombinedOrdinal(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	index := 0
	req.SubtitleTrackIndex = &index
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AdditionalSubtitles: []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: "downloaded"}}})
	if result.Plan == nil || result.Plan.Subtitle.Mode != SubtitleRenderV3 || result.Plan.SelectedTracks.Subtitle == nil || result.Plan.SelectedTracks.Subtitle.ID != req.SubtitleTrackID {
		t.Fatalf("result = %#v", result)
	}
}

func TestSubtitleBurnInUsesEmbeddedOrdinalAndRejectsUnsupportedSources(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = []models.ExternalSubtitle{{Format: "ass"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "hdmv_pgs_subtitle"}}
	req := validStartRequestV3()
	embeddedCombinedIndex := 1
	req.SubtitleTrackIndex = &embeddedCombinedIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", embeddedCombinedIndex)
	result := ResolveSubtitlePolicyV3(file, req, true, EngineMedia3DirectV3, nil)
	if !result.RequiresBurn || result.SelectedIndex != 1 || result.TransportIndex != 0 {
		t.Fatalf("embedded burn-in result = %#v", result)
	}

	externalIndex := 0
	req.SubtitleTrackIndex = &externalIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", externalIndex)
	req.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	result = ResolveSubtitlePolicyV3(file, req, true, EngineMedia3DirectV3, nil)
	if result.Terminal == nil || result.Terminal.Reason != "subtitle_burn_in_source_unsupported" {
		t.Fatalf("external burn-in result = %#v", result)
	}
}

func TestResolveQualityPolicyV3HonorsBandwidthCapInAllModes(t *testing.T) {
	source := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 20_000}
	cap := 5_000

	req := validStartRequestV3()
	req.QualityPreference = "original"
	req.BandwidthCapKbps = &cap
	result := ResolveQualityPolicyV3(req, source)
	if !result.RequiresTranscode || result.Height != 720 || result.Reason != "quality_bandwidth_cap" || result.ExplicitRung {
		t.Fatalf("original over cap = %#v", result)
	}
	if !hasDegradationWarningV3(result.Warnings, "bandwidth_cap_applied") {
		t.Fatalf("missing cap warning: %#v", result.Warnings)
	}

	lowBitrateSource := SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 4_000}
	result = ResolveQualityPolicyV3(req, lowBitrateSource)
	if !result.PreservesSource || result.RequiresTranscode || len(result.Warnings) != 0 {
		t.Fatalf("original under cap = %#v", result)
	}

	req.QualityPreference = "1080p"
	result = ResolveQualityPolicyV3(req, source)
	if result.Height != 720 || result.Reason != "quality_bandwidth_cap" || !result.ExplicitRung || !result.RequiresTranscode {
		t.Fatalf("fixed rung over cap = %#v", result)
	}
	if !hasDegradationWarningV3(result.Warnings, "bandwidth_cap_applied") {
		t.Fatalf("missing cap warning: %#v", result.Warnings)
	}

	req.QualityPreference = "480p"
	result = ResolveQualityPolicyV3(req, source)
	if result.Height != 480 || result.Reason != "quality_fixed_rung" || hasDegradationWarningV3(result.Warnings, "bandwidth_cap_applied") {
		t.Fatalf("fixed rung under cap = %#v", result)
	}

	req.QualityPreference = "auto"
	result = ResolveQualityPolicyV3(req, source)
	if !result.RequiresTranscode || result.Height != 720 || result.PreservesSource {
		t.Fatalf("auto with cap = %#v", result)
	}
}

func TestResolveQualityPolicyV3MeteredWithoutEvidencePrefersConservativeRung(t *testing.T) {
	source := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 20_000}
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	req.Metered = true
	result := ResolveQualityPolicyV3(req, source)
	if result.Height != 720 || !result.RequiresTranscode || result.Reason != "quality_metered_limit" || result.ExplicitRung {
		t.Fatalf("metered auto = %#v", result)
	}

	estimate := 30_000
	req.BandwidthEstimateKbps = &estimate
	result = ResolveQualityPolicyV3(req, source)
	if !result.PreservesSource || result.Reason != "quality_bandwidth_limit" {
		t.Fatalf("metered with estimate = %#v", result)
	}

	req.BandwidthEstimateKbps = nil
	req.Metered = false
	result = ResolveQualityPolicyV3(req, source)
	if !result.PreservesSource || result.RequiresTranscode {
		t.Fatalf("unmetered auto = %#v", result)
	}
}

func TestPlanPlaybackV3HDRDeviceCapFallsBackToOriginalQuality(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	req.Capabilities.MaxResolution = "1080p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.HDR10 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.EffectiveRecipe.Height == nil || *result.Plan.EffectiveRecipe.Height != 2160 {
		t.Fatalf("recipe = %#v", result.Plan.EffectiveRecipe)
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, "quality_reduction_unavailable") {
		t.Fatalf("warnings = %#v", result.Plan.DegradationWarnings)
	}
}

func TestPlanPlaybackV3HDRBandwidthCapFallsBackToOriginalQuality(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	cap := 5_000
	req.BandwidthCapKbps = &cap
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.HDR10 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, "quality_reduction_unavailable") {
		t.Fatalf("warnings = %#v", result.Plan.DegradationWarnings)
	}
}

func TestPlanPlaybackV3CapWithoutTranscodeRouteFallsBackToOriginal(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "1080p"
	file.VideoTracks[0].Width = 1920
	file.VideoTracks[0].Height = 1080
	file.VideoTracks[0].Bitrate = 8_000
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.QualityPreference = "original"
	cap := 4_000
	req.BandwidthCapKbps = &cap
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	// The client has no HLS engine, so the cap-induced transcode cannot run.
	delete(req.ClientPlaybackContext.Engines, string(EngineMedia3HLSV3))

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, "quality_reduction_unavailable") {
		t.Fatalf("warnings = %#v", result.Plan.DegradationWarnings)
	}
}

func TestPlanPlaybackV3LegacyHDRUnknownAssumesHDR10ForCapableClients(t *testing.T) {
	file := detailedFixtureFileV3()
	file.HDR = true
	file.VideoTracks[0].VideoRange = ""
	file.VideoTracks[0].VideoRangeType = ""
	file.VideoTracks[0].ColorTransfer = ""
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.HDR10 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, "hdr_range_assumed_hdr10") {
		t.Fatalf("warnings = %#v", result.Plan.DegradationWarnings)
	}

	// Without HDR10 output support the legacy row keeps the previous
	// (ineligible) behavior instead of guessing at the client's range.
	req.Capabilities.HDRDetails = nil
	req.ClientPlaybackContext.Output.HDRDetails = nil
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestSourceDescriptorV3NormalizesLegacyFileBitrateFallback(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].Bitrate = 0
	file.Bitrate = 60_000_000 // legacy rows stored bps

	source := SourceDescriptorFromFileV3(file, 0)
	if source.BitrateKbps != 60_000 {
		t.Fatalf("file-level bitrate = %d, want normalized 60000", source.BitrateKbps)
	}

	file.VideoTracks[0].Bitrate = 45_000_000
	source = SourceDescriptorFromFileV3(file, 0)
	if source.BitrateKbps != 45_000 {
		t.Fatalf("track-level bitrate = %d, want normalized 45000", source.BitrateKbps)
	}
}

func TestPlanAttemptedV3RequiresExactKeyMatch(t *testing.T) {
	plan := PlanV3{PlanID: "plan:exact", Delivery: DeliveryOriginalHTTPV3, Stream: StreamV3{Protocol: StreamHTTPProgressiveV3, Container: "mkv"}, Subtitle: SubtitleDecisionV3{Mode: SubtitleOffV3}}
	key := PlanAttemptKeyV3(plan, 1, nil)
	if !planAttemptedV3(plan, 1, []string{"  " + key + " "}) {
		t.Fatal("whitespace-trimmed exact key must match")
	}
	if planAttemptedV3(plan, 1, []string{strings.ToUpper(key)}) {
		t.Fatal("case-folded attempt key must not match an exact hash")
	}
}

func TestStartRequestV3ValidationBoundsInnerLists(t *testing.T) {
	longValue := strings.Repeat("x", 65)
	cases := []struct {
		name   string
		mutate func(*StartRequestV3)
	}{
		{"video_decode_profile_count", func(r *StartRequestV3) {
			r.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, Profiles: make([]string, 65)}}
		}},
		{"video_decode_profile_length", func(r *StartRequestV3) {
			r.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, Profiles: []string{longValue}}}
		}},
		{"video_decode_level_count", func(r *StartRequestV3) {
			r.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, Levels: make([]int, 65)}}
		}},
		{"video_decode_bit_depth_count", func(r *StartRequestV3) {
			r.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, BitDepths: make([]int, 65)}}
		}},
		{"capability_dolby_vision_profiles", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: make([]int, 17)}
		}},
		{"output_dolby_vision_profiles", func(r *StartRequestV3) {
			r.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: make([]int, 17)}
		}},
		{"engine_dolby_vision_profiles", func(r *StartRequestV3) {
			engine := r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
			engine.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: make([]int, 17)}
			r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = engine
		}},
		{"engine_container_length", func(r *StartRequestV3) {
			engine := r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
			engine.Containers = []string{longValue}
			r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = engine
		}},
		{"engine_validated_claim_length", func(r *StartRequestV3) {
			engine := r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
			engine.ValidatedClaims = []string{longValue}
			r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = engine
		}},
		{"engine_feature_length", func(r *StartRequestV3) {
			engine := r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
			engine.Features = []string{longValue}
			r.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = engine
		}},
		{"audio_track_id_length", func(r *StartRequestV3) {
			r.AudioTrackID = strings.Repeat("a", 129)
		}},
		{"subtitle_track_id_length", func(r *StartRequestV3) {
			r.SubtitleTrackID = strings.Repeat("s", 129)
		}},
	}
	for _, value := range cases {
		t.Run(value.name, func(t *testing.T) {
			req := validStartRequestV3()
			value.mutate(&req)
			if _, err := req.NormalizeAndValidate(); err == nil {
				t.Fatal("oversized capability accepted")
			}
		})
	}
}

func TestResolveQualityPolicyV3PreservesNonStandardSourceHeight(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	result := ResolveQualityPolicyV3(req, SourceDescriptorV3{Width: 2560, Height: 1440, BitrateKbps: 12_000})
	if !result.PreservesSource || result.RequiresTranscode || result.Width != 2560 || result.Height != 1440 || result.Label != "1440p" {
		t.Fatalf("quality result = %#v", result)
	}
}

func TestPlanPlaybackV3SeekedHLSCopyPreservesVideo(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "1080p"
	file.VideoTracks[0].Width = 1920
	file.VideoTracks[0].Height = 1080
	file.VideoTracks[0].BitDepth = 8
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	start := 20.0
	req.StartPosition = &start
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = EngineCapabilityV3{}
	req.ClientPlaybackContext.Engines[string(EngineMedia3ProgressiveRemuxV3)] = EngineCapabilityV3{}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.TargetVideoCodec != "copy" || result.Plan.Timeline.SourceStartSeconds != start {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3TimelineChangePreservesRouteIdentity(t *testing.T) {
	file := detailedFixtureFileV3()
	request := validStartRequestV3()
	request.ClientFeatures = append(request.ClientFeatures, FeatureDetailedDecodeV3)
	request.ClientPlaybackContext.Features = append(request.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	hdr := &HDRCapabilitiesV3{HDR10: true}
	request.Capabilities.HDR = true
	request.Capabilities.HDRDetails = hdr
	request.ClientPlaybackContext.Output.HDRDetails = hdr
	startAtZero := 0.0
	request.StartPosition = &startAtZero
	first := PlanPlaybackV3(PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	})
	if first.Plan == nil {
		t.Fatalf("initial plan = %#v", first)
	}

	startAtSeek := 321.25
	request.StartPosition = &startAtSeek
	second := PlanPlaybackV3(PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	})
	if second.Plan == nil {
		t.Fatalf("seeked plan = %#v", second)
	}
	if first.Plan.PlanID != second.Plan.PlanID ||
		PlanAttemptKeyV3(*first.Plan, request.OutputRouteGeneration, nil) != PlanAttemptKeyV3(*second.Plan, request.OutputRouteGeneration, nil) {
		t.Fatalf("timeline changed route identity: first=%#v second=%#v", first.Plan, second.Plan)
	}
	if first.Plan.Timeline.SourceStartSeconds != 0 || second.Plan.Timeline.SourceStartSeconds != startAtSeek {
		t.Fatalf("timeline positions: first=%#v second=%#v", first.Plan.Timeline, second.Plan.Timeline)
	}
}

func validStartRequestV3() StartRequestV3 {
	return StartRequestV3{
		ProtocolVersion:            ProtocolV3,
		ClientFeatures:             []string{FeaturePlaybackPlanV3, FeatureMedia3Only},
		FileID:                     42,
		ProfileID:                  "profile-1",
		PlaybackAttemptID:          "attempt-0001",
		QualityPreference:          "original",
		SubtitleFidelityPreference: SubtitleFidelityCompatibleV3,
		OutputRouteGeneration:      1,
		Capabilities:               ClientCodecCapabilitiesV3{CodecsVideo: []string{"hevc"}, CodecsAudio: []string{"aac"}, Containers: []string{"mkv"}, MaxResolution: "2160p"},
		ClientPlaybackContext: ClientPlaybackContextV3{ProtocolVersion: ProtocolV3, Features: []string{FeaturePlaybackPlanV3, FeatureMedia3Only}, Platform: "android", FormFactor: "tv", AppVersion: "test", Output: OutputContextV3{OutputRouteGeneration: 1}, Engines: map[string]EngineCapabilityV3{
			string(EngineMedia3DirectV3):           {Enabled: true, SupportedOnDevice: true, Subtitles: EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}},
			string(EngineMedia3ProgressiveRemuxV3): {Enabled: true, SupportedOnDevice: true},
			string(EngineMedia3HLSV3):              {Enabled: true, SupportedOnDevice: true},
		}},
	}
}

func detailedFixtureFileV3() *models.MediaFile {
	return &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac", Resolution: "2160p", Bitrate: 60_000, AudioChannels: 2, VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160, FrameRate: "24000/1001", Bitrate: 60_000, BitDepth: 10, VideoRange: "HDR", VideoRangeType: "HDR10"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

func testTransformationRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "audio_to_aac", Available: true},
		{Name: "video_to_h264", Available: true},
		{Name: "server_dv7_to_hdr10", Available: true},
	})
}
