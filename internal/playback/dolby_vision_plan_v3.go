package playback

import "github.com/Silo-Server/silo-server/internal/models"

// planServerDV7ToDV81V3 validates converted output as Profile 8.1, then restores
// the source descriptor so file and selected track identities remain stable.
func planServerDV7ToDV81V3(input PlannerInputV3, file *models.MediaFile, source SourceDescriptorV3) (PlannerResultV3, bool) {
	if !input.Settings.TranscodeEnabled || input.Settings.ViewerTranscodeDisabled || source.DVProfile != 7 || source.VideoCopyUnsafe || !source.DVBaseLayerProven || source.DVBLCompatID != 6 || len(file.VideoTracks) == 0 || !file.VideoTracks[0].DVRPUPresent ||
		!HasFeatureV3(input.Request.ClientFeatures, FeatureServerDV7ToDV81V3) || nativeHLSPackagingV3(input.Request) != TransformationHLSSeparateAudioV3 {
		return PlannerResultV3{}, false
	}
	registry := input.hlsRemuxRegistry()
	if !registry.Available(TransformationServerDV7DV81V3) || !registry.Available(TransformationHLSSeparateAudioV3) {
		return PlannerResultV3{}, false
	}
	converted := *file
	converted.VideoTracks = append([]models.VideoTrack(nil), file.VideoTracks...)
	track := &converted.VideoTracks[0]
	track.DVProfile = 8
	track.DVBLCompatID = 1
	track.DVELPresent = false
	track.DVEnhancementLayer = string(EnhancementNoneV3)
	track.VideoRangeType = videoRangeDOVIWithHDR10
	logical := input
	logical.EffectiveFile = &converted
	logical.Request.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{DeliveryClassHLSV3: input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]}
	result := PlanPlaybackV3(logical)
	if result.Plan == nil || result.PlayMethod != PlayRemux || result.Plan.EffectiveRecipe.DynamicRange != DynamicRangeDolbyVisionV3 || !PlanUsesSeparateAudioHLSV3(result.Plan) {
		return PlannerResultV3{}, false
	}
	plan := result.Plan
	plan.Source = source
	plan.Transformations = append(plan.Transformations, TransformationV3{Name: TransformationServerDV7DV81V3, Executor: ExecutorServerV3, RecipeVersion: TransformationServerDV7DV81RecipeVersionV3, ValidatedClaims: []string{ClaimHDR10BaseLayerPreservedV3, ClaimEnhancementLayerDiscardedV3, "dolby_vision_profile_8_1"}})
	plan.DegradationWarnings = append(plan.DegradationWarnings, DegradationWarningV3{Code: "dolby_vision_enhancement_layer_removed", Message: "The enhancement layer is discarded and Dolby Vision metadata is converted to Profile 8.1."})
	plan.DecisionReason = TransformationServerDV7DV81V3
	finalizePlanIdentityV3(plan, input.Request.PlaybackAttemptID, input.Request.ClientPlaybackContext.Output.OutputContextID)
	if planAttemptedV3(*plan, input.Request.ClientPlaybackContext.Output.OutputContextID, input.AttemptedKeys) {
		return PlannerResultV3{}, false
	}
	return result, true
}

// PlanConvertsDV7ToDV81V3 reports the server-owned, frozen DV conversion recipe.
func PlanConvertsDV7ToDV81V3(plan *PlanV3) bool {
	if !PlanUsesSeparateAudioHLSV3(plan) {
		return false
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == ExecutorServerV3 && transformation.Name == TransformationServerDV7DV81V3 && transformation.RecipeVersion == TransformationServerDV7DV81RecipeVersionV3 {
			return true
		}
	}
	return false
}
