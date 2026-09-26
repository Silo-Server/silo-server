package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// A Profile 8.1 source with an HDR10-compatible base layer, on an output that
// advertises both native Dolby Vision Profile 8 and HDR10.
func dv81NativeAndHDR10FixtureV3() (*models.MediaFile, StartRequestV3) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVLevel = 6
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithHDR10"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	return file, req
}

// replanChainV3 plans, marks each returned plan attempted as a failed client
// would, and replans until the planner returns a terminal.
func replanChainV3(t *testing.T, input PlannerInputV3) ([]PlanV3, *TerminalV3) {
	t.Helper()
	var plans []PlanV3
	for range 16 {
		result := PlanPlaybackV3(input)
		if result.Plan == nil {
			return plans, result.Terminal
		}
		for _, previous := range plans {
			if previous.PlanAttemptKey == result.Plan.PlanAttemptKey {
				t.Fatalf("attempted plan %s was offered again", previous.PlanAttemptKey)
			}
		}
		plans = append(plans, *result.Plan)
		input.AttemptedKeys = append(input.AttemptedKeys, result.Plan.PlanAttemptKey)
	}
	t.Fatalf("replan chain did not terminate: %d plans", len(plans))
	return nil, nil
}

func planStripsDVToHDR10V3(plan PlanV3) bool {
	for _, transformation := range plan.Transformations {
		if transformation.Name == TransformationServerDV7HDR10V3 {
			return true
		}
	}
	return false
}

func dvStripFallbackRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: true},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
}

// Once every native-DV route has failed on this output, the validated HDR10
// strip must be offered before the planner gives up.
func TestPlanPlaybackV3DV81FallsBackToHDR10StripAfterNativeDVFails(t *testing.T) {
	file, req := dv81NativeAndHDR10FixtureV3()
	plans, terminal := replanChainV3(t, PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: dvStripFallbackRegistryV3(),
	})

	var native, strip []PlanV3
	for _, plan := range plans {
		if planStripsDVToHDR10V3(plan) {
			strip = append(strip, plan)
			continue
		}
		if len(strip) > 0 {
			t.Fatalf("native-DV plan %s offered after the HDR10 strip", plan.Delivery)
		}
		native = append(native, plan)
	}
	if len(native) == 0 || !native[0].Claims.Video.DolbyVision {
		t.Fatalf("first plan should preserve native Dolby Vision: %#v", plans)
	}
	if len(strip) != 2 || strip[0].Delivery != DeliveryRemuxProgressiveV3 || strip[1].Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("strip plans = %d, want progressive then HLS remux", len(strip))
	}
	for _, plan := range strip {
		if plan.EffectiveRecipe.DynamicRange != DynamicRangeHDR10V3 || !plan.Claims.Video.HDR10 || plan.Claims.Video.DolbyVision {
			t.Fatalf("%s strip range=%s claims=%#v, want HDR10", plan.Delivery, plan.EffectiveRecipe.DynamicRange, plan.Claims.Video)
		}
		if !hasDegradationWarningV3(plan.DegradationWarnings, "dolby_vision_removed") {
			t.Fatalf("%s strip is missing the dolby_vision_removed warning: %#v", plan.Delivery, plan.DegradationWarnings)
		}
		for _, failed := range native {
			if plan.PlanID == failed.PlanID {
				t.Fatalf("strip plan reused the native-DV plan id %s", plan.PlanID)
			}
		}
	}
	if terminal == nil || terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("terminal = %#v, want hdr_transcode_unsupported once every recipe is exhausted", terminal)
	}
}

// With a tone-map route installed, the HDR10 strip still outranks an SDR
// transcode: it keeps the source's base layer and its HDR presentation.
func TestPlanPlaybackV3DV81PrefersHDR10StripOverToneMap(t *testing.T) {
	file, req := dv81NativeAndHDR10FixtureV3()
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationHDRToSDRToneMapV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, Available: true},
	})
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	plans, _ := replanChainV3(t, PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true},
		Registry: registry, ToneMapCapabilities: capabilities,
	})

	firstStrip, firstTranscode := -1, -1
	for i, plan := range plans {
		if firstStrip < 0 && planStripsDVToHDR10V3(plan) {
			firstStrip = i
		}
		if firstTranscode < 0 && plan.Delivery == DeliveryTranscodeHLSV3 {
			firstTranscode = i
		}
	}
	if firstStrip < 0 || firstTranscode >= 0 && firstTranscode < firstStrip {
		t.Fatalf("strip index %d, transcode index %d: the HDR10 strip must precede any transcode", firstStrip, firstTranscode)
	}
}

func TestPlanPlaybackV3DV81SkipsHDR10StripWhenItCannotApply(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PlannerInputV3)
	}{
		{name: "source RPU cannot be stripped", mutate: func(input *PlannerInputV3) {
			input.DVRPUStrippable = func() bool { return false }
		}},
		{name: "output lacks HDR10", mutate: func(input *PlannerInputV3) {
			input.Request.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: []int{8}}
			input.Request.ClientPlaybackContext.Output.HDRDetails = input.Request.Capabilities.HDRDetails
		}},
		{name: "strip recipe not installed", mutate: func(input *PlannerInputV3) {
			input.Registry = NewTransformationRegistryV3([]TransformationSpecV3{
				{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, req := dv81NativeAndHDR10FixtureV3()
			input := PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
				Registry: dvStripFallbackRegistryV3(),
			}
			test.mutate(&input)
			plans, terminal := replanChainV3(t, input)
			if len(plans) == 0 || !plans[0].Claims.Video.DolbyVision {
				t.Fatalf("first plan should still preserve native Dolby Vision: %#v", plans)
			}
			for _, plan := range plans {
				if planStripsDVToHDR10V3(plan) {
					t.Fatalf("%s offered the HDR10 strip", plan.Delivery)
				}
			}
			if terminal == nil {
				t.Fatal("chain ended without a terminal")
			}
		})
	}
}

// The strip fallback stays scoped to the executor pool that can run it: when
// only HLS nodes carry the strip, the progressive delivery offers no strip plan.
func TestPlanPlaybackV3DV81StripFallbackRespectsDeliveryExecutors(t *testing.T) {
	file, req := dv81NativeAndHDR10FixtureV3()
	withoutStrip := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
	plans, _ := replanChainV3(t, PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings:                 PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:                 dvStripFallbackRegistryV3(),
		ProgressiveRemuxRegistry: func() *TransformationRegistryV3 { return withoutStrip },
	})
	var strip []PlanV3
	for _, plan := range plans {
		if planStripsDVToHDR10V3(plan) {
			strip = append(strip, plan)
		}
	}
	if len(strip) != 1 || strip[0].Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("strip plans = %d, want only the HLS remux", len(strip))
	}
}
