package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: TransformationServerDV7HDR10RecipeVersionV3, Available: true},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
}

// HLS cannot carry TrueHD without an AAC conversion. When no HLS executor
// offers one, that route is skipped rather than ending planning, so the
// progressive HDR10 strip, which copies TrueHD, is still reached. The
// conversion terminal is reported only once every remux recipe is exhausted.
func TestPlanPlaybackV3DV81StripFallbackSurvivesMissingHLSAudioConversion(t *testing.T) {
	file, req := dv81NativeAndHDR10FixtureV3()
	file.CodecAudio = "truehd"
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1", Default: true}
	req.Capabilities.CodecsAudio = []string{"truehd"}
	withoutAAC := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: TransformationServerDV7HDR10RecipeVersionV3, Available: true},
	})
	plans, terminal := replanChainV3(t, PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings:         PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:         dvStripFallbackRegistryV3(),
		HLSRemuxRegistry: func() *TransformationRegistryV3 { return withoutAAC },
	})
	var strip []PlanV3
	for _, plan := range plans {
		if plan.Delivery == DeliveryRemuxHLSV3 {
			t.Fatalf("HLS remux offered without an AAC executor: %#v", plan.Transformations)
		}
		if planStripsDVToHDR10V3(plan) {
			strip = append(strip, plan)
		}
	}
	if len(strip) != 1 || strip[0].Delivery != DeliveryRemuxProgressiveV3 || strip[0].EffectiveRecipe.AudioCodec != "truehd" {
		t.Fatalf("strip plans = %d, want one progressive remux copying TrueHD", len(strip))
	}
	if terminal == nil || terminal.Reason != TerminalAudioConversionUnsupportedV3 {
		t.Fatalf("terminal = %#v, want the audio conversion cause once every recipe is exhausted", terminal)
	}
}

// A single-track Profile 7 source goes to an output that takes Profile 8.1 as
// the server's HLS 8.1 conversion (fMP4 only) before the HDR10 strip. A
// dual-track source keeps its RPUs on the enhancement-layer track, which a
// remux does not copy, so it goes straight to the strip, as does a client that
// needs MPEG-TS copies. Without the strip, a failed 8.1 plan falls through to a
// transcode, not to a raw Profile 7 copy.
func TestPlanPlaybackV3Profile7ConvertsToDV81BeforeStrip(t *testing.T) {
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7DV81V3, RecipeVersion: TransformationServerDV7DV81RecipeVersionV3, Available: true},
		{Name: TransformationServerDV7HDR10V3, RecipeVersion: TransformationServerDV7HDR10RecipeVersionV3, Available: true},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
	stripless := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationServerDV7DV81V3, RecipeVersion: TransformationServerDV7DV81RecipeVersionV3, Available: true},
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
	for _, tc := range []struct {
		singleTrack, mpegts bool
		registry            *TransformationRegistryV3
	}{{true, false, registry}, {false, false, registry}, {true, false, stripless}, {true, true, registry}} {
		singleTrack := tc.singleTrack
		file, req := dv81NativeAndHDR10FixtureV3()
		track := &file.VideoTracks[0]
		track.DVProfile, track.DVBLCompatID, track.VideoRangeType = 7, 6, "DOVIWithEL"
		track.DVConfigPresent, track.DVRPUPresent, track.DVBLPresent, track.DVELPresent = true, true, true, singleTrack
		track.DVEnhancementLayer = string(EnhancementFELV3)
		hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
		// A delivery's own HDR details must accept the 8.1 output, not reject
		// the Profile 7 source.
		hls.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}}
		if tc.mpegts {
			hls.Features = append(hls.Features, FeatureHLSCopyMPEGTSV3) // the conversion is fMP4-only
		}
		req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls

		plans, _ := replanChainV3(t, PlannerInputV3{
			Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
			Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
			Registry: tc.registry,
		})
		dv81, firstStrip := 0, -1
		for i, plan := range plans {
			for _, tr := range plan.Transformations {
				if tr.Name != TransformationServerDV7DV81V3 {
					continue
				}
				dv81++
				if i != 0 || plan.Delivery != DeliveryRemuxHLSV3 || plan.EffectiveRecipe.SegmentContainer != "" ||
					!plan.Claims.Video.DolbyVision || plan.EffectiveRecipe.DynamicRange != DynamicRangeDolbyVisionV3 {
					t.Fatalf("8.1 plan %d = %s %s %#v", i, plan.Delivery, plan.EffectiveRecipe.SegmentContainer, plan.Claims.Video)
				}
			}
			if firstStrip < 0 && planStripsDVToHDR10V3(plan) {
				firstStrip = i
			}
			// Without the strip, the 8.1 plan must fall through to a
			// transcode, never to a copy of the raw Profile 7 stream.
			if plan.Delivery != DeliveryOriginalHTTPV3 && plan.Delivery != DeliveryTranscodeHLSV3 && len(plan.Transformations) == 0 {
				t.Fatalf("plan %d copies Profile 7 unconverted: %s", i, plan.Delivery)
			}
		}
		wantStrip := dv81
		if tc.registry == stripless {
			wantStrip = -1
		}
		want := 0
		if singleTrack && !tc.mpegts {
			want = 1
		}
		if dv81 != want || firstStrip != wantStrip {
			t.Fatalf("single track %v, mpegts %v: %d 8.1 plans, first strip at %d", singleTrack, tc.mpegts, dv81, firstStrip)
		}
	}
}
