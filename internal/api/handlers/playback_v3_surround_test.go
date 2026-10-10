package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// surroundFallbackFixtureV3 plans a DTS 5.1 track for an Android TV whose sink
// validates E-AC-3 5.1 but not DTS, which yields the surround conversion.
func surroundFallbackFixtureV3(t *testing.T) (*PlaybackHandler, playback.PlannerInputV3, playback.PlannerResultV3) {
	t.Helper()
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	registry := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToEAC3V3, RecipeVersion: playback.TransformationAudioToEAC3RecipeVersionV3, Available: true},
	})
	presetLocalRegistryV3(handler, registry)

	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.CodecAudio = "dts"
	file.AudioChannels = 6
	file.AudioTracks = []models.AudioTrack{{Codec: "dts", Channels: 6, Layout: "5.1(side)", Default: true}}

	request := v3HandlerStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureLayoutPassthrough)
	request.Capabilities.Containers = []string{"mkv", "mp4"}
	request.Capabilities.CodecsAudio = []string{"aac", "eac3", "dts"}
	sink := &playback.AudioPassthroughV3{PassthroughCodecs: []string{"eac3"}, MaxChannels: 6, Entries: []playback.AudioPassthroughEntryV3{{Codec: "eac3", ChannelCounts: []int{2, 6}, Layouts: []string{"stereo", "5.1"}}}}
	request.Capabilities.AudioPassthrough = sink
	request.ClientPlaybackContext.Output.AudioPassthrough = sink
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true, AudioPassthroughCodecs: []string{"eac3"}}

	input := playback.PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file,
		Settings: playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: registry,
	}
	result, err := handler.planPlaybackWithCapabilitiesV3(context.Background(), input)
	if err != nil || !playback.IsSurroundBitstreamConversionV3(result.Plan) {
		t.Fatalf("fixture planned %s (err %v), want the surround conversion", playback.ExplainPlannerResultV3(result), err)
	}
	return handler, input, result
}

func TestReplanWithoutSurroundConversionV3FallsBackToDirectPlayWhenRefused(t *testing.T) {
	for _, refusal := range []transportErrorV3{
		{reason: capacityUnavailableReasonV3},
		{reason: audioTranscodingDisabledReasonV3},
		{reason: string(noderouting.OutcomeCapacityUnavailable), routePreparation: true},
		// A local transcode directory that cannot be created surfaces as an
		// internal error, but it is still the conversion's route failing.
		{reason: "internal_error", routePreparation: true},
	} {
		t.Run(refusal.reason, func(t *testing.T) {
			handler, input, result := surroundFallbackFixtureV3(t)
			fallback, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mediaAuthModeV3{}, func() playback.PlannerInputV3 { return input }, result, &refusal)
			if !ok || fallback.Plan == nil || fallback.Plan.Delivery != playback.DeliveryOriginalHTTPV3 {
				t.Fatalf("fallback = %s (ok %v), want direct play", playback.ExplainPlannerResultV3(fallback), ok)
			}
		})
	}
}

func TestReplanWithoutSurroundConversionV3LeavesOtherFailuresAlone(t *testing.T) {
	handler, input, result := surroundFallbackFixtureV3(t)
	plannerInput := func() playback.PlannerInputV3 { return input }
	mode := mediaAuthModeV3{}
	if _, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mode, plannerInput, result, nil); ok {
		t.Fatal("a successful start was replanned")
	}
	if _, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mode, plannerInput, result, &transportErrorV3{reason: "playback_attempt_reused"}); ok {
		t.Fatal("a reused attempt was replanned")
	}
	if _, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mode, plannerInput, result, &transportErrorV3{reason: "internal_error"}); ok {
		t.Fatal("an internal error outside route preparation was replanned")
	}
	// The attempt record is already saved, so its ID cannot start another route.
	if _, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mode, plannerInput, result, &transportErrorV3{reason: transcodeStartFailedReasonV3, routePreparation: true, attemptPersisted: true}); ok {
		t.Fatal("a failure after the attempt was persisted was replanned")
	}
	direct := result
	plan := *result.Plan
	plan.DecisionReason = "validated_original_playback"
	direct.Plan = &plan
	if _, ok := handler.replanWithoutSurroundConversionV3(context.Background(), mode, plannerInput, direct, &transportErrorV3{reason: capacityUnavailableReasonV3}); ok {
		t.Fatal("a plan that is not a surround conversion was replanned")
	}
}
