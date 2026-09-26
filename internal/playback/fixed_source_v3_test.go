package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestFixedSourceAllowsIndependentViewerQuality(t *testing.T) {
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: TransformationHDRToSDRToneMapV3, RecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, Available: true},
	})
	capabilities := tonemap.Capabilities{
		{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemapx", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
	}
	for _, test := range []struct {
		name         string
		quality      string
		sourceSDR    bool
		directHDR    bool
		settings     PlannerSettingsV3
		wantHeight   int
		wantTerminal string
	}{
		{
			name: "4K HDR direct play with transcoding disabled", quality: "original", directHDR: true, wantHeight: 2160,
		},
		{
			name: "4K SDR transcode", quality: QualityRung2160pMediumV3, sourceSDR: true, wantHeight: 2160,
			settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		},
		{
			name: "4K tone-mapped output", quality: QualityRung2160pMediumV3, wantHeight: 2160,
			settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true},
		},
		{
			name: "1080p tone-mapped output from the same 4K file", quality: QualityRung1080pMediumV3, wantHeight: 1080,
			settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true},
		},
		{
			name: "4K transcode disabled", quality: QualityRung1080pMediumV3, sourceSDR: true,
			settings: PlannerSettingsV3{TranscodeEnabled: true}, wantTerminal: "no_alternate_version",
		},
		{
			name: "tone mapping disabled", quality: QualityRung2160pMediumV3,
			settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, wantTerminal: TerminalHDRTranscodeUnsupportedV3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := detailedFixtureFileV3()
			file.VideoTracks[0].ColorPrimaries = "bt2020"
			file.VideoTracks[0].ColorTransfer = "smpte2084"
			file.VideoTracks[0].ColorSpace = "bt2020nc"
			if test.sourceSDR {
				file.VideoTracks[0].VideoRange = "SDR"
				file.VideoTracks[0].VideoRangeType = "SDR"
				file.VideoTracks[0].ColorPrimaries = "bt709"
				file.VideoTracks[0].ColorTransfer = "bt709"
				file.VideoTracks[0].ColorSpace = "bt709"
			}
			request := validStartRequestV3()
			request.AllowAlternateVersions = new(false)
			request.QualityPreference = test.quality
			if test.directHDR {
				request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
				request.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
				request.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
			}
			result := PlanPlaybackV3(PlannerInputV3{
				Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: test.settings, Registry: registry, ToneMapCapabilities: capabilities,
			})
			if test.wantTerminal != "" {
				if result.Terminal == nil || result.Terminal.Reason != test.wantTerminal {
					t.Fatalf("result = %s; want refusal %q", ExplainPlannerResultV3(result), test.wantTerminal)
				}
				return
			}
			if result.Plan == nil || result.Terminal != nil {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if result.Plan.EffectiveMediaFileID != file.ID || result.Plan.RequestedMediaFileID != file.ID {
				t.Fatalf("viewer changed source file: %#v", result.Plan)
			}
			if result.Plan.EffectiveRecipe.Height == nil || *result.Plan.EffectiveRecipe.Height != test.wantHeight {
				t.Fatalf("recipe = %#v; want height %d", result.Plan.EffectiveRecipe, test.wantHeight)
			}
			if test.directHDR {
				if result.PlayMethod != PlayDirect || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeHDR10V3 {
					t.Fatalf("capable viewer lost direct HDR: %s", ExplainPlannerResultV3(result))
				}
			} else if result.PlayMethod != PlayTranscode || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeSDRV3 {
				t.Fatalf("viewer adaptation missing: %s", ExplainPlannerResultV3(result))
			} else if !test.sourceSDR && result.ToneMapMode != tonemap.ModeSoftware {
				t.Fatalf("tone map mode = %q", result.ToneMapMode)
			}
		})
	}
}
