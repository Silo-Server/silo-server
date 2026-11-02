package playback

import (
	"fmt"
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

func TestPlanPlaybackV3DV8BaseLayerClaimUsesHLGForCompat4(t *testing.T) {
	file := dv8BaseLayerFixtureV3(4)
	result := planDV8V3(t, file, dv8BaseLayerRequestV3(&HDRCapabilitiesV3{HDR10: true, HLG: true}))
	if result.Plan == nil || result.Plan.DecisionReason != decisionReasonClientDV8BaseLayerV3 || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeHLGV3 {
		t.Fatalf("expected an HLG base-layer plan: %s", ExplainPlannerResultV3(result))
	}
}

// The narrow base-layer claim fails closed: only a proven single-layer
// Profile 8 base with compat id 1 (HDR10) or 4 (HLG, on an HLG output) takes
// it, and only on original_http.
func TestPlanPlaybackV3DV8BaseLayerClaimRefusals(t *testing.T) {
	hdr10 := &HDRCapabilitiesV3{HDR10: true}
	hdr10HLG := &HDRCapabilitiesV3{HDR10: true, HLG: true}
	type refusal struct {
		name       string
		compatID   int
		output     *HDRCapabilitiesV3
		mutateFile func(*models.VideoTrack)
		mutateReq  func(*StartRequestV3)
	}
	tests := []refusal{
		// Without HLG on the output the HLG base cannot be promised.
		{name: "compat 4 without HLG output", compatID: 4, output: hdr10},
		{name: "profile 5", compatID: 1, output: hdr10, mutateFile: func(track *models.VideoTrack) { track.DVProfile = 5 }},
		{name: "enhancement layer present", compatID: 1, output: hdr10, mutateFile: func(track *models.VideoTrack) { track.DVELPresent = true }},
		{name: "unproven compatibility id", compatID: 1, output: hdr10, mutateFile: func(track *models.VideoTrack) { track.DVBLCompatIDPresent = false }},
		{name: "absent base layer", compatID: 1, output: hdr10, mutateFile: func(track *models.VideoTrack) { track.DVBLPresent = false }},
		{name: "claim on packaged deliveries only", compatID: 1, output: hdr10, mutateReq: func(req *StartRequestV3) {
			direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			direct.Enabled = false
			req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
			for _, class := range []string{DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
				packaged := req.ClientPlaybackContext.Deliveries[class]
				packaged.ValidatedClaims = append(packaged.ValidatedClaims, ClaimClientDV8BaseLayerFallbackV3)
				req.ClientPlaybackContext.Deliveries[class] = packaged
			}
		}},
	}
	for _, compatID := range []int{0, 3, 5, 6, 7} {
		tests = append(tests, refusal{name: fmt.Sprintf("unsupported compat id %d", compatID), compatID: compatID, output: hdr10HLG})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := dv8BaseLayerFixtureV3(tt.compatID)
			if tt.mutateFile != nil {
				tt.mutateFile(&file.VideoTracks[0])
			}
			req := dv8BaseLayerRequestV3(tt.output)
			if tt.mutateReq != nil {
				tt.mutateReq(&req)
			}
			result := planDV8V3(t, file, req)
			if result.Plan != nil && result.Plan.DecisionReason == decisionReasonClientDV8BaseLayerV3 {
				t.Fatalf("must not take the base-layer route: %#v", result.Plan)
			}
		})
	}
}

func TestPlanPlaybackV3NativeDolbyVisionWinsOverBaseLayerClaim(t *testing.T) {
	file := dv8BaseLayerFixtureV3(1)
	result := planDV8V3(t, file, dv8BaseLayerRequestV3(&HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}))
	if result.Plan == nil || result.Plan.DecisionReason != "validated_original_playback" || !result.Plan.Claims.Video.DolbyVision || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeDolbyVisionV3 {
		t.Fatalf("native Dolby Vision output must plan native playback: %s", ExplainPlannerResultV3(result))
	}
}

// hdr10NativeRequestV3 is a client with an exact-tier HEVC Main 10 decoder
// and decoder-scoped HDR10 whose original_http delivery takes the 4K HDR10
// fixture as-is, so only the output and display facts decide native HDR10.
func hdr10NativeRequestV3(output *HDRCapabilitiesV3, display *OutputDisplayV3) StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = output
	req.ClientPlaybackContext.Output.Display = display
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Containers = []string{"mkv"}
	direct.VideoCodecs = []string{"hevc"}
	direct.AudioDecodeCodecs = []string{"aac"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	return req
}

func planNativeHDR10V3(t *testing.T, req StartRequestV3) (PlannerResultV3, bool) {
	t.Helper()
	result := planDV8V3(t, detailedFixtureFileV3(), req) // plain 4K HDR10 source
	return result, result.Plan != nil && result.Plan.Delivery == DeliveryOriginalHTTPV3 && result.Plan.Claims.Video.HDR10
}

func TestPlanPlaybackV3UnknownDisplayEvidenceFailsClosedForNativeHDR(t *testing.T) {
	req := hdr10NativeRequestV3(&HDRCapabilitiesV3{HDR10: true}, &OutputDisplayV3{HDREvidence: OutputHDREvidenceUnknownV3})
	if result, native := planNativeHDR10V3(t, req); native {
		t.Fatalf("unknown display evidence must not earn a native HDR10 claim: %#v", result.Plan)
	}

	// The same request with exact evidence and matching panel facts keeps
	// the native route.
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{HDR10: true}}
	if result, native := planNativeHDR10V3(t, req); !native {
		t.Fatalf("exact display evidence should keep native HDR10 direct play: %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3DisplayEvidenceDisablesDeviceLevelFallback(t *testing.T) {
	// Decoder-scoped HDR10 but no output HDR facts at all, with the display
	// evidence field present: a decoder fact must not become a native claim.
	req := hdr10NativeRequestV3(nil, &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{}})
	if result, native := planNativeHDR10V3(t, req); native {
		t.Fatalf("device-level HDR must not be promoted to native output once display evidence is reported: %#v", result.Plan)
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

func TestPlanPlaybackV3ExactSDRDisplayNarrowsContradictoryOutputHDR(t *testing.T) {
	req := hdr10NativeRequestV3(&HDRCapabilitiesV3{HDR10: true}, &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{}})
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("a contradiction between exact display and output hdr_details must be rejected at validation")
	}
	if result, native := planNativeHDR10V3(t, req); native {
		t.Fatalf("an exact SDR panel must not authorize native HDR10 through hdr_details: %#v", result.Plan)
	}
}

func TestNativeOutputHDRV3IntersectsExactPanelBounds(t *testing.T) {
	req := validStartRequestV3()
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{
		HDR10: true, HDR10MaxWidth: 3840, HDR10MaxHeight: 2160, HDR10MaxFrameRate: 60, HDR10MaxBitrateKbps: 100_000,
		DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 9}},
	}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{
		HDR10: true, HDR10MaxWidth: 1920, HDR10MaxHeight: 1080, HDR10MaxFrameRate: 30,
		DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6}},
	}}
	got := nativeOutputHDRV3(req)
	if got == nil || got.HDR10MaxWidth != 1920 || got.HDR10MaxHeight != 1080 || got.HDR10MaxFrameRate != 30 || got.HDR10MaxBitrateKbps != 100_000 {
		t.Fatalf("hdr10 bounds were not narrowed to the panel: %#v", got)
	}
	if len(got.DolbyVisionProfileLevels) != 1 || got.DolbyVisionProfileLevels[0].MaxLevel != 6 {
		t.Fatalf("dolby vision level was not narrowed to the panel: %#v", got.DolbyVisionProfileLevels)
	}
}

func TestStartRequestV3RejectsOutputBoundsLooserThanExactPanel(t *testing.T) {
	req := validStartRequestV3()
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 3840}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 1920}}
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("an output hdr10 ceiling looser than the exact panel must be rejected")
	}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 9}}}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6}}}}
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("an output dolby vision level above the exact panel must be rejected")
	}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 1920}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 3840}}
	if _, err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("a tighter output ceiling is the decoder narrowing the panel and must validate: %v", err)
	}
}

func TestPlanPlaybackV3BaseLayerUsedWhenDeliveryRefusesNativeDolbyVision(t *testing.T) {
	file := dv8BaseLayerFixtureV3(1)
	// Output carries native DV, but the original_http executor only knows
	// how to present HDR10 (per-delivery hdr_details override).
	output := &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	req := dv8BaseLayerRequestV3(output)
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct

	result := planDV8V3(t, file, req)
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.Plan.DecisionReason != decisionReasonClientDV8BaseLayerV3 || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeHDR10V3 {
		t.Fatalf("expected the base-layer route when the delivery cannot carry native DV: %s", ExplainPlannerResultV3(result))
	}
}

func TestNativeOutputHDRV3IntersectsDolbyVisionCompatibilityIDs(t *testing.T) {
	req := validStartRequestV3()
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{
		HDR10: true, DolbyVisionProfiles: []int{8},
		DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 9, BLCompatibilityIDs: []int{1, 4}}},
	}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{
		HDR10: true, DolbyVisionProfiles: []int{8},
		DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 9, BLCompatibilityIDs: []int{1}}},
	}}
	got := nativeOutputHDRV3(req)
	if got == nil || len(got.DolbyVisionProfileLevels) != 1 || len(got.DolbyVisionProfileLevels[0].BLCompatibilityIDs) != 1 || got.DolbyVisionProfileLevels[0].BLCompatibilityIDs[0] != 1 {
		t.Fatalf("compatibility ids were not intersected with the panel: %#v", got)
	}
	// A panel-only bounded record survives a legacy profile-only output.
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
	got = nativeOutputHDRV3(req)
	if got == nil || len(got.DolbyVisionProfileLevels) != 1 || got.DolbyVisionProfileLevels[0].MaxLevel != 9 {
		t.Fatalf("panel-only profile bound was dropped: %#v", got)
	}
}

func TestPlanPlaybackV3ExactPanelHDR10CeilingGatesNativeRouteWithoutDeliveryBounds(t *testing.T) {
	// The delivery omits its own bounds.
	req := hdr10NativeRequestV3(&HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 1920, HDR10MaxHeight: 1080},
		&OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 1920, HDR10MaxHeight: 1080}})
	if result, native := planNativeHDR10V3(t, req); native {
		t.Fatalf("a 4K HDR10 source must not be planned natively on a panel capped at 1080p HDR10: %#v", result.Plan)
	}
}

func TestPlanPlaybackV3ExactPanelHDR10CeilingGatesDV7Strip(t *testing.T) {
	file := unstrippableProfile7FixtureV3() // 4K Profile 7
	req := hdr10OnlyProfile7RequestV3()
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true, HDR10MaxWidth: 1920, HDR10MaxHeight: 1080}
	req.ClientPlaybackContext.Output.Display = &OutputDisplayV3{HDREvidence: OutputHDREvidenceExactV3, HDRTypes: req.ClientPlaybackContext.Output.HDRDetails}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", Available: true}})
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if result.Plan != nil {
		for _, transformation := range result.Plan.Transformations {
			if transformation.Name == "server_dv7_to_hdr10" {
				t.Fatalf("a 4K HDR10 strip must not be promised on a panel capped at 1080p HDR10: %#v", result.Plan)
			}
		}
	}
}
