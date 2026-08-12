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

func TestServerFeaturesV3ReturnsCompleteIndependentSlices(t *testing.T) {
	first := ServerFeaturesV3()
	second := ServerFeaturesV3()
	expected := map[string]struct{}{
		FeaturePlaybackPlanV3:       {},
		FeatureNeutralContractV3:    {},
		FeatureLayoutPassthrough:    {},
		FeatureRouteDiagnostics:     {},
		FeatureDeviceQuirksV3:       {},
		FeatureSeekReanchorV3:       {},
		FeatureOutputChangeV3:       {},
		FeatureDirectStreamResumeV3: {},
		FeaturePlanSourceDurationV3: {},
	}
	if len(first) != len(expected) {
		t.Fatalf("server features = %v, want %d entries", first, len(expected))
	}
	seen := make(map[string]struct{}, len(first))
	for _, feature := range first {
		if _, ok := expected[feature]; !ok {
			t.Fatalf("server features contain unexpected %q: %v", feature, first)
		}
		if _, duplicate := seen[feature]; duplicate {
			t.Fatalf("server features contain duplicate %q: %v", feature, first)
		}
		seen[feature] = struct{}{}
	}
	for feature := range expected {
		if _, ok := seen[feature]; !ok {
			t.Fatalf("server features omitted %q: %v", feature, first)
		}
	}

	first[0] = "mutated"
	if second[0] != FeaturePlaybackPlanV3 {
		t.Fatalf("feature slices share backing storage: %v", second)
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

func TestStartRequestV3ProgressPersistenceValidation(t *testing.T) {
	req := validStartRequestV3()
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if req.ProgressPersistence != ProgressPersistenceServerV3 {
		t.Fatalf("omitted progress_persistence normalized to %q", req.ProgressPersistence)
	}

	req = validStartRequestV3()
	req.ProgressPersistence = ProgressPersistenceClientV3
	req.StartPosition = nil
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("client progress persistence without explicit start_position was accepted")
	}
	zero := 0.0
	req.StartPosition = &zero
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("explicit zero must remain distinguishable and valid: %v", err)
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

func TestHDR10CapabilityLimitsBoundTheClaimedStreamClass(t *testing.T) {
	width, height, frameRate, bitrate := 3840, 2160, 24.0, 80_000
	plan := PlanV3{EffectiveRecipe: EffectiveRecipeV3{
		VideoCodec: "hevc", Width: &width, Height: &height, FrameRate: &frameRate,
		BitrateKbps: &bitrate, DynamicRange: DynamicRangeHDR10V3,
	}}
	hdr := HDRCapabilitiesV3{
		HDR10: true, HDR10MaxWidth: 3840, HDR10MaxHeight: 2160,
		HDR10MaxFrameRate: 24, HDR10MaxBitrateKbps: 80_000,
	}
	if !hdrDetailsSupportPlanV3(hdr, plan) {
		t.Fatal("the exactly probed HDR10 stream class was rejected")
	}
	tooFast := 60.0
	plan.EffectiveRecipe.FrameRate = &tooFast
	if hdrDetailsSupportPlanV3(hdr, plan) {
		t.Fatal("an HDR10 stream above the probed frame-rate ceiling was accepted")
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

func TestPlanAttemptKeyV3Fixture(t *testing.T) {
	type fixture struct {
		Name                 string   `json:"name"`
		ServerPlanAttemptKey string   `json:"server_plan_attempt_key"`
		ReplanEcho           string   `json:"replan_echo"`
		AttemptedPlanKeys    []string `json:"attempted_plan_keys"`
		ExpectedServerAction string   `json:"expected_server_action"`
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
			if value.ServerPlanAttemptKey == "" || value.ReplanEcho != value.ServerPlanAttemptKey {
				t.Fatalf("opaque echo drifted: %#v", value)
			}
			if len(value.AttemptedPlanKeys) != 1 || value.AttemptedPlanKeys[0] != value.ServerPlanAttemptKey {
				t.Fatalf("attempted plan keys do not echo the server token: %#v", value)
			}
			if value.ExpectedServerAction != "reject_already_attempted_plan" {
				t.Fatalf("server action = %q", value.ExpectedServerAction)
			}
		})
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
		"restart_matrix", "capacity_matrix", "route_event_limits",
	} {
		if !categories[required] {
			t.Errorf("conformance matrix omits %q", required)
		}
	}
	for name, delivery := range map[string]DeliveryV3{
		"evidence_exact":                  DeliveryTranscodeHLSV3,
		"evidence_platform_attested":      DeliveryOriginalHTTPV3,
		"evidence_declared":               DeliveryOriginalHTTPV3,
		"delivery_original":               DeliveryOriginalHTTPV3,
		"delivery_progressive":            DeliveryRemuxProgressiveV3,
		"delivery_hls":                    DeliveryRemuxHLSV3,
		"delivery_transcode":              DeliveryTranscodeHLSV3,
		"audio_only_original":             DeliveryOriginalHTTPV3,
		"hdr10_exact_direct":              DeliveryOriginalHTTPV3,
		"dolby_vision_8_exact_direct":     DeliveryOriginalHTTPV3,
		"dolby_vision_7_hdr10_fallback":   DeliveryRemuxProgressiveV3,
		"truehd_audio_conversion":         DeliveryRemuxProgressiveV3,
		"truehd_exact_layout_passthrough": DeliveryOriginalHTTPV3,
		"embedded_pgs_sidecar":            DeliveryOriginalHTTPV3,
		"embedded_ass_authored_render":    DeliveryOriginalHTTPV3,
		"embedded_dvd_burn_in":            DeliveryTranscodeHLSV3,
	} {
		value, ok := plannerByName[name]
		if !ok || value.Expected.Outcome != OutcomePlayableV3 || value.Expected.Delivery != delivery {
			t.Errorf("scenario %q = %#v, want playable %q", name, value.Expected, delivery)
		}
	}
	if value := plannerByName["available_qualities"]; len(value.Expected.AvailableQualities) < 2 || value.Expected.AvailableQualities[0].Label != QualityOriginalV3 {
		t.Errorf("available quality fixture = %#v", value.Expected.AvailableQualities)
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

func TestStartRequestV3RequiresCapabilityEvidenceTiers(t *testing.T) {
	req := validStartRequestV3()
	req.Capabilities.VideoEvidence = ""
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("missing video_evidence was accepted")
	}
	req = validStartRequestV3()
	req.Capabilities.AudioEvidence = "guessed"
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("unknown audio_evidence was accepted")
	}
	req = validStartRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidencePlatformAttestedV3
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("valid evidence tiers rejected: %v", err)
	}
}

// The same SDR source must reach a tier-appropriate route for each evidence
// tier: exact and platform_attested validate against decode entries (the
// latter without profile/level matching), declared grants the copy route on
// the flat codec+container match alone.
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
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
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

// Only exact audio evidence earns passthrough claims; platform_attested
// evidence still qualifies for a copy route via decode support.
func TestAudioEligibilityV3PassthroughRequiresExactEvidence(t *testing.T) {
	source := SourceDescriptorV3{AudioCodec: "eac3", AudioChannels: 6, AudioLayout: "5.1"}
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureLayoutPassthrough)
	req.Capabilities.CodecsAudio = []string{"eac3"}
	req.Capabilities.AudioPassthrough = &AudioPassthroughV3{
		PassthroughCodecs: []string{"eac3"}, MaxChannels: 8,
		Entries: []AudioPassthroughEntryV3{{Codec: "eac3", ChannelCounts: []int{6}, Layouts: []string{"5.1"}}},
	}

	copyOK, passthrough, claim := audioEligibilityV3(source, req)
	if !copyOK || !passthrough || !claim.Passthrough {
		t.Fatalf("exact evidence passthrough = %v %v %#v", copyOK, passthrough, claim)
	}

	req.Capabilities.AudioEvidence = EvidencePlatformAttestedV3
	copyOK, passthrough, claim = audioEligibilityV3(source, req)
	if !copyOK || passthrough || claim.Passthrough {
		t.Fatalf("platform_attested evidence = %v %v %#v", copyOK, passthrough, claim)
	}
}

func TestPlanPlaybackV3DirectRequiresDetailedEvidence(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	// Exact-tier evidence with no validating decode entry cannot earn a
	// direct route; without a transcode fallback the plan terminates.
	req.Capabilities.VideoDecode = nil
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

func TestPlanPlaybackV3DirectPlaysLegacyHDR10WithInferredBitDepth(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].BitDepth = 0
	file.VideoTracks[0].PixelFormat = "yuv420p10le"
	req := validStartRequestV3()
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

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "source_metadata_incomplete" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3TranscodesVP9WithUnknownCodecLevel(t *testing.T) {
	file := detailedFixtureFileV3()
	file.CodecVideo = "vp9"
	file.CodecAudio = "opus"
	file.Resolution = "2160p"
	file.Bitrate = 2_797
	file.VideoTracks[0] = models.VideoTrack{
		Codec: "vp9", Profile: "Profile 0", Level: -99,
		Width: 1080, Height: 1920, FrameRate: "24.000", Bitrate: 2_797,
		BitDepth: 8, PixelFormat: "yuv420p", VideoRange: "SDR",
		VideoRangeType: "SDR", ColorRange: "tv", ColorTransfer: "bt709",
	}
	file.AudioTracks[0] = models.AudioTrack{Codec: "opus", Channels: 2, Layout: "stereo"}

	// An exact client that constrains VP9 levels must not earn a direct route
	// from the unknown ffprobe sentinel. The level is optional for planning a
	// server transcode, but it cannot satisfy a concrete decoder bound.
	source := SourceDescriptorFromFileV3(file, 0)
	exact := validStartRequestV3()
	exact.Capabilities.CodecsVideo = []string{"vp9"}
	exact.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
		Codec: "vp9", Profiles: []string{"profile 0"}, Levels: []int{41},
		BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, Hardware: true,
	}}
	if direct, _ := videoEligibleV3(source, exact); direct {
		t.Fatal("an unknown VP9 level satisfied an exact decoder level bound")
	}
	// Empty exact-tier bounds are an affirmative claim that every variant is
	// supported, not missing evidence. Keep this distinct from the concrete
	// level constraint above so relaxing transcode metadata requirements cannot
	// accidentally weaken a bound the client actually supplied.
	unconstrained := exact
	unconstrained.Capabilities.VideoDecode[0].Profiles = nil
	unconstrained.Capabilities.VideoDecode[0].Levels = nil
	if direct, _ := videoEligibleV3(source, unconstrained); !direct {
		t.Fatal("an exact decoder's explicitly unconstrained profile/level claim was rejected")
	}

	req := validStartRequestV3()
	req.Capabilities.VideoEvidence = EvidencePlatformAttestedV3
	req.Capabilities.AudioEvidence = EvidencePlatformAttestedV3
	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	})

	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.TargetVideoCodec != "h264" || result.TargetAudioCodec != "aac" {
		t.Fatalf("targets = video %q audio %q", result.TargetVideoCodec, result.TargetAudioCodec)
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
	req.ClientFeatures = append(req.ClientFeatures, FeatureClientVideoTransforms)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
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

func TestPlanPlaybackV3Profile7DoesNotInferClientTransformFromHDRProfile(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
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
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetVideoCodec != "" {
		t.Fatalf("result = %#v", result)
	}
}

// copyUnsafeFixtureV3 returns an SDR source that would normally take a
// video-copy remux (its audio needs conversion, its container is not offered),
// with the copy-safety flag settable by the caller.
func copyUnsafeFixtureV3(multiPPS bool) (*models.MediaFile, StartRequestV3) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	file.VideoTracks[0].MultiplePPS = &multiPPS
	req := validStartRequestV3()
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	return file, req
}

func TestPlanPlaybackV3CopyUnsafeSourceForcesTranscode(t *testing.T) {
	// The source carries conflicting in-band PPS, so the video stream-copy remux
	// is disqualified and planning must fall through to a real transcode.
	file, req := copyUnsafeFixtureV3(true)
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.PlayMethod != PlayTranscode {
		t.Fatalf("PlayMethod = %q, want transcode; result = %s", result.PlayMethod, ExplainPlannerResultV3(result))
	}
	if result.Plan == nil || result.Plan.DecisionReason != "copy_routes_exhausted" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3CopySafeSourceStillCopies(t *testing.T) {
	// The identical source with the copy-safety scan resolved to safe keeps the
	// cheap video stream-copy remux.
	file, req := copyUnsafeFixtureV3(false)
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetVideoCodec != "" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3FallsBackFromProgressiveToHLSWithoutRepeatingKey(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
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

func TestPlanPlaybackV3NeverClaimsUnimplementedHDRTranscode(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
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

func TestPlanPlaybackV3HonorsDolbyVisionLevelBound(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithHDR10"
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVLevel = 7
	file.VideoTracks[0].DVBLCompatID = 1
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	hdr := &HDRCapabilitiesV3{
		DolbyVisionProfiles: []int{8},
		DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{
			Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1},
		}},
	}
	req.Capabilities.HDRDetails = hdr
	req.ClientPlaybackContext.Output.HDRDetails = hdr

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
	})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	file.VideoTracks[0].DVLevel = 6
	result = PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
	})
	if result.Plan == nil || !result.Plan.Claims.Video.DolbyVision {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	file.VideoTracks[0].DVBLCompatID = 4
	result = PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
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

func TestPlanPlaybackV3Profile7StripFallsBackToValidatedHLSCopy(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVELPresent = true
	file.VideoTracks[0].DVEnhancementLayer = "mel"
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{7}}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 || len(first.Plan.Transformations) != 1 {
		t.Fatalf("first = %#v", first)
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil)
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
	req.ClientFeatures = append(req.ClientFeatures, FeatureLayoutPassthrough)
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

func TestPlanPlaybackV3DownloadedSubtitleCarriesStableIdentity(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	index := 0
	req.SubtitleTrackIndex = &index
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AdditionalSubtitles: []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: "downloaded", DownloadedSubtitleID: 71}}})
	if result.Plan == nil || result.Plan.Subtitle.Mode != SubtitleRenderV3 || result.Plan.SelectedTracks.Subtitle == nil || result.Plan.SelectedTracks.Subtitle.ID != req.SubtitleTrackID || result.DownloadedSubtitleID != 71 {
		t.Fatalf("result = %#v", result)
	}
}

// The plan carries the whole ordinal space, so a client never has to rebuild it
// from the track arrays it happens to be able to render.
func TestPlanPlaybackV3PublishesTheCompleteSubtitleInventory(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.en.srt", Language: "en", Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Language: "ja", Codec: "hdmv_pgs_subtitle"},
		{Index: 1, Language: "de", Codec: "dvd_subtitle"},
	}
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	additional := []SubtitleInventoryEntryV3{{CombinedIndex: 3, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es"}}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AdditionalSubtitles: additional})
	if result.Plan == nil {
		t.Fatalf("result = %#v, want a plan", result)
	}

	inventory := result.Plan.Subtitle.Inventory
	if len(inventory) != 4 {
		t.Fatalf("inventory = %+v, want 4 entries (1 external + 2 embedded + 1 downloaded)", inventory)
	}
	for i, item := range inventory {
		if item.CombinedIndex != i {
			t.Errorf("entry %d has combined_index %d; the published space must be dense", i, item.CombinedIndex)
		}
	}
	// The burn-in-only track is present with no URL rather than dropped.
	if inventory[2].Delivery != SubtitleDeliveryBurnInOnlyV3 || inventory[2].URL != "" {
		t.Errorf("the DVD track should be published as burn-in only with no URL, got %+v", inventory[2])
	}
	if inventory[3].Source != SubtitleSourceDownloadedV3 {
		t.Errorf("the downloaded track should hold the last ordinal, got %+v", inventory[3])
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

	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
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

// Adding the inventory to the plan must not perturb plan identity: replans
// would otherwise miss the cache and clients would see spurious new attempts.
func TestPlanAttemptKeyV3IgnoresTheSubtitleInventory(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if result.Plan == nil {
		t.Fatalf("result = %#v, want a plan", result)
	}

	before := PlanAttemptKeyV3(*result.Plan, "output-1", nil)
	withInventory := *result.Plan
	withInventory.Subtitle.Inventory = []SubtitleInventoryItemV3{{TrackID: "file:42:subtitle:0", CombinedIndex: 0, Source: SubtitleSourceEmbeddedV3, Codec: "subrip", Delivery: SubtitleDeliverySidecarV3}}
	if after := PlanAttemptKeyV3(withInventory, "output-1", nil); after != before {
		t.Errorf("attempt key changed with the inventory attached: %q -> %q", before, after)
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
	result := ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil)
	if !result.RequiresBurn || result.SelectedIndex != 1 || result.TransportIndex != 0 {
		t.Fatalf("embedded burn-in result = %#v", result)
	}

	externalIndex := 0
	req.SubtitleTrackIndex = &externalIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", externalIndex)
	req.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	result = ResolveSubtitlePolicyV3(file, req, true, DeliveryClassOriginalHTTPV3, nil)
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
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	// The client has no HLS engine, so the cap-induced transcode cannot run.
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)

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
	key := PlanAttemptKeyV3(plan, "1", nil)
	if !planAttemptedV3(plan, "1", []string{"  " + key + " "}) {
		t.Fatal("whitespace-trimmed exact key must match")
	}
	if planAttemptedV3(plan, "1", []string{strings.ToUpper(key)}) {
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
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.TargetVideoCodec != "copy" || result.Plan.Timeline.SourceStartSeconds != start {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3TimelineChangePreservesRouteIdentity(t *testing.T) {
	file := detailedFixtureFileV3()
	request := validStartRequestV3()
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
		PlanAttemptKeyV3(*first.Plan, request.ClientPlaybackContext.Output.OutputContextID, nil) != PlanAttemptKeyV3(*second.Plan, request.ClientPlaybackContext.Output.OutputContextID, nil) {
		t.Fatalf("timeline changed route identity: first=%#v second=%#v", first.Plan, second.Plan)
	}
	if first.Plan.PlanAttemptKey == "" || first.Plan.PlanAttemptKey != PlanAttemptKeyV3(*first.Plan, request.ClientPlaybackContext.Output.OutputContextID, nil) {
		t.Fatalf("plan attempt key not carried on the plan: %q", first.Plan.PlanAttemptKey)
	}
	if first.Plan.Timeline.SourceStartSeconds != 0 || second.Plan.Timeline.SourceStartSeconds != startAtSeek {
		t.Fatalf("timeline positions: first=%#v second=%#v", first.Plan.Timeline, second.Plan.Timeline)
	}
}

func TestPlanPlaybackV3DroppingFallbackHistoryReintroducesRejectedRoute(t *testing.T) {
	file := detailedFixtureFileV3()
	file.FilePath = "/media/movie.mp4"
	file.Container = "mp4"
	file.CodecVideo = "h264"
	file.Resolution = "1080p"
	file.Bitrate = 8_000
	file.VideoTracks[0] = models.VideoTrack{Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}
	request := validStartRequestV3()
	request.Capabilities.CodecsVideo = []string{"h264"}
	request.Capabilities.CodecsVideoHardware = []string{"h264"}
	request.Capabilities.Containers = []string{"mp4"}
	request.Capabilities.MaxResolution = "1080p"
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	input := PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
	}
	direct := PlanPlaybackV3(input)
	if direct.Plan == nil || direct.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("direct plan = %#v", direct)
	}
	input.AttemptedKeys = []string{PlanAttemptKeyV3(*direct.Plan, request.ClientPlaybackContext.Output.OutputContextID, nil)}
	progressive := PlanPlaybackV3(input)
	if progressive.Plan == nil || progressive.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("progressive fallback = %#v", progressive)
	}
	input.AttemptedKeys = append(input.AttemptedKeys, PlanAttemptKeyV3(*progressive.Plan, request.ClientPlaybackContext.Output.OutputContextID, nil))
	hls := PlanPlaybackV3(input)
	if hls.Plan == nil || hls.Plan.Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("HLS fallback = %#v", hls)
	}

	seek := 321.25
	input.Request.StartPosition = &seek
	input.AttemptedKeys = nil // This is what the old seek-reanchor path did.
	replanned := PlanPlaybackV3(input)
	if replanned.Plan == nil || replanned.Plan.PlanID != direct.Plan.PlanID || replanned.Plan.PlanID == hls.Plan.PlanID {
		t.Fatalf("dropped fallback history did not reproduce identity drift: direct=%#v hls=%#v replanned=%#v", direct.Plan, hls.Plan, replanned.Plan)
	}
	if replanned.Plan.Delivery != DeliveryOriginalHTTPV3 ||
		replanned.Plan.Stream.Protocol != StreamHTTPProgressiveV3 || replanned.Plan.Stream.Container != "mp4" {
		t.Fatalf("reintroduced route = %#v", replanned.Plan)
	}
}

func TestPlanPlaybackV3AppliesDeliverySpecificCodecAndChannelLimits(t *testing.T) {
	file := detailedFixtureFileV3()
	file.FilePath = "/media/movie.mp4"
	file.Container = "mp4"
	file.CodecVideo = "h264"
	file.Resolution = "1080p"
	file.Bitrate = 8_000
	file.AudioChannels = 6
	file.VideoTracks[0] = models.VideoTrack{Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}
	file.AudioTracks[0] = models.AudioTrack{Codec: "aac", Channels: 6, Layout: "5.1"}

	request := validStartRequestV3()
	request.Capabilities.CodecsVideo = []string{"h264"}
	request.Capabilities.CodecsVideoHardware = []string{"h264"}
	request.Capabilities.CodecsAudio = []string{"aac"}
	request.Capabilities.Containers = []string{"mp4"}
	request.Capabilities.MaxResolution = "1080p"
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	maxStereo := 2
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"}, MaxChannels: &maxStereo,
	}
	request.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}

	result := PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("delivery-specific stereo ceiling did not exclude original_http: %#v", result)
	}

	original := request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	original.MaxChannels = nil
	original.AudioDecodeCodecs = []string{"opus"}
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = original
	result = PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("delivery-specific audio codec list did not exclude original_http: %#v", result)
	}

	original.AudioDecodeCodecs = nil
	original.AudioPassthroughCodecs = []string{"aac"}
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = original
	result = PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("passthrough-only delivery must not claim AAC decode support: %#v", result)
	}
}

func TestPlanPlaybackV3AppliesDeliverySpecificHDRDetails(t *testing.T) {
	file := detailedFixtureFileV3()
	request := validStartRequestV3()
	hdr := &HDRCapabilitiesV3{HDR10: true}
	request.Capabilities.HDRDetails = hdr
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	request.ClientPlaybackContext.Output.HDRDetails = hdr
	request.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mkv"}, VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"}, HDRDetails: &HDRCapabilitiesV3{},
	}
	request.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"}, HDRDetails: hdr,
	}

	result := PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("delivery-specific HDR override did not exclude original_http: %#v", result)
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

// A source whose RPU ffmpeg cannot parse must lose the strip in the plan, not
// at the transport. The plan is what promises HDR10, and the durable session's
// RemuxDVMode — re-read by every later restart, seek and audio switch — is
// derived from it, so a plan that still names server_dv7_to_hdr10 puts the
// hanging filter back no matter what the start path did with it. With no
// tone-map recipe in the tree there is no route left for an HDR10-only client,
// and the terminal has to name the real cause.
func TestPlanPlaybackV3AbandonsStripForAnUnstrippableSource(t *testing.T) {
	file := unstrippableProfile7FixtureV3()
	req := hdr10OnlyProfile7RequestV3()
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})
	input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry}

	// Baseline: with a parseable RPU this is the validated HDR10 remux.
	if healthy := PlanPlaybackV3(input); healthy.Plan == nil || len(healthy.Plan.Transformations) != 1 || healthy.Plan.Transformations[0].Name != "server_dv7_to_hdr10" {
		t.Fatalf("the strip route regressed for a healthy source: %#v", healthy)
	}

	input.DVRPUStrippable = func() bool { return false }
	result := PlanPlaybackV3(input)
	if result.Plan != nil {
		t.Fatalf("a source that cannot be stripped was still planned onto a route: %#v", result.Plan)
	}
	if result.Terminal == nil || result.Terminal.Reason != "dv_conversion_unsupported" {
		t.Fatalf("terminal = %#v, want the Dolby Vision cause rather than a generic HDR message", result.Terminal)
	}
}

// The strip is a server capability, not the only one: a client that can do the
// conversion itself must still get its route, with the reason the server route
// was dropped attached.
func TestPlanPlaybackV3KeepsTheClientTransformWhenTheSourceCannotBeStripped(t *testing.T) {
	file := unstrippableProfile7FixtureV3()
	req := hdr10OnlyProfile7RequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureClientVideoTransforms)
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Transformations = []TransformationV3{{Name: ClientDV7ToHDR10V3, Executor: "client", RecipeVersion: ClientDVTransformVersionV3}}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry,
		DVRPUStrippable: func() bool { return false },
	})
	if result.Plan == nil {
		t.Fatalf("terminal = %#v, want the client-side transformation route", result.Terminal)
	}
	for _, transformation := range result.Plan.Transformations {
		if transformation.Name == "server_dv7_to_hdr10" {
			t.Fatalf("the unusable server strip survived: %#v", result.Plan.Transformations)
		}
	}
	if !hasDegradationWarningV3(result.Plan.DegradationWarnings, "dolby_vision_strip_unsupported_by_source") {
		t.Fatalf("the client was not told why the server route was dropped: %#v", result.Plan.DegradationWarnings)
	}
}

func unstrippableProfile7FixtureV3() *models.MediaFile {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 6
	file.VideoTracks[0].DVELPresent = false
	file.VideoTracks[0].DVEnhancementLayer = ""
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	return file
}

func hdr10OnlyProfile7RequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	return req
}

// The probe is expensive, so it must sit behind every cheap gate: a source
// nobody would strip anyway must never spawn one.
func TestPlanPlaybackV3DoesNotProbeWhenNoStripIsOnTheTable(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].BitDepth = 8
	req := validStartRequestV3()
	probed := false
	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
		DVRPUStrippable: func() bool { probed = true; return true },
	})
	if result.Plan == nil {
		t.Fatalf("terminal = %#v", result.Terminal)
	}
	if probed {
		t.Fatal("an ordinary SDR source paid for a Dolby Vision RPU probe")
	}
}

// availableQualities must publish the transcode ladder below the source height
// plus the source-preserving "original" entry, and shrink to "original" alone
// when the transcode route cannot execute.
func TestPlanPlaybackV3PublishesAvailableQualities(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].BitDepth = 8
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	labels := make([]string, 0, len(result.Plan.AvailableQualities))
	for _, quality := range result.Plan.AvailableQualities {
		labels = append(labels, quality.Label)
	}
	if len(labels) != 4 || labels[0] != "original" || labels[1] != "1080p" || labels[2] != "720p" || labels[3] != "480p" {
		t.Fatalf("labels = %v", labels)
	}
	if !result.Plan.AvailableQualities[0].PreservesSource || result.Plan.AvailableQualities[0].Height != 2160 {
		t.Fatalf("original entry = %#v", result.Plan.AvailableQualities[0])
	}
	if result.Plan.AvailableQualities[2].BitrateKbps != 2_000 {
		t.Fatalf("720p bitrate = %#v", result.Plan.AvailableQualities[2])
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

func TestAvailableQualitiesV3UnknownSourceHeightPublishesNoFixedRungs(t *testing.T) {
	request := validStartRequestV3()
	qualities := availableQualitiesV3(PlannerInputV3{
		Request:  request,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
	}, SourceDescriptorV3{VideoCodec: "h264", BitrateKbps: 8_000})
	if len(qualities) != 1 || qualities[0].Label != QualityOriginalV3 || !qualities[0].PreservesSource {
		t.Fatalf("unknown-height qualities = %#v, want original only", qualities)
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

func TestPlanPlaybackV3NonDefaultAudioSelectionCannotUseOriginalHTTP(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.AudioTracks = []models.AudioTrack{
		{Codec: "aac", Channels: 2, Layout: "stereo", Default: true},
		{Codec: "aac", Channels: 2, Layout: "stereo"},
	}
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 1, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.PlayMethod != PlayRemux || result.TranscodeAudio {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.SelectedTracks.Audio == nil || result.Plan.SelectedTracks.Audio.Index == nil || *result.Plan.SelectedTracks.Audio.Index != 1 {
		t.Fatalf("selected audio = %#v", result.Plan.SelectedTracks.Audio)
	}
}

func TestPlanPlaybackV3HLSAudioConversionHonorsChannelCeiling(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.CodecAudio = "dts"
	file.AudioChannels = 8
	file.AudioTracks[0] = models.AudioTrack{Codec: "dts", Channels: 8, Layout: "7.1", Default: true}
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassProgressiveV3)
	maxChannels := 2
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.MaxChannels = &maxChannels
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || !result.TranscodeAudio || result.TargetAudioChannels != 2 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.EffectiveRecipe.AudioChannels == nil || *result.Plan.EffectiveRecipe.AudioChannels != 2 || result.Plan.EffectiveRecipe.AudioLayout != "stereo" {
		t.Fatalf("AAC recipe = %#v", result.Plan.EffectiveRecipe)
	}
}

// An audio-only source whose codec the client cannot decode must take the
// progressive audio_to_aac conversion route, and keep an accurate retryable
// terminal when the toolchain is missing.
func TestPlanPlaybackV3AudioOnlyConvertsUnsupportedCodec(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.CodecAudio = "flac"
	file.AudioTracks[0].Codec = "flac"
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetAudioCodec != "aac" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if result.Plan.DecisionReason != "audio_adaptation" || len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != "audio_to_aac" {
		t.Fatalf("plan = %#v", result.Plan)
	}

	noToolchain := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "1"}})
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: noToolchain})
	if result.Terminal == nil || result.Terminal.Reason != "audio_conversion_unsupported" || !result.Terminal.Retryable {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
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

// Loop prevention applies to audio-only routes exactly like video routes: an
// attempted original route falls to the conversion remux, and an exhausted
// route family terminals.
func TestPlanPlaybackV3AudioOnlyHonorsAttemptedKeys(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}
	input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()}

	first := PlanPlaybackV3(input)
	if first.Plan == nil || first.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("first = %s", ExplainPlannerResultV3(first))
	}
	input.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	second := PlanPlaybackV3(input)
	if second.Plan == nil || second.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("second = %s", ExplainPlannerResultV3(second))
	}
	input.AttemptedKeys = append(input.AttemptedKeys, second.Plan.PlanAttemptKey)
	third := PlanPlaybackV3(input)
	if third.Terminal == nil || third.Terminal.Reason != "adaptation_exhausted" {
		t.Fatalf("third = %s", ExplainPlannerResultV3(third))
	}
}
