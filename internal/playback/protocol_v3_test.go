package playback

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func hasDegradationWarningV3(warnings []DegradationWarningV3, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

// /api/v2-only features extend the shared list and never reach /api/v1.
func TestNativeServerFeaturesV3ExtendTheSharedList(t *testing.T) {
	shared, native := ServerFeaturesV3(), NativeServerFeaturesV3()
	for _, feature := range []string{FeatureSubripSidecarV3, FeatureHLSCopyMPEGTSV3, FeatureHLSVariableSubstitutionV3, FeaturePausedTimeoutV3} {
		if HasFeatureV3(shared, feature) || !HasFeatureV3(native, feature) {
			t.Fatalf("%s must be advertised on /api/v2 only: shared=%v native=%v", feature, shared, native)
		}
	}
	for _, feature := range shared {
		if !HasFeatureV3(native, feature) {
			t.Fatalf("native features omit shared feature %q", feature)
		}
	}
	independent := ServerFeaturesV3()
	shared[0] = "mutated"
	if independent[0] != FeaturePlaybackPlanV3 {
		t.Fatalf("feature slices share backing storage: %v", independent)
	}
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

func TestReplanRequestV3OperationDefaultsAndValidates(t *testing.T) {
	request := replanFixtureV3("operation-0001")
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
	if _, ok := wire["failure"]; ok {
		t.Fatalf("seek reanchor serialized a synthetic failure = %#v", wire["failure"])
	}

	request.Operation = ReplanOperationSeekFailureRecoveryV3
	if err := request.Validate(); err == nil {
		t.Fatal("seek failure recovery without a classification was accepted")
	}
	request.Failure.Classification = "decoder_failure"
	if err := request.Validate(); err != nil {
		t.Fatalf("seek failure recovery operation: %v", err)
	}

	request.Operation = ReplanOperationTrackChangeV3
	request.Failure = FailureV3{}
	if err := request.Validate(); err != nil {
		t.Fatalf("track change without a failure classification: %v", err)
	}
	for _, operation := range []ReplanOperationV3{
		ReplanOperationTrackChangeV3,
		ReplanOperationQualityChangeV3,
		ReplanOperationOutputChangeV3,
	} {
		request.Operation = operation
		request.QualityPreference = "720p"
		request.Failure = FailureV3{Classification: "decoder_failure"}
		if err := request.Validate(); err == nil {
			t.Fatalf("%s with failure was accepted", operation)
		}
	}
	request.Failure = FailureV3{}

	request.Operation = ReplanOperationQualityChangeV3
	request.QualityPreference = ""
	if err := request.Validate(); err == nil {
		t.Fatal("quality change without a quality_preference was accepted")
	}
	request.QualityPreference = "720p"
	if err := request.Validate(); err != nil {
		t.Fatalf("quality change without a failure classification: %v", err)
	}

	request.Operation = ReplanOperationOutputChangeV3
	request.QualityPreference = ""
	if err := request.Validate(); err != nil {
		t.Fatalf("output change without a failure classification: %v", err)
	}

	request.Operation = "future_operation"
	if err := request.Validate(); err == nil {
		t.Fatal("unknown replan operation was accepted")
	}
}

func TestReplanRequestV3RejectsInvalidNetworkAndTrackEvidence(t *testing.T) {
	request := replanFixtureV3("validation-0001")

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

func TestProtocolV3ConformanceMatrixCoversReleaseTrain(t *testing.T) {
	var matrix ConformanceMatrixV3
	body, err := os.ReadFile("testdata/protocol_v3/conformance_matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &matrix); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", matrix.SchemaVersion)
	}
	categories := make(map[string]bool)
	names := make(map[string]bool)
	plannerByName := make(map[string]PlannerScenarioV3)
	for _, value := range matrix.Planner {
		plannerByName[value.Name] = value
	}
	recordScenario := func(name, category string) {
		t.Helper()
		if name == "" || category == "" {
			t.Fatalf("unnamed scenario: %q/%q", name, category)
		}
		if names[name] {
			t.Fatalf("duplicate scenario name %q", name)
		}
		names[name] = true
		categories[category] = true
	}
	for _, value := range matrix.Planner {
		recordScenario(value.Name, value.Category)
	}
	for _, value := range matrix.Replans {
		recordScenario(value.Name, value.Category)
		if err := value.Request.Validate(); err != nil {
			t.Errorf("replan scenario %q is invalid: %v", value.Name, err)
		}
	}
	for _, value := range matrix.Protocol {
		recordScenario(value.Name, value.Category)
		if value.Input.StartRequest != nil {
			request := *value.Input.StartRequest
			if _, err := request.NormalizeAndValidate(); err != nil {
				t.Errorf("protocol scenario %q has invalid start request: %v", value.Name, err)
			}
		}
		if value.Input.ReplanRequest != nil {
			if err := value.Input.ReplanRequest.Validate(); err != nil {
				t.Errorf("protocol scenario %q has invalid replan request: %v", value.Name, err)
			}
		}
	}
	for _, required := range []string{
		"evidence_tier_gating", "deliveries_negotiation", "attempt_key_echo_and_loop",
		"track_change_replan", "quality_change_replan", "output_change_replan", "idempotent_replan",
		"concurrent_replan", "mid_seek_replan", "available_qualities",
		"audio_only_planning", "output_context_invalidation", "legacy_426",
		"hdr_dv_matrix", "audio_matrix", "subtitle_matrix", "recovery_matrix",
		"restart_matrix", "capacity_matrix", "route_event_limits", "profile_compatibility",
	} {
		if !categories[required] {
			t.Errorf("conformance matrix omits %q", required)
		}
	}
	for name, delivery := range map[string]DeliveryV3{
		"evidence_exact":                    DeliveryTranscodeHLSV3,
		"evidence_platform_attested":        DeliveryOriginalHTTPV3,
		"evidence_declared":                 DeliveryOriginalHTTPV3,
		"delivery_original":                 DeliveryOriginalHTTPV3,
		"delivery_progressive":              DeliveryRemuxProgressiveV3,
		"delivery_hls":                      DeliveryRemuxHLSV3,
		"delivery_transcode":                DeliveryTranscodeHLSV3,
		"audio_only_original":               DeliveryOriginalHTTPV3,
		"hdr10_exact_direct":                DeliveryOriginalHTTPV3,
		"client_managed_hdr_selected_audio": DeliveryOriginalHTTPV3,
		"dolby_vision_8_exact_direct":       DeliveryOriginalHTTPV3,
		"dolby_vision_7_hdr10_fallback":     DeliveryRemuxProgressiveV3,
		"truehd_audio_conversion":           DeliveryRemuxProgressiveV3,
		"truehd_exact_layout_passthrough":   DeliveryOriginalHTTPV3,
		"embedded_pgs_sidecar":              DeliveryOriginalHTTPV3,
		"embedded_ass_authored_render":      DeliveryOriginalHTTPV3,
		"embedded_dvd_burn_in":              DeliveryTranscodeHLSV3,
		"h264_constrained_baseline_direct":  DeliveryOriginalHTTPV3,
	} {
		value, ok := plannerByName[name]
		if !ok || value.Expected.Outcome != OutcomePlayableV3 || value.Expected.Delivery != delivery {
			t.Errorf("scenario %q = %#v, want playable %q", name, value.Expected, delivery)
		}
	}
	if value := plannerByName["available_qualities"]; len(value.Expected.AvailableQualities) < 2 || value.Expected.AvailableQualities[0].Label != QualityOriginalV3 {
		t.Errorf("available quality fixture = %#v", value.Expected.AvailableQualities)
	}
	constrainedBaseline := plannerByName["h264_constrained_baseline_direct"]
	if constrainedBaseline.Source.VideoProfile != "constrained baseline" ||
		len(constrainedBaseline.Request.Capabilities.VideoDecode) != 1 ||
		len(constrainedBaseline.Request.Capabilities.VideoDecode[0].Profiles) != 1 ||
		constrainedBaseline.Request.Capabilities.VideoDecode[0].Profiles[0] != h264BaselineProfileV3 ||
		constrainedBaseline.Expected.DecisionReason != "validated_original_playback" {
		t.Errorf("constrained baseline fixture = %#v", constrainedBaseline)
	}
	transformationNamed := func(value PlannerScenarioV3, name string) bool {
		t.Helper()
		for _, transformation := range value.Expected.Transformations {
			if transformation.Name == name {
				return true
			}
		}
		return false
	}
	if value := plannerByName["hdr10_exact_direct"]; value.Source.DynamicRange != DynamicRangeHDR10V3 || value.Source.BitDepth != 10 {
		t.Errorf("HDR10 scenario source = %#v", value.Source)
	}
	if value := plannerByName["client_managed_hdr_selected_audio"]; value.Expected.DecisionReason != decisionReasonClientManagedDynamicRangeV3 ||
		value.Expected.SelectedTracks == nil || value.Expected.SelectedTracks.Audio == nil || value.Expected.SelectedTracks.Audio.Index == nil ||
		*value.Expected.SelectedTracks.Audio.Index != 1 {
		t.Errorf("client-managed HDR selected-audio scenario = %#v", value)
	}
	if value := plannerByName["dolby_vision_8_exact_direct"]; value.Source.DynamicRange != DynamicRangeDolbyVisionV3 || value.Source.DVProfile != 8 || len(value.Expected.Transformations) != 0 {
		t.Errorf("Dolby Vision 8 scenario = %#v", value)
	}
	if value := plannerByName["dolby_vision_7_hdr10_fallback"]; value.Source.DVProfile != 7 || !transformationNamed(value, TransformationServerDV7HDR10V3) {
		t.Errorf("Dolby Vision 7 fallback scenario = %#v", value)
	}
	if value := plannerByName["truehd_audio_conversion"]; value.Source.AudioCodec != "truehd" || !transformationNamed(value, TransformationAudioToAACV3) {
		t.Errorf("TrueHD conversion scenario = %#v", value)
	}
	if value := plannerByName["truehd_exact_layout_passthrough"]; value.Source.AudioCodec != "truehd" || value.Expected.Claims == nil || !value.Expected.Claims.Audio.Passthrough {
		t.Errorf("TrueHD passthrough scenario = %#v", value)
	}
	for name, mode := range map[string]SubtitleModeV3{
		"embedded_pgs_sidecar":         SubtitleRenderV3,
		"embedded_ass_authored_render": SubtitleRenderV3,
		"embedded_dvd_burn_in":         SubtitleBurnInV3,
	} {
		value := plannerByName[name]
		if value.Request.SubtitleTrackID == "" || value.Expected.SelectedTracks == nil || value.Expected.SelectedTracks.Subtitle == nil ||
			value.Expected.SelectedTracks.Subtitle.ID != value.Request.SubtitleTrackID || value.Expected.Subtitle == nil ||
			value.Expected.Subtitle.Mode != mode || value.Expected.Subtitle.TrackID != value.Request.SubtitleTrackID {
			t.Errorf("subtitle scenario %q = %#v", name, value)
		}
	}

	replansByName := make(map[string]ReplanScenarioV3, len(matrix.Replans))
	for _, value := range matrix.Replans {
		replansByName[value.Name] = value
	}
	for _, operation := range []struct {
		prefix string
		value  ReplanOperationV3
	}{
		{prefix: "track_change", value: ReplanOperationTrackChangeV3},
		{prefix: "quality_change", value: ReplanOperationQualityChangeV3},
	} {
		base := replansByName[operation.prefix]
		if base.Request.EffectiveOperation() != operation.value || base.Expected.HTTPStatus != 200 {
			t.Errorf("%s base scenario = %#v", operation.prefix, base)
		}
		duplicate := replansByName[operation.prefix+"_idempotent_duplicate"]
		if duplicate.Request.EffectiveOperation() != operation.value || duplicate.Expected.SameRequestAndBodyStatus != 200 ||
			!duplicate.Expected.ResponseReplayedVerbatim || duplicate.Expected.ChangedBodyStatus != 409 || duplicate.Expected.ChangedBodyError != "idempotency_key_reused" {
			t.Errorf("%s duplicate scenario = %#v", operation.prefix, duplicate)
		}
		concurrent := replansByName[operation.prefix+"_concurrent_duplicate"]
		if concurrent.Request.EffectiveOperation() != operation.value || concurrent.Expected.WhileFirstLeaseActiveStatus != 409 ||
			concurrent.Expected.ConcurrentError != "replan_in_progress" || concurrent.Expected.AfterCompletionStatus != 200 || !concurrent.Expected.ResponseReplayedVerbatim {
			t.Errorf("%s concurrent scenario = %#v", operation.prefix, concurrent)
		}
		midSeek := replansByName[operation.prefix+"_mid_seek"]
		if midSeek.Request.EffectiveOperation() != operation.value || midSeek.Request.PositionSeconds != 321.25 ||
			midSeek.Expected.PositionSeconds != midSeek.Request.PositionSeconds || !midSeek.Expected.PositionPreserved {
			t.Errorf("%s mid-seek scenario = %#v", operation.prefix, midSeek)
		}
	}

	protocolByName := make(map[string]ProtocolScenarioV3, len(matrix.Protocol))
	for _, value := range matrix.Protocol {
		protocolByName[value.Name] = value
	}
	legacy := protocolByName["legacy_start_requires_upgrade"]
	if legacy.Input.LegacyStartBody == nil || legacy.Input.LegacyStartBody.FileID <= 0 || legacy.Expected.HTTPStatus != 426 || legacy.Expected.Error != "client_upgrade_required" {
		t.Errorf("legacy protocol scenario = %#v", legacy)
	}
	output := protocolByName["output_context_change_invalidates_attempt"]
	if output.Input.FirstOutputContextID == output.Input.SecondOutputContextID || !output.Expected.PlanIDUnchanged || !output.Expected.PlanAttemptKeyChanged {
		t.Errorf("output-context scenario = %#v", output)
	}
	loop := protocolByName["opaque_attempt_key_loop"]
	if loop.Input.ServerPlanAttemptKey == "" || loop.Input.ReplanEcho != loop.Input.ServerPlanAttemptKey ||
		len(loop.Input.AttemptedPlanKeys) != 1 || loop.Input.AttemptedPlanKeys[0] != loop.Input.ServerPlanAttemptKey || loop.Expected.Action != "reject_already_attempted_plan" {
		t.Errorf("opaque loop scenario = %#v", loop)
	}
	recovery := protocolByName["failure_recovery_preserves_intent"]
	if recovery.Input.ReplanRequest == nil || recovery.Input.ReplanRequest.SelectedTracks.Subtitle == nil ||
		!recovery.Expected.SelectionPreserved || !recovery.Expected.PositionPreserved || recovery.Expected.HTTPStatus != 200 {
		t.Errorf("failure-recovery scenario = %#v", recovery)
	}
	restart := protocolByName["restart_replays_terminal_attempt"]
	if restart.Input.StartRequest == nil || restart.Input.PersistedDecision == nil || !restart.Input.Restarted ||
		restart.Expected.HTTPStatus != 201 || restart.Expected.Outcome != OutcomeAdaptationUnavailableV3 ||
		restart.Expected.TerminalReason != "transcode_start_failed" || !restart.Expected.ResponseReplayedVerbatim ||
		restart.Expected.CapacityDelta == nil || *restart.Expected.CapacityDelta != 0 {
		t.Errorf("restart replay scenario = %#v", restart)
	}
	capacity := protocolByName["capacity_unavailable_cleans_up"]
	if capacity.Input.StartRequest == nil || capacity.Input.CapacityAvailable == nil || *capacity.Input.CapacityAvailable ||
		capacity.Expected.HTTPStatus != 201 || capacity.Expected.TerminalReason != "capacity_unavailable" ||
		capacity.Expected.CapacityDelta == nil || *capacity.Expected.CapacityDelta != 0 || !capacity.Expected.CleanupComplete {
		t.Errorf("capacity scenario = %#v", capacity)
	}
	limit := protocolByName["route_event_diagnostic_limit"]
	if limit.Input.RouteEvent == nil || len(limit.Input.RouteEvent.Diagnostics) != 33 || limit.Expected.HTTPStatus != 400 ||
		limit.Expected.Error != "bad_request" || limit.Expected.Action != "reject_without_persisting" {
		t.Errorf("route-event limit scenario = %#v", limit)
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
	if !strings.HasPrefix(response.PlaybackPlan.PlanAttemptKey, "v3:") {
		t.Fatalf("golden plan attempt key = %q, want an opaque server-owned v3: token", response.PlaybackPlan.PlanAttemptKey)
	}
	if replan.FailedPlanID != response.PlaybackPlan.PlanID || replan.PlanAttemptKey != response.PlaybackPlan.PlanAttemptKey ||
		len(replan.AttemptedPlanKeys) != 1 || replan.AttemptedPlanKeys[0] != response.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("golden replan does not echo the golden decision identity: replan=%#v plan=%#v", replan, response.PlaybackPlan)
	}
}

// The same SDR source must reach a tier-appropriate route for each evidence
// tier: exact and platform_attested validate against decode entries (the
// latter skips profile/level only for hardware attestations), while declared
// grants the copy route on the flat codec+container match alone.
func TestPlanPlaybackV3EvidenceTiersReachTierAppropriateRoutes(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].BitDepth = 8
	file.VideoTracks[0].Profile = "Main"

	base := validStartRequestV3()
	base.Capabilities.Containers = []string{"mkv"}

	// exact: a decode entry with a non-matching profile blocks the direct route.
	exact := base
	exact.Capabilities.VideoEvidence = EvidenceExactV3
	exact.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	result := PlanPlaybackV3(PlannerInputV3{Request: exact, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery == DeliveryOriginalHTTPV3 {
		t.Fatalf("exact tier with mismatched profile = %s", ExplainPlannerResultV3(result))
	}

	// platform_attested: the identical capability set skips profile/level
	// matching and earns the direct route.
	attested := exact
	attested.Capabilities.VideoEvidence = EvidencePlatformAttestedV3
	result = PlanPlaybackV3(PlannerInputV3{Request: attested, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("platform_attested tier = %s", ExplainPlannerResultV3(result))
	}

	// declared: no decode entries at all; the flat codec list carries the day.
	declared := base
	declared.Capabilities.VideoEvidence = EvidenceDeclaredV3
	result = PlanPlaybackV3(PlannerInputV3{Request: declared, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("declared tier = %s", ExplainPlannerResultV3(result))
	}
}

// A tier that cannot validate a stream the flat lists claim must say so:
// the adapted plan carries evidence_insufficient_for_direct rather than
// looking like a device refusal.
func TestPlanPlaybackV3ExactTierWithoutDecodeEntryReportsEvidenceInsufficiency(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].BitDepth = 8
	req := validStartRequestV3()
	req.Capabilities.Containers = []string{"mkv"}
	req.Capabilities.VideoDecode = nil
	result := planFileV3(req, file)
	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, EvidenceInsufficientForDirectV3) {
		t.Fatalf("warnings = %#v", result.Plan.DegradationWarnings)
	}
	if result.Plan.DecisionReason != EvidenceInsufficientForDirectV3 {
		t.Fatalf("decision reason = %q", result.Plan.DecisionReason)
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

func TestSourceDescriptorV3PreservesCanonicalColorRange(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "limited", input: "tv", want: "tv"},
		{name: "full", input: "pc", want: "pc"},
		{name: "unspecified", input: "unknown", want: "unknown"},
		{name: "normalizes case and whitespace", input: " PC ", want: "pc"},
		{name: "rejects non-ffmpeg value", input: "limited", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := detailedFixtureFileV3()
			file.VideoTracks[0].ColorRange = test.input
			if got := SourceDescriptorFromFileV3(file, 0).ColorRange; got != test.want {
				t.Fatalf("color range = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPlanPlaybackV3AndroidMedia3HLSRecipesAcrossSourceQualities(t *testing.T) {
	profiles := []struct {
		name           string
		profile        int
		compatibility  int
		videoRangeType string
		hdr            *HDRCapabilitiesV3
		wantEntry      string
	}{
		{name: "Profile 7 strip", profile: 7, compatibility: 6, videoRangeType: "DOVIWithEL", hdr: &HDRCapabilitiesV3{HDR10: true}, wantEntry: VideoSampleEntryHVC1},
		{name: "Profile 8 preserve", profile: 8, compatibility: 1, videoRangeType: "DOVIWithHDR10", hdr: &HDRCapabilitiesV3{DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1}}}}, wantEntry: VideoSampleEntryDVH1},
	}
	for _, profile := range profiles {
		for _, quality := range []string{"auto", "2160p"} {
			t.Run(profile.name+"/"+quality, func(t *testing.T) {
				file := detailedFixtureFileV3()
				file.CodecAudio = "truehd"
				file.AudioChannels = 8
				file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1", Default: true}
				file.VideoTracks[0].DVProfile = profile.profile
				file.VideoTracks[0].DVBLCompatID = profile.compatibility
				file.VideoTracks[0].DVLevel = 6
				file.VideoTracks[0].ColorTransfer = "smpte2084"
				file.VideoTracks[0].VideoRange = "DolbyVision"
				file.VideoTracks[0].VideoRangeType = profile.videoRangeType

				req := validStartRequestV3()
				req.QualityPreference = quality
				req.ClientPlaybackContext.Device.Platform = "android"
				req.ClientPlaybackContext.AppBuild = legacyAndroidMedia3HLSBuildV3
				req.ClientFeatures = append(req.ClientFeatures, FeatureDeviceQuirksV3)
				req.Capabilities.VideoDecode = hevcMain10DecodeV3()
				req.Capabilities.HDRDetails = profile.hdr
				req.ClientPlaybackContext.Output.HDRDetails = profile.hdr
				delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
				delete(req.ClientPlaybackContext.Deliveries, DeliveryClassProgressiveV3)
				hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
				hls.VideoCodecs = []string{"hevc"}
				hls.AudioDecodeCodecs = []string{"aac"}
				hls.HDRDetails = profile.hdr
				req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls

				result := planFileV3(req, file)
				if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.TargetVideoCodec != "copy" || !result.TranscodeAudio {
					t.Fatalf("result = %s", ExplainPlannerResultV3(result))
				}
				if got := result.Plan.EffectiveRecipe.VideoSampleEntry; got != profile.wantEntry {
					t.Fatalf("sample entry = %q, want %q", got, profile.wantEntry)
				}
			})
		}
	}
}

// A file the scanner stored without any stream metadata is either waiting for
// its first probe or was rejected by ffprobe. Only the first is worth retrying.
func TestPlanPlaybackV3SeparatesUnprobedFromUnreadableSources(t *testing.T) {
	req := validStartRequestV3()
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}
	bare := func() *models.MediaFile {
		return &models.MediaFile{ID: 7, FilePath: "/library/show/S01E03.mkv", FileSize: 0, SubtitleTracks: []models.SubtitleTrack{}}
	}

	unprobed := bare()
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: unprobed, EffectiveFile: unprobed, AudioTrackIndex: 0, Settings: settings, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "source_metadata_incomplete" || !result.Terminal.Retryable {
		t.Fatalf("unprobed result = %s", ExplainPlannerResultV3(result))
	}

	failedAt := time.Date(2026, time.October, 2, 12, 1, 3, 0, time.UTC)
	rejected := bare()
	rejected.ProbeFailedAt = &failedAt
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: rejected, EffectiveFile: rejected, AudioTrackIndex: 0, Settings: settings, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != TerminalSourceUnreadableV3 || result.Terminal.Retryable {
		t.Fatalf("rejected result = %s", ExplainPlannerResultV3(result))
	}
	if result.Terminal.Message != TerminalSourceUnreadableMessageV3 {
		t.Fatalf("message = %q", result.Terminal.Message)
	}

	// The server bitrate cap renames refusals it causes; it must not claim
	// an unreadable file.
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: rejected, EffectiveFile: rejected, AudioTrackIndex: 0, Settings: settings, Registry: testTransformationRegistryV3(), ServerBitrateCapKbps: 4000})
	if result.Terminal == nil || result.Terminal.Reason != TerminalSourceUnreadableV3 {
		t.Fatalf("capped rejected result = %s", ExplainPlannerResultV3(result))
	}

	// The effective file decides: a rejected requested file whose effective
	// version is readable plans normally.
	readable := detailedFixtureFileV3()
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: rejected, EffectiveFile: readable, AudioTrackIndex: 0, Settings: settings, Registry: testTransformationRegistryV3()})
	if result.Terminal != nil && (result.Terminal.Reason == TerminalSourceUnreadableV3 || result.Terminal.Reason == "source_metadata_incomplete") {
		t.Fatalf("readable effective file result = %s", ExplainPlannerResultV3(result))
	}

	// A stale failure mark never hides metadata from a successful probe.
	probed := detailedFixtureFileV3()
	probed.ProbeFailedAt = &failedAt
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: probed, EffectiveFile: probed, AudioTrackIndex: 0, Settings: settings, Registry: testTransformationRegistryV3()})
	if result.Terminal != nil && (result.Terminal.Reason == TerminalSourceUnreadableV3 || result.Terminal.Reason == "source_metadata_incomplete") {
		t.Fatalf("probed file with failure mark result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3Profile7UsesVersionedClientTransformationsOnSameFile(t *testing.T) {
	file := dv7WithELFixtureV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureClientVideoTransforms)
	req.Capabilities.VideoDecode = hevcMain10DecodeV3()
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Transformations = []TransformationV3{
		{Name: ClientDV7ToDV81V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3},
		{Name: ClientDV7ToHDR10V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3},
	}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct

	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if first.Plan == nil || first.Plan.Delivery != DeliveryOriginalHTTPV3 || first.Plan.EffectiveMediaFileID != file.ID || first.Plan.DecisionReason != "client_dv7_to_dv81" {
		t.Fatalf("first = %#v", first)
	}
	if got := first.Plan.Transformations; len(got) != 1 || got[0].Name != ClientDV7ToDV81V3 || got[0].Executor != "client" || got[0].RecipeVersion != "1" {
		t.Fatalf("first transformations = %#v", got)
	}

	failedKey := PlanAttemptKeyV3(*first.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(), AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryOriginalHTTPV3 || second.Plan.EffectiveMediaFileID != file.ID || second.Plan.DecisionReason != "client_dv7_to_hdr10" || !second.Plan.Claims.Video.HDR10 {
		t.Fatalf("second = %#v", second)
	}
}

func TestPlanPlaybackV3FallsBackFromProgressiveToHLSWithoutRepeatingKey(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := hdr10ClientRequestV3(false)
	req.Capabilities.Containers = []string{"mp4"}
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("first = %s", ExplainPlannerResultV3(first))
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryRemuxHLSV3 || second.TargetVideoCodec != "copy" {
		t.Fatalf("second = %#v", second)
	}
	secondKey := PlanAttemptKeyV3(*second.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil)
	third := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(), AttemptedKeys: []string{failedKey, secondKey}})
	if third.Plan == nil || third.Plan.Delivery != DeliveryTranscodeHLSV3 || third.Plan.DecisionReason != "copy_routes_exhausted" {
		t.Fatalf("third = %#v", third)
	}
}

// A copy-unsafe source streams like any other: decoders that apply in-band
// parameter sets play its copy, and strict ones fail it and recover to the
// encode. Strict browsers get the encode on the first plan.
func TestPlanPlaybackV3CopyUnsafeSourceStillCopies(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := hdr10ClientRequestV3(false)
	req.Capabilities.Containers = []string{"mp4"}
	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	multi := true
	scanSize := file.FileSize
	file.FileModifiedAt = &mtime
	file.MultiplePPS = &multi
	file.MultiplePPSScanSize = &scanSize
	file.MultiplePPSScanMtime = &mtime

	result := planFileV3(req, file)
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.Plan.Source.VideoCopyUnsafe {
		t.Fatalf("result = %s, want the progressive copy of a source marked copy-unsafe", ExplainPlannerResultV3(result))
	}

	// A strict decoder fails every copy; its recovery walk reaches the encode.
	plans, _ := replanChainV3(t, PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	})
	var deliveries []DeliveryV3
	for _, plan := range plans {
		deliveries = append(deliveries, plan.Delivery)
	}
	t.Logf("recovery walk: %v", deliveries)
	firstEncode := slices.Index(deliveries, DeliveryTranscodeHLSV3)
	if firstEncode < 0 || !slices.Contains(deliveries[:firstEncode], DeliveryRemuxHLSV3) {
		t.Fatalf("recovery walk = %v, want both copy deliveries before the encode", deliveries)
	}

	// A browser whose decoder ignores in-band parameter sets gets the encode
	// up front, since every track or quality replan would fail the copies
	// again. Firefox decodes these sources and keeps the copy.
	for name, test := range map[string]struct {
		userAgent string
		want      DeliveryV3
	}{
		"safari":  {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15", DeliveryTranscodeHLSV3},
		"chrome":  {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36", DeliveryTranscodeHLSV3},
		"firefox": {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0", DeliveryRemuxProgressiveV3},
	} {
		web := req
		web.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{"user_agent": test.userAgent}}
		if result := planFileV3(web, file); result.Plan == nil || result.Plan.Delivery != test.want {
			t.Errorf("%s: result = %s, want %s", name, ExplainPlannerResultV3(result), test.want)
		}
		// An MP4 the browser could play as is fails there the same way.
		mp4 := *file
		mp4.Container = "mp4"
		want := test.want
		if want == DeliveryRemuxProgressiveV3 {
			want = DeliveryOriginalHTTPV3
		}
		if result := planFileV3(web, &mp4); result.Plan == nil || result.Plan.Delivery != want {
			t.Errorf("%s, mp4: result = %s, want %s", name, ExplainPlannerResultV3(result), want)
		}
	}
}

// TestPlanPlaybackV3ToneMapSettingsSelectValidatedExecutor verifies planning honors validated executor policy.
func TestPlanPlaybackV3ToneMapSettingsSelectValidatedExecutor(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].ColorPrimaries = "bt2020"
	file.VideoTracks[0].ColorTransfer = "smpte2084"
	file.VideoTracks[0].ColorSpace = "bt2020nc"
	req := validStartRequestV3()
	req.QualityPreference = QualityRung2160pMediumV3
	registry := toneMapTestRegistryV3()
	capabilities := tonemap.Capabilities{
		{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemapx", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ, tonemap.SourceHLG}},
		{Mode: tonemap.ModeHardware, Backend: "qsv", Filter: "tonemap_opencl", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
	}
	tests := []struct {
		name     string
		hardware bool
		software bool
		wantMode tonemap.Mode
	}{
		{name: "disabled"},
		{name: "hardware only", hardware: true, wantMode: tonemap.ModeHardware},
		{name: "software only", software: true, wantMode: tonemap.ModeSoftware},
		{name: "hardware preferred", hardware: true, software: true, wantMode: tonemap.ModeHardware},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PlanPlaybackV3(PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, HardwareToneMapEnabled: tt.hardware, SoftwareToneMapEnabled: tt.software},
				Registry: registry, ToneMapCapabilities: capabilities,
			})
			if tt.wantMode == "" {
				if result.Terminal == nil || result.Terminal.Reason != TerminalHDRTranscodeUnsupportedV3 {
					t.Fatalf("result = %s", ExplainPlannerResultV3(result))
				}
				return
			}
			if result.Plan == nil || result.ToneMapMode != tt.wantMode || result.ToneMapSourceKind != tonemap.SourcePQ {
				t.Fatalf("result = %#v", result)
			}
			if result.TargetResolution != "2160p" || result.TargetBitrateKbps != 20_000 || result.Plan.EffectiveRecipe.Height == nil || *result.Plan.EffectiveRecipe.Height != 2160 {
				t.Fatalf("4K Medium target = resolution %q bitrate %d recipe %#v", result.TargetResolution, result.TargetBitrateKbps, result.Plan.EffectiveRecipe)
			}
			if result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeSDRV3 || !hasDegradationWarningV3(result.Plan.DegradationWarnings, DegradationWarningHDRToneMappedV3) {
				t.Fatalf("plan = %#v", result.Plan)
			}
			found := false
			for _, transformation := range result.Plan.Transformations {
				if transformation.Name == TransformationHDRToSDRToneMapV3 && transformation.RecipeVersion == TransformationHDRToSDRToneMapRecipeVersionV3 {
					found = true
				}
			}
			if !found {
				t.Fatalf("transformations = %#v", result.Plan.Transformations)
			}
		})
	}
}

func TestPlanPlaybackV3ResolvesToneMapRecipeOnce(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].ColorPrimaries = "bt2020"
	file.VideoTracks[0].ColorTransfer = "smpte2084"
	file.VideoTracks[0].ColorSpace = "bt2020nc"
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	registry := toneMapTestRegistryV3()
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	registryCalls := 0
	capabilityCalls := 0

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true},
		Registry: registry,
		HLSRegistry: func() *TransformationRegistryV3 {
			registryCalls++
			return registry
		},
		HLSToneMapCapabilities: func() tonemap.Capabilities {
			capabilityCalls++
			return capabilities
		},
	})

	if result.Plan == nil || result.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("result = %s, want software tone-map transcode", ExplainPlannerResultV3(result))
	}
	if registryCalls != 1 || capabilityCalls != 1 {
		t.Fatalf("tone-map resolution calls = registry %d capabilities %d, want one each", registryCalls, capabilityCalls)
	}
}

func TestPlanPlaybackV3ReportsHDRLimitationBefore4KPolicy(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "2160p"
	file.VideoTracks[0].Width = 3840
	file.VideoTracks[0].Height = 2160
	req := validStartRequestV3()
	req.Capabilities.HDRDetails = nil
	req.ClientPlaybackContext.Output.HDRDetails = nil

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false},
	})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestSourceDescriptorV3OmitsInvalidDolbyVisionLevel(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVLevel = 14
	if got := SourceDescriptorFromFileV3(file, 0).DVLevel; got != 0 {
		t.Fatalf("DVLevel = %d, want omitted", got)
	}
}

func TestPlanPlaybackV3AdaptedRoutesPreservePGSCombinedInventory(t *testing.T) {
	file := detailedFixtureFileV3()
	for range 8 {
		file.ExternalSubtitles = append(file.ExternalSubtitles, models.ExternalSubtitle{Format: "srt"})
	}
	// The scanner index is the absolute probed stream index. The client-facing
	// combined ordinal is 8, while ffmpeg addresses this first embedded
	// subtitle as 0:s:0 (transport index 0).
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Codec: "hdmv_pgs_subtitle"}}

	req := hdr10ClientRequestV3(true)
	for _, deliveryClass := range []string{DeliveryClassOriginalHTTPV3, DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
		delivery := req.ClientPlaybackContext.Deliveries[deliveryClass]
		delivery.Subtitles.EmbeddedBitmap = true
		req.ClientPlaybackContext.Deliveries[deliveryClass] = delivery
	}
	selectedIndex := 8
	req.SubtitleTrackIndex = &selectedIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", selectedIndex)

	input := PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	}
	direct := PlanPlaybackV3(input)
	if direct.Plan == nil || direct.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("direct plan = %#v", direct)
	}
	input.AttemptedKeys = []string{PlanAttemptKeyV3(*direct.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil)}
	progressive := PlanPlaybackV3(input)
	if progressive.Plan == nil || progressive.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("progressive fallback = %#v", progressive)
	}
	input.AttemptedKeys = append(input.AttemptedKeys, PlanAttemptKeyV3(*progressive.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil))
	hls := PlanPlaybackV3(input)
	if hls.Plan == nil || hls.Plan.Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("HLS fallback = %#v", hls)
	}
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	input.Request.QualityPreference = "1080p"
	input.AttemptedKeys = nil
	transcode := PlanPlaybackV3(input)
	if transcode.Plan == nil || transcode.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("transcode route = %#v", transcode)
	}

	for name, result := range map[string]PlannerResultV3{"progressive": progressive, "HLS": hls, "transcode": transcode} {
		if result.SubtitleTrackIndex != selectedIndex || result.SubtitleTransportTrackIndex != 0 {
			t.Errorf("%s subtitle selection = combined %d / transport %d, want 8 / 0", name, result.SubtitleTrackIndex, result.SubtitleTransportTrackIndex)
		}
		inventory := result.Plan.Subtitle.Inventory
		if len(inventory) != 9 {
			t.Errorf("%s inventory = %+v, want all 9 combined ordinals", name, inventory)
			continue
		}
		selected := inventory[selectedIndex]
		if selected.CombinedIndex != selectedIndex || selected.TrackID != req.SubtitleTrackID || selected.Source != SubtitleSourceEmbeddedV3 || selected.Codec != "hdmv_pgs_subtitle" || selected.Delivery != SubtitleDeliverySidecarV3 {
			t.Errorf("%s selected inventory entry = %+v", name, selected)
		}
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
	result := ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil, 0, nil)
	if !result.RequiresBurn || result.SelectedIndex != 1 || result.TransportIndex != 0 {
		t.Fatalf("embedded burn-in result = %#v", result)
	}

	externalIndex := 0
	req.SubtitleTrackIndex = &externalIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", externalIndex)
	req.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	result = ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil, 0, nil)
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
	// The cap reaches 1080p Low, so the resolution stays and only the
	// bitrate drops to the cap.
	if result.Height != 1080 || result.BitrateKbps != cap || result.Reason != "quality_bandwidth_cap" || !result.ExplicitRung || !result.RequiresTranscode {
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
		{"delivery_dolby_vision_profiles", func(r *StartRequestV3) {
			delivery := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			delivery.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: make([]int, 17)}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery
		}},
		{"capability_dolby_vision_profile_levels", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: make([]DolbyVisionProfileCapabilityV3, 17)}
		}},
		{"invalid_dolby_vision_profile_level", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 14}}}
		}},
		{"negative_dolby_vision_profile_level_profile", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: -1, MaxLevel: 6}}}
		}},
		{"duplicate_dolby_vision_profile_level", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6}, {Profile: 8, MaxLevel: 7}}}
		}},
		{"dolby_vision_bl_compatibility_count", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: make([]int, 17)}}}
		}},
		{"invalid_dolby_vision_bl_compatibility_id", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{16}}}}
		}},
		{"duplicate_dolby_vision_bl_compatibility_id", func(r *StartRequestV3) {
			r.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1, 1}}}}
		}},
		{"delivery_container_length", func(r *StartRequestV3) {
			delivery := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			delivery.Containers = []string{longValue}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery
		}},
		{"delivery_validated_claim_length", func(r *StartRequestV3) {
			delivery := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			delivery.ValidatedClaims = []string{longValue}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery
		}},
		{"delivery_feature_length", func(r *StartRequestV3) {
			delivery := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			delivery.Features = []string{longValue}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery
		}},
		{"audio_track_id_length", func(r *StartRequestV3) {
			r.AudioTrackID = strings.Repeat("a", 129)
		}},
		{"subtitle_track_id_length", func(r *StartRequestV3) {
			r.SubtitleTrackID = strings.Repeat("s", 129)
		}},
		{"native_subtitle_limitation_count", func(r *StartRequestV3) {
			setNativeSubtitleLimitationsV3(r, make([]string, 17))
		}},
		{"native_subtitle_limitation_length", func(r *StartRequestV3) {
			setNativeSubtitleLimitationsV3(r, []string{longValue})
		}},
		{"native_subtitle_limitation_empty", func(r *StartRequestV3) {
			setNativeSubtitleLimitationsV3(r, []string{" "})
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

// setNativeSubtitleLimitationsV3 gives r an otherwise valid original_http
// native_embedded entry with these limitations.
func setNativeSubtitleLimitationsV3(r *StartRequestV3, limitations []string) {
	delivery := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	delivery.Subtitles.NativeEmbedded = []NativeEmbeddedSubtitleCapabilityV3{{Container: "mkv", Codecs: []string{"subrip"}, TrackIdentity: "container_track_id", Limitations: limitations}}
	r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery
}

// A newer client may declare a limitation this server does not know. The
// request stays valid; planning ignores that entry instead.
func TestStartRequestV3AcceptsUnknownNativeSubtitleLimitations(t *testing.T) {
	req := validStartRequestV3()
	setNativeSubtitleLimitationsV3(&req, []string{" Forced_Unlisted ", "future_limitation"})
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	got := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3].Subtitles.NativeEmbedded[0].Limitations
	if len(got) != 2 || got[0] != NativeEmbeddedLimitationForcedUnlistedV3 || got[1] != "future_limitation" {
		t.Fatalf("limitations = %q, want normalized values kept", got)
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
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{}
	result := planFileV3(req, file)
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.TargetVideoCodec != "copy" || result.Plan.Timeline.SourceStartSeconds != start {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3ClientManagedDynamicRangeUsesOriginalOnSDROutput(t *testing.T) {
	file := detailedFixtureFileV3()
	request := clientManagedSDRRequestV3()

	result := planFileV3(request, file)
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect {
		t.Fatalf("client-managed HDR source did not reach original_http: %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.DecisionReason != decisionReasonClientManagedDynamicRangeV3 {
		t.Fatalf("decision reason = %q, want client_managed_dynamic_range", result.Plan.DecisionReason)
	}
	if len(result.Plan.Transformations) != 0 {
		t.Fatalf("Aether-owned routing must not be represented as a selectable transformation: %#v", result.Plan.Transformations)
	}
	if result.Plan.Claims.Video.HDR10 || result.Plan.Claims.Video.HDR10Plus || result.Plan.Claims.Video.HLG || result.Plan.Claims.Video.DolbyVision {
		t.Fatalf("server must not invent the client's runtime output mode: %#v", result.Plan.Claims.Video)
	}
}

func validStartRequestV3() StartRequestV3 {
	return StartRequestV3{
		ProtocolVersion:            ProtocolV3,
		ClientFeatures:             []string{FeaturePlaybackPlanV3},
		FileID:                     42,
		ProfileID:                  "profile-1",
		PlaybackAttemptID:          "attempt-0001",
		QualityPreference:          "original",
		SubtitleFidelityPreference: SubtitleFidelityCompatibleV3,
		Capabilities:               ClientCodecCapabilitiesV3{VideoEvidence: EvidenceExactV3, AudioEvidence: EvidenceExactV3, CodecsVideo: []string{"hevc"}, CodecsAudio: []string{"aac"}, Containers: []string{"mkv"}, MaxResolution: "2160p"},
		ClientPlaybackContext: ClientPlaybackContextV3{ProtocolVersion: ProtocolV3, FormFactor: "tv", AppVersion: "test", Device: DeviceContextV3{Platform: "android", OSVersion: "15"}, Output: OutputContextV3{OutputContextID: "route-1"}, Deliveries: map[string]DeliveryCapabilityV3{
			DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true, Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}},
			DeliveryClassProgressiveV3:  {Enabled: true, SupportedOnDevice: true},
			DeliveryClassHLSV3:          {Enabled: true, SupportedOnDevice: true},
		}},
	}
}

func detailedFixtureFileV3() *models.MediaFile {
	return &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac", Resolution: "2160p", Bitrate: 60_000, AudioChannels: 2, VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160, FrameRate: "24000/1001", Bitrate: 60_000, BitDepth: 10, VideoRange: "HDR", VideoRangeType: "HDR10", ColorRange: "tv"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

func testTransformationRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "audio_to_aac", Available: true},
		{Name: "video_to_h264", Available: true},
		{Name: "server_dv7_to_hdr10", Available: true},
	})
}

// replanFixtureV3 is a valid failure-recovery replan of validStartRequestV3
// whose replan, plan and plan-attempt ids carry the given suffix.
func replanFixtureV3(id string) ReplanRequestV3 {
	start := validStartRequestV3()
	return ReplanRequestV3{
		ProtocolVersion:       ProtocolV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "replan-" + id,
		FailedPlanID:          "plan:" + id,
		PlanAttemptID:         "plan-attempt-" + id,
		PlanAttemptKey:        "v3:0000000000000001",
		AttemptCount:          1,
		QualityPreference:     start.QualityPreference,
		Failure:               FailureV3{Classification: "parser_failure"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
}

// hevcMain10DecodeV3 is an exact hardware HEVC Main 10 decoder that covers
// the 4K detailedFixtureFileV3 source.
func hevcMain10DecodeV3() []VideoDecodeCapabilityV3 {
	return []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
}

// hdr10ClientRequestV3 is validStartRequestV3 with hevcMain10DecodeV3 and
// HDR10 capability; withOutput also declares an HDR10 output.
func hdr10ClientRequestV3(withOutput bool) StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = hevcMain10DecodeV3()
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	if withOutput {
		req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	}
	return req
}

// toneMapTestRegistryV3 advertises the versioned AAC, H.264 and HDR-to-SDR
// tone-map recipes.
func toneMapTestRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: TransformationHDRToSDRToneMapV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, Available: true},
	})
}

// declaredRemuxFixtureV3 returns an SDR HEVC source with one audio track and a
// declared-evidence request that decodes AAC and the track's codec, offers no
// original_http, and limits progressive to mp4/HEVC and HLS to hlsVideo.
func declaredRemuxFixtureV3(track models.AudioTrack, progressiveAudio, hlsVideo, hlsAudio string) (*models.MediaFile, StartRequestV3) {
	file := detailedFixtureFileV3()
	file.CodecAudio = track.Codec
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.AudioTracks[0] = track

	req := validStartRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidenceDeclaredV3
	req.Capabilities.CodecsAudio = []string{"aac", track.Codec}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
	progressive := req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3]
	progressive.Containers = []string{"mp4"}
	progressive.VideoCodecs = []string{"hevc"}
	progressive.AudioDecodeCodecs = []string{progressiveAudio}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = progressive
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Containers = []string{"hls"}
	hls.VideoCodecs = []string{hlsVideo}
	hls.AudioDecodeCodecs = []string{hlsAudio}
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	return file, req
}

// clientManagedSDRRequestV3 is a declared-evidence request on an SDR output
// whose original_http delivery (mkv, HEVC, AAC) carries the client-managed
// dynamic-range claim.
func clientManagedSDRRequestV3() StartRequestV3 {
	request := validStartRequestV3()
	request.Capabilities.VideoEvidence = EvidenceDeclaredV3
	request.Capabilities.AudioEvidence = EvidenceDeclaredV3
	request.Capabilities.HDR = false
	request.Capabilities.HDRDetails = &HDRCapabilitiesV3{}
	request.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{}
	direct := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Containers = []string{"mkv"}
	direct.VideoCodecs = []string{"hevc"}
	direct.AudioDecodeCodecs = []string{"aac"}
	direct.HDRDetails = &HDRCapabilitiesV3{}
	direct.ValidatedClaims = []string{ClaimClientManagedDynamicRangeV3}
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	return request
}

// dv7WithELFixtureV3 is the 4K fixture as a Dolby Vision Profile 7 source
// whose enhancement layer type is unknown.
func dv7WithELFixtureV3() *models.MediaFile {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVELPresent = true
	file.VideoTracks[0].DVEnhancementLayer = "unknown"
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	return file
}

// clientManagedDV7RequestV3 is a declared-evidence request with client video
// transforms whose original_http delivery (mkv, HEVC, AAC, directHDR) carries
// the client-managed dynamic-range claim and the given client transformations.
func clientManagedDV7RequestV3(hdr, outputHDR, directHDR *HDRCapabilitiesV3, transformations ...TransformationV3) StartRequestV3 {
	request := validStartRequestV3()
	request.ClientFeatures = append(request.ClientFeatures, FeatureClientVideoTransforms)
	request.Capabilities.VideoEvidence = EvidenceDeclaredV3
	request.Capabilities.AudioEvidence = EvidenceDeclaredV3
	request.Capabilities.HDRDetails = hdr
	request.ClientPlaybackContext.Output.HDRDetails = outputHDR
	direct := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Containers = []string{"mkv"}
	direct.VideoCodecs = []string{"hevc"}
	direct.AudioDecodeCodecs = []string{"aac"}
	direct.HDRDetails = directHDR
	direct.ValidatedClaims = append(direct.ValidatedClaims, ClaimClientManagedDynamicRangeV3)
	direct.Transformations = transformations
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	return request
}

// planFileV3 plans req against file with transcoding (4K included) enabled
// and the standard test registry.
func planFileV3(req StartRequestV3, file *models.MediaFile) PlannerResultV3 {
	return PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
}

// availableQualities must publish useful same-class bitrate steps and every
// lower resolution step alongside the source-preserving "original" entry, and
// shrink to "original" alone when the transcode route cannot execute.
func TestPlanPlaybackV3PublishesAvailableQualities(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].BitDepth = 8
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}

	result := planFileV3(req, file)
	if result.Plan == nil {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	labels := make([]string, 0, len(result.Plan.AvailableQualities))
	for _, quality := range result.Plan.AvailableQualities {
		labels = append(labels, quality.Label)
	}
	want := []string{
		"original",
		QualityRung2160pHighV3, QualityRung2160pMediumV3, QualityRung2160pLowV3,
		QualityRung1080pHighV3, QualityRung1080pMediumV3, QualityRung1080pLowV3,
		QualityRung720pHighV3, QualityRung720pMediumV3, QualityRung720pLowV3,
		"480p", QualityRung480pLowV3, QualityRung360pV3,
	}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	if !result.Plan.AvailableQualities[0].PreservesSource || result.Plan.AvailableQualities[0].Height != 2160 {
		t.Fatalf("original entry = %#v", result.Plan.AvailableQualities[0])
	}
	if got := result.Plan.AvailableQualities[2]; got.BitrateKbps != 20_000 || got.DisplayName != "4K Medium" {
		t.Fatalf("4K Medium = %#v", got)
	}

	// Without an HLS delivery the ladder cannot execute: menu shrinks to
	// original only.
	noHLS := validStartRequestV3()
	noHLS.Capabilities.VideoDecode = req.Capabilities.VideoDecode
	delete(noHLS.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	result = PlanPlaybackV3(PlannerInputV3{Request: noHLS, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || len(result.Plan.AvailableQualities) != 1 || result.Plan.AvailableQualities[0].Label != "original" {
		t.Fatalf("no-HLS qualities = %#v (%s)", result.Plan, ExplainPlannerResultV3(result))
	}
}

// A Dolby Vision Profile 7 4K source the client converts to Profile 8.1, under
// a bandwidth estimate it cannot carry and with 4K transcoding off, played at
// original quality. With a lower version serving lower qualities it is refused
// for a reason the alternate-version fallback acts on instead.
func TestPlanPlaybackV3AutoBandwidthLimitRefusesDV7SourceForLowerVersion(t *testing.T) {
	file := dv7WithELFixtureV3()
	hdr := &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	request := clientManagedDV7RequestV3(hdr, hdr, hdr,
		TransformationV3{Name: ClientDV7ToDV81V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3},
	)
	request.QualityPreference = QualityAutoV3
	request.BandwidthEstimateKbps = new(4_000)
	request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	input := PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, HardwareToneMapEnabled: true, SoftwareToneMapEnabled: true},
		Registry: NewTransformationRegistryV3(nil),
	}

	original := PlanPlaybackV3(input)
	if original.Plan == nil || original.Plan.DecisionReason != "client_dv7_to_dv81" || !hasDegradationWarningV3(original.Plan.DegradationWarnings, "quality_reduction_unavailable") {
		t.Fatalf("without a lower version = %s, want the source at original quality", ExplainPlannerResultV3(original))
	}

	input.LowerVersion = &LowerVersionV3{
		Requested: SourceDescriptorFromFileV3(file, 0),
		Lower:     SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 32_000, DynamicRange: DynamicRangeSDRV3},
	}
	refused := PlanPlaybackV3(input)
	if refused.Terminal == nil || (refused.Terminal.Reason != "no_alternate_version" && refused.Terminal.Reason != TerminalHDRTranscodeUnsupportedV3) {
		t.Fatalf("with a lower version = %s, want a refusal the alternate-version fallback acts on", ExplainPlannerResultV3(refused))
	}
}

// Without an encode toolchain, a quality the lower version serves still goes
// to it, which can play it at its own smaller size, rather than to a refusal
// the alternate-version fallback does not act on.
func TestPlanPlaybackV3LowerVersionTakesQualityWithoutToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].Profile, file.VideoTracks[0].BitDepth = "Main", 8
	file.VideoTracks[0].VideoRange, file.VideoTracks[0].VideoRangeType = "SDR", "SDR"
	request := validStartRequestV3()
	request.QualityPreference = QualityAutoV3
	request.BandwidthEstimateKbps = new(4_000)
	input := PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, PreferLowerVersionTranscode: true},
		Registry: NewTransformationRegistryV3(nil),
		LowerVersion: &LowerVersionV3{
			Requested: SourceDescriptorFromFileV3(file, 0),
			Lower:     SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 8_000, DynamicRange: DynamicRangeSDRV3},
		},
	}
	if result := PlanPlaybackV3(input); result.Terminal == nil || result.Terminal.Reason != "no_alternate_version" {
		t.Fatalf("result = %s, want the quality refused for the lower version", ExplainPlannerResultV3(result))
	}
}

// A 4K version that may not be transcoded offers the qualities its
// lower-resolution version serves: that version as it is, then the rungs a
// transcode of it can reach. Original stays the 4K version.
func TestAvailableQualitiesV3LowerVersionServes4KWithout4KTranscode(t *testing.T) {
	requested := SourceDescriptorV3{VideoCodec: "hevc", Width: 3840, Height: 2160, BitrateKbps: 69_000, DynamicRange: DynamicRangeHDR10V3}
	lower := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 26_000, DynamicRange: DynamicRangeSDRV3}
	input := PlannerInputV3{
		Request:      validStartRequestV3(),
		Settings:     PlannerSettingsV3{TranscodeEnabled: true, HardwareToneMapEnabled: true},
		LowerVersion: &LowerVersionV3{Requested: requested, Lower: lower},
	}
	labels := func(qualities []AvailableQualityV3) []string {
		out := make([]string, 0, len(qualities))
		for _, quality := range qualities {
			out = append(out, quality.Label)
		}
		return out
	}

	// The planner may be planning either version; the menu is the same.
	for _, source := range []SourceDescriptorV3{requested, lower} {
		got := availableQualitiesV3(input, source)
		want := []string{
			QualityOriginalV3, "1080p",
			QualityRung1080pHighV3, QualityRung1080pMediumV3, QualityRung1080pLowV3,
			QualityRung720pHighV3, QualityRung720pMediumV3, QualityRung720pLowV3,
			"480p", QualityRung480pLowV3, QualityRung360pV3,
		}
		if !reflect.DeepEqual(labels(got), want) {
			t.Fatalf("labels = %v, want %v", labels(got), want)
		}
		if got[0].Height != 2160 || got[0].BitrateKbps != 69_000 || !got[0].PreservesSource {
			t.Fatalf("original entry = %#v, want the 4K version", got[0])
		}
		if got[1] != (AvailableQualityV3{Label: "1080p", DisplayName: "1080p", Height: 1080, BitrateKbps: 26_000}) {
			t.Fatalf("lower version entry = %#v", got[1])
		}
	}

	// A viewer who may not transcode can still switch to the lower version.
	restricted := input
	restricted.Settings.ViewerTranscodeDisabled = true
	if got := labels(availableQualitiesV3(restricted, requested)); !reflect.DeepEqual(got, []string{QualityOriginalV3, "1080p"}) {
		t.Fatalf("restricted viewer labels = %v", got)
	}

	// The 4K source's own ladder applies whenever the lower version is not
	// how lower qualities are reached.
	for name, mutate := range map[string]func(*PlannerInputV3){
		"4K transcode allowed": func(in *PlannerInputV3) { in.Settings.Allow4KTranscode = true },
		"transcode disabled":   func(in *PlannerInputV3) { in.Settings.TranscodeEnabled = false },
		"no HLS": func(in *PlannerInputV3) {
			in.Request = validStartRequestV3()
			delete(in.Request.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
		},
	} {
		changed := input
		mutate(&changed)
		withoutLower := changed
		withoutLower.LowerVersion = nil
		if got, want := availableQualitiesV3(changed, requested), availableQualitiesV3(withoutLower, requested); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: qualities = %v, want the source ladder %v", name, labels(got), labels(want))
		}
	}
}

func audioOnlyFixtureFileV3() *models.MediaFile {
	return &models.MediaFile{ID: 77, BaseType: "audiobook", FilePath: "/media/audiobook.m4b", Container: "mp4", CodecAudio: "aac", Bitrate: 128, AudioChannels: 2, Duration: 39_600, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

// An audio-only source with client-decodable audio must plan the validated
// original route, skipping every video/HDR/subtitle gate.
func TestPlanPlaybackV3AudioOnlyPlansOriginalHTTP(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.DecisionReason != "validated_original_playback" {
		t.Fatalf("decision reason = %q", result.Plan.DecisionReason)
	}
	if result.Plan.EffectiveRecipe.VideoCodec != "" || result.Plan.Subtitle.Mode != SubtitleOffV3 {
		t.Fatalf("audio-only plan carried video state: %#v", result.Plan)
	}
	if len(result.Plan.AvailableQualities) != 1 || result.Plan.AvailableQualities[0].Label != "original" || !result.Plan.AvailableQualities[0].PreservesSource {
		t.Fatalf("audio-only qualities = %#v", result.Plan.AvailableQualities)
	}
	if !strings.HasPrefix(result.Plan.PlanAttemptKey, "v3:") {
		t.Fatalf("attempt key = %q", result.Plan.PlanAttemptKey)
	}
}

func TestPlanPlaybackV3AudioOnlyHonorsBandwidthCap(t *testing.T) {
	for _, test := range []struct {
		name       string
		bitrate    int
		capKbps    *int
		wantDirect bool
		wantAAC    int
	}{
		{name: "source exceeds cap", bitrate: 1_200, capKbps: intPointerV3(256), wantAAC: 192},
		{name: "cap below AAC default", bitrate: 1_200, capKbps: intPointerV3(96), wantAAC: 96},
		{name: "source is below cap", bitrate: 128, capKbps: intPointerV3(256), wantDirect: true},
		{name: "uncapped", bitrate: 1_200, wantDirect: true},
		{name: "unknown source bitrate", capKbps: intPointerV3(256), wantDirect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := audioOnlyFixtureFileV3()
			file.Bitrate = test.bitrate
			req := validStartRequestV3()
			req.FileID = file.ID
			req.Capabilities.Containers = []string{"mp4"}
			req.BandwidthCapKbps = test.capKbps

			result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
			if test.wantDirect {
				if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.TranscodeAudio {
					t.Fatalf("result = %s", ExplainPlannerResultV3(result))
				}
				return
			}
			if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetAudioCodec != "aac" {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if result.Plan.DecisionReason != "quality_bandwidth_cap" || !hasDegradationWarningV3(result.Plan.DegradationWarnings, "audio_converted") || !hasDegradationWarningV3(result.Plan.DegradationWarnings, "bandwidth_cap_applied") {
				t.Fatalf("capped plan = %#v", result.Plan)
			}
			if result.TargetAudioBitrateKbps != test.wantAAC || result.Plan.EffectiveRecipe.BitrateKbps == nil || *result.Plan.EffectiveRecipe.BitrateKbps != test.wantAAC {
				t.Fatalf("AAC bitrate = target %d recipe %#v, want %d", result.TargetAudioBitrateKbps, result.Plan.EffectiveRecipe.BitrateKbps, test.wantAAC)
			}
		})
	}
}

func TestPlanPlaybackV3NonDefaultAudioSelectionRequiresScopedOriginalClaim(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.AudioTracks = []models.AudioTrack{
		{Codec: "aac", Channels: 2, Layout: "stereo", Default: true},
		{Codec: "aac", Channels: 2, Layout: "stereo"},
	}
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = hevcMain10DecodeV3()

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 1, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.PlayMethod != PlayRemux || result.TranscodeAudio {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	packaged := req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3]
	packaged.ValidatedClaims = append(packaged.ValidatedClaims, ClaimClientSelectedAudioTrackV3)
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = packaged
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 1, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("packaged claim leaked into original eligibility: %s", ExplainPlannerResultV3(result))
	}
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.ValidatedClaims = append(direct.ValidatedClaims, ClaimClientSelectedAudioTrackV3)
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 1, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect || result.TranscodeAudio {
		t.Fatalf("claimed original selection = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.SelectedTracks.Audio == nil || result.Plan.SelectedTracks.Audio.Index == nil || *result.Plan.SelectedTracks.Audio.Index != 1 {
		t.Fatalf("selected audio = %#v", result.Plan.SelectedTracks.Audio)
	}
}

func androidMedia3FFmpegFixtureV3(
	formFactor string,
	audioTracks []models.AudioTrack,
	audioDecodeCodecs []string,
	maxChannels int,
) (*models.MediaFile, StartRequestV3) {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "h264",
		CodecAudio: audioTracks[0].Codec, Resolution: "1080p", Bitrate: 12_000,
		AudioChannels: audioTracks[0].Channels,
		VideoTracks: []models.VideoTrack{{
			Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080,
			FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR",
		}},
		AudioTracks: audioTracks,
	}
	request := validStartRequestV3()
	request.FileID = file.ID
	request.Capabilities = ClientCodecCapabilitiesV3{
		VideoEvidence: EvidenceExactV3, AudioEvidence: EvidenceExactV3,
		CodecsVideo: []string{"h264"}, CodecsVideoHardware: []string{"h264"},
		CodecsAudio: audioDecodeCodecs, Containers: []string{"mkv", "matroska", "m3u8", "hls"},
		MaxResolution: "1080p",
		VideoDecode: []VideoDecodeCapabilityV3{{
			Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8},
			MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000, Hardware: true,
		}},
	}
	request.ClientPlaybackContext = ClientPlaybackContextV3{
		ProtocolVersion: ProtocolV3, FormFactor: formFactor, AppVersion: "1.0.0", AppBuild: "16", AppChannel: "beta",
		Device: DeviceContextV3{
			Platform: "android", OSVersion: "16", Manufacturer: "Android", Model: "Media3 device",
			PlatformDetails: map[string]string{"sdk_int": "36", "abis": "arm64-v8a"},
		},
		Output: OutputContextV3{CurrentSink: "local_output", SinkType: "local", OutputContextID: "route-1"},
		Deliveries: map[string]DeliveryCapabilityV3{
			DeliveryClassOriginalHTTPV3: {
				Enabled: true, SupportedOnDevice: true, Containers: []string{"mkv", "matroska"},
				VideoCodecs: []string{"h264"}, AudioDecodeCodecs: audioDecodeCodecs, MaxChannels: intPointerV3(maxChannels),
				Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true, EmbeddedBitmap: true, SidecarBitmap: true},
				Features:  []string{"track_switching", "buffer_reporting"}, AuthHeaderRefresh: true,
				ValidatedClaims: []string{ClaimClientSelectedAudioTrackV3},
			},
			DeliveryClassProgressiveV3: {
				Enabled: false, SupportedOnDevice: false, FailureReason: "disabled_pending_seekable_transport",
				Containers:  []string{"mp4", "m4v", "webm", "mkv", "matroska"},
				VideoCodecs: []string{"h264"}, AudioDecodeCodecs: audioDecodeCodecs, MaxChannels: intPointerV3(maxChannels),
			},
			DeliveryClassHLSV3: {
				Enabled: true, SupportedOnDevice: true, Containers: []string{"m3u8", "hls"},
				VideoCodecs: []string{"h264"}, AudioDecodeCodecs: audioDecodeCodecs, MaxChannels: intPointerV3(maxChannels),
				Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true, EmbeddedBitmap: true, SidecarBitmap: true},
				Features:  []string{"hls", ClientNativeHLSPlaybackV3, "track_switching", "buffer_reporting"}, AuthHeaderRefresh: true,
			},
		},
	}
	return file, request
}

func TestPlanPlaybackV3AudioOnlyConvertsForProgressiveCodecSubset(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.CodecAudio = "flac"
	file.AudioChannels = 2
	file.AudioTracks[0].Codec = "flac"
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.CodecsAudio = []string{"aac", "flac"}
	req.Capabilities.Containers = []string{"mp4"}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
	maxChannels := 1
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, AudioDecodeCodecs: []string{"aac"}, MaxChannels: &maxChannels,
	}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetAudioCodec != "aac" || result.TargetAudioChannels != 1 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.EffectiveRecipe.AudioChannels == nil || *result.Plan.EffectiveRecipe.AudioChannels != 1 || result.Plan.EffectiveRecipe.AudioLayout != "mono" {
		t.Fatalf("AAC recipe = %#v", result.Plan.EffectiveRecipe)
	}
	if result.Plan.DecisionReason != "audio_adaptation" || len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != TransformationAudioToAACV3 || !hasDegradationWarningV3(result.Plan.DegradationWarnings, "audio_converted") {
		t.Fatalf("converted plan = %#v", result.Plan)
	}
}

func TestPlanPlaybackV3VideoRemuxHLSAudioCodecRouting(t *testing.T) {
	for _, test := range []struct {
		name               string
		hlsVideo           string
		wantDelivery       DeliveryV3
		wantTranscodeAudio bool
		wantAudioCodec     string
	}{
		{name: "HLS-only audio codec is preserved", hlsVideo: "hevc", wantDelivery: DeliveryRemuxHLSV3, wantAudioCodec: "copy"},
		{name: "progressive adapts when HLS video is unsupported", hlsVideo: "h264", wantDelivery: DeliveryRemuxProgressiveV3, wantTranscodeAudio: true, wantAudioCodec: "aac"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, req := declaredRemuxFixtureV3(models.AudioTrack{Codec: "eac3", Channels: 6, Layout: "5.1"}, "aac", test.hlsVideo, "eac3")

			result := PlanPlaybackV3(PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
			})
			if result.Plan == nil || result.Plan.Delivery != test.wantDelivery || result.TranscodeAudio != test.wantTranscodeAudio || result.TargetAudioCodec != test.wantAudioCodec {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
		})
	}
}

func TestPlanPlaybackV3VideoOnlyRemuxIgnoresScopedAudioConstraints(t *testing.T) {
	file := detailedFixtureFileV3()
	file.CodecAudio = ""
	file.AudioChannels = 0
	file.AudioTracks = nil
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"

	req := validStartRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.Containers = []string{"mp4"}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	progressive := req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3]
	progressive.Containers = []string{"mp4"}
	progressive.VideoCodecs = []string{"hevc"}
	progressive.AudioDecodeCodecs = []string{"aac"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = progressive

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: nil,
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.TranscodeAudio || len(result.Plan.Transformations) != 0 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.Claims.Audio.Reason != "no_audio_track" || result.Plan.EffectiveRecipe.AudioCodec != "" {
		t.Fatalf("audio recipe = %#v, claims = %#v", result.Plan.EffectiveRecipe, result.Plan.Claims.Audio)
	}
}

func TestPlanPlaybackV3VideoRemuxWithNilRegistryReturnsAudioConversionTerminal(t *testing.T) {
	file, req := declaredRemuxFixtureV3(models.AudioTrack{Codec: "vorbis", Channels: 2, Layout: "stereo"}, "aac", "hevc", "aac")

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: nil,
	})
	if result.Terminal == nil || result.Terminal.Reason != TerminalAudioConversionUnsupportedV3 || !result.Terminal.Retryable {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

// A container mismatch on decodable audio is a remux, not a conversion, and a
// client with neither route left gets an honest terminal. An audio-only file
// with no probed audio codec keeps its own metadata terminal.
func TestPlanPlaybackV3AudioOnlyRemuxesForeignContainerAndTerminals(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.Container = "ogg"
	file.FilePath = "/media/audiobook.ogg"
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.TranscodeAudio {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.DecisionReason != "container_normalization" {
		t.Fatalf("decision reason = %q", result.Plan.DecisionReason)
	}

	progressiveless := validStartRequestV3()
	progressiveless.FileID = file.ID
	progressiveless.Capabilities.Containers = []string{"mkv"}
	delete(progressiveless.ClientPlaybackContext.Deliveries, DeliveryClassProgressiveV3)
	result = PlanPlaybackV3(PlannerInputV3{Request: progressiveless, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "adaptation_unavailable" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	unprobed := audioOnlyFixtureFileV3()
	unprobed.CodecAudio = ""
	unprobed.AudioTracks = nil
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: unprobed, EffectiveFile: unprobed, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "source_metadata_incomplete" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}
