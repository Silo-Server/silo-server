package playback

import (
	"fmt"
	"strings"
)

const (
	hlsInitFilename          = "init.mp4"
	hlsVideoManifestFilename = "stream.m3u8"
	hlsAudioRendition        = "audio"
	hlsVideoRendition        = "video"
	hlsAudioManifestFilename = "audio.m3u8"
	hlsVideoRangeSDR         = "SDR"
	hlsVideoRangePQ          = "PQ"
	hlsVideoRangeHLG         = "HLG"
	videoRangeDOVIWithHDR10  = "DOVIWithHDR10"
)

func nativeHLSPackagingV3(request StartRequestV3) string {
	return nativeHLSPackagingCandidatesV3(request)[0]
}

func nativeHLSPackagingCandidatesV3(request StartRequestV3) []string {
	var candidates []string
	for _, recipe := range []struct{ feature, name string }{{ClientNativeHLSSeparateAudioV3, TransformationHLSSeparateAudioV3}} {
		if HasFeatureV3(request.ClientFeatures, recipe.feature) && deliverySupportsFeatureV3(request, DeliveryClassHLSV3, recipe.feature) {
			candidates = append(candidates, recipe.name)
		}
	}
	if len(candidates) == 0 {
		return []string{""}
	}
	return append(candidates, "")
}

// PlanUsesSeparateAudioHLSV3 reports the frozen audio rendition recipe.
func PlanUsesSeparateAudioHLSV3(plan *PlanV3) bool {
	if plan == nil || plan.Delivery != DeliveryRemuxHLSV3 {
		return false
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == ExecutorServerV3 && transformation.Name == TransformationHLSSeparateAudioV3 && transformation.RecipeVersion == TransformationHLSSeparateAudioRecipeVersionV3 {
			return true
		}
	}
	return false
}

// NativeHLSMasterAttributesV3 describes the selected output, rather than a DV
// source whose metadata the recipe may strip. Unknown codec detail is omitted.
func NativeHLSMasterAttributesV3(plan *PlanV3, audioProfile string) (string, string) {
	if plan == nil {
		return "", ""
	}
	videoRange := hlsVideoRangeSDR
	switch plan.EffectiveRecipe.DynamicRange {
	case DynamicRangeHDR10V3, DynamicRangeHDR10PlusV3, DynamicRangeDolbyVisionV3:
		videoRange = hlsVideoRangePQ
	case DynamicRangeHLGV3:
		videoRange = hlsVideoRangeHLG
	}
	videoCodec := nativeHLSVideoCodecV3(plan)
	audioCodec := ""
	switch plan.EffectiveRecipe.AudioCodec {
	case audioCodecEAC3:
		audioCodec = "ec-3"
	case audioCodecAC3:
		audioCodec = "ac-3"
	case audioCodecAACV3:
		converted := false
		for _, transformation := range plan.Transformations {
			if transformation.Name == TransformationAudioToAACV3 {
				converted = true
			}
		}
		if converted || strings.EqualFold(strings.TrimSpace(audioProfile), "LC") {
			audioCodec = "mp4a.40.2"
		}
	}
	if audioCodec == "" {
		return "", videoRange
	}
	return videoCodec + "," + audioCodec, videoRange
}

func nativeHLSVideoCodecV3(plan *PlanV3) string {
	if plan.EffectiveRecipe.VideoCodec != transcodeCodecHEVC {
		return ""
	}
	if plan.EffectiveRecipe.DynamicRange == DynamicRangeDolbyVisionV3 && plan.Source.DVLevel > 0 {
		profile := plan.Source.DVProfile
		if PlanConvertsDV7ToDV81V3(plan) {
			profile = 8
		}
		return fmt.Sprintf("dvh1.%02d.%02d", profile, plan.Source.DVLevel)
	}
	if plan.Source.VideoLevel <= 0 {
		return ""
	}
	profile := "1.6"
	if plan.Source.BitDepth > 8 {
		profile = "2.4"
	}
	return fmt.Sprintf("hvc1.%s.L%d.B0", profile, plan.Source.VideoLevel)
}
