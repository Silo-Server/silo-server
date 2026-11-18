package playback

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A single-layer Profile 8.1 source on a client whose active output carries
// HDR10 but not Dolby Vision, with the narrow base-layer claim.
func dv8BaseLayerFixtureV3(compatID int) *models.MediaFile {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = compatID
	file.VideoTracks[0].DVConfigPresent = true
	file.VideoTracks[0].DVBLCompatIDPresent = true
	file.VideoTracks[0].DVBLPresent = true
	file.VideoTracks[0].VideoRangeType = "DOVI"
	return file
}

func dv8BaseLayerRequestV3(output *HDRCapabilitiesV3) StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	// Decoder-scoped facts may still list Dolby Vision; the output is what
	// decides native presentation.
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, HLG: true, DolbyVisionProfiles: []int{5, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = output
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: output}
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Containers = []string{"mkv"}
	direct.VideoCodecs = []string{"hevc"}
	direct.AudioDecodeCodecs = []string{"aac"}
	direct.HDRDetails = output
	direct.ValidatedClaims = []string{ClaimClientDV8BaseLayerFallbackV3}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	return req
}

func planDV8V3(t *testing.T, file *models.MediaFile, req StartRequestV3) PlannerResultV3 {
	t.Helper()
	return PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: testTransformationRegistryV3(),
	})
}

func TestPlanPlaybackV3DV8BaseLayerClaimPlaysOriginalAsHDR10OnNonDVOutput(t *testing.T) {
	file := dv8BaseLayerFixtureV3(1)
	req := dv8BaseLayerRequestV3(&HDRCapabilitiesV3{HDR10: true})
	result := planDV8V3(t, file, req)
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect {
		t.Fatalf("expected original_http direct play: %s", ExplainPlannerResultV3(result))
	}
	plan := result.Plan
	if plan.DecisionReason != decisionReasonClientDV8BaseLayerV3 {
		t.Fatalf("decision reason = %q", plan.DecisionReason)
	}
	if plan.EffectiveRecipe.DynamicRange != DynamicRangeHDR10V3 {
		t.Fatalf("effective range = %q, want hdr10", plan.EffectiveRecipe.DynamicRange)
	}
	if plan.Claims.Video.DolbyVision || !plan.Claims.Video.HDR10 || plan.Claims.Video.DolbyVisionReason != "base_layer_compatible_hevc" {
		t.Fatalf("claims = %#v", plan.Claims.Video)
	}
	if len(plan.Transformations) != 0 {
		t.Fatalf("base-layer route must not be a transformation: %#v", plan.Transformations)
	}
	found := false
	for _, warning := range plan.DegradationWarnings {
		if warning.Code == "dolby_vision_base_layer_only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing dolby_vision_base_layer_only warning: %#v", plan.DegradationWarnings)
	}
}

// dv10BaseLayerFixtureV3 is the AV1 counterpart of dv8BaseLayerFixtureV3: a
// single-layer Profile 10 source whose compatibility id names its base range.
func dv10BaseLayerFixtureV3(compatID int) *models.MediaFile {
	file := dv8BaseLayerFixtureV3(compatID)
	file.CodecVideo = "av1"
	file.VideoTracks[0].Codec = "av1"
	file.VideoTracks[0].Profile = "Main"
	file.VideoTracks[0].Level = 0
	file.VideoTracks[0].DVProfile = 10
	return file
}

// dv10BaseLayerRequestV3 is an Android-style request whose output carries
// HDR10 but not Dolby Vision, with a hardware AV1 Main 10 decoder and the
// given original_http claims.
func dv10BaseLayerRequestV3(claims ...string) StartRequestV3 {
	output := &HDRCapabilitiesV3{HDR10: true, HLG: true}
	req := dv8BaseLayerRequestV3(output)
	req.Capabilities.CodecsVideo = append(req.Capabilities.CodecsVideo, "av1")
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "av1", Profiles: []string{"main"}, BitDepths: []int{8, 10}, MaxWidth: 7680, MaxHeight: 4320, MaxFrameRate: 30, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = output
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.VideoCodecs = []string{"hevc", "av1"}
	direct.ValidatedClaims = claims
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	return req
}

func TestPlanPlaybackV3DV10BaseLayerClaim(t *testing.T) {
	cases := []struct {
		name     string
		file     *models.MediaFile
		claims   []string
		profile  int
		wantBase bool
	}{
		{name: "profile 10.1 plays original as HDR10", file: dv10BaseLayerFixtureV3(1), claims: []string{ClaimClientDV10BaseLayerFallbackV3}, wantBase: true},
		// The Profile 8 claim promises an HEVC base-layer decoder, not AV1.
		{name: "profile 8 claim does not cover profile 10", file: dv10BaseLayerFixtureV3(1), claims: []string{ClaimClientDV8BaseLayerFallbackV3}},
		{name: "profile 10.0 has no compatible base layer", file: dv10BaseLayerFixtureV3(0), claims: []string{ClaimClientDV10BaseLayerFallbackV3}},
		{name: "profile 10 claim does not cover profile 8", file: dv8BaseLayerFixtureV3(1), claims: []string{ClaimClientDV10BaseLayerFallbackV3}},
		// Profile 10 is only defined for AV1; an HEVC stream tagged 10 fails closed.
		{name: "profile 10 needs an AV1 stream", file: dv8BaseLayerFixtureV3(1), claims: []string{ClaimClientDV10BaseLayerFallbackV3}, profile: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.profile != 0 {
				tc.file.VideoTracks[0].DVProfile = tc.profile
			}
			req := dv10BaseLayerRequestV3(tc.claims...)
			req.Capabilities.VideoDecode = append(req.Capabilities.VideoDecode, hevcMain10DecodeV3()...)
			result := planDV8V3(t, tc.file, req)
			gotBase := result.Plan != nil && result.Plan.Delivery == DeliveryOriginalHTTPV3 &&
				(result.Plan.DecisionReason == decisionReasonClientDV10BaseLayerV3 || result.Plan.DecisionReason == decisionReasonClientDV8BaseLayerV3)
			if gotBase != tc.wantBase {
				t.Fatalf("base-layer original route = %v, want %v: %s", gotBase, tc.wantBase, ExplainPlannerResultV3(result))
			}
			if !tc.wantBase {
				return
			}
			plan := result.Plan
			if result.PlayMethod != PlayDirect || plan.DecisionReason != decisionReasonClientDV10BaseLayerV3 || plan.EffectiveRecipe.DynamicRange != DynamicRangeHDR10V3 {
				t.Fatalf("plan = %s / %q / %q", result.PlayMethod, plan.DecisionReason, plan.EffectiveRecipe.DynamicRange)
			}
			if plan.Claims.Video.DolbyVision || !plan.Claims.Video.HDR10 || plan.Claims.Video.DolbyVisionReason != "base_layer_compatible_av1" {
				t.Fatalf("claims = %#v", plan.Claims.Video)
			}
			if len(plan.Transformations) != 0 || !slices.ContainsFunc(plan.DegradationWarnings, func(w DegradationWarningV3) bool { return w.Code == "dolby_vision_base_layer_only" }) {
				t.Fatalf("transformations = %#v, warnings = %#v", plan.Transformations, plan.DegradationWarnings)
			}
		})
	}
}

func TestStartRequestV3RejectsInvalidDisplayEvidence(t *testing.T) {
	req := validStartRequestV3()
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: "maybe"}
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("expected an invalid hdr_evidence value to be rejected")
	}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: " Exact "}
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("expected a case-insensitive evidence value to validate: %v", err)
	}
	if req.ClientPlaybackContext.Output.Display.HDREvidence != OutputHDREvidenceExactV3 {
		t.Fatalf("evidence was not normalized: %q", req.ClientPlaybackContext.Output.Display.HDREvidence)
	}
}
