package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// audioShapeHandlerSource is the field source's audio shape: a copyable video
// track with a DTS 5.1 track the TV client over-claims.
func audioShapeHandlerSource(t *testing.T) *models.MediaFile {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.CodecAudio = "dts"
	file.AudioChannels = 6
	file.AudioTracks = []models.AudioTrack{{Language: "en", Codec: "dts", Channels: 6, Layout: "5.1", Default: true}}
	return file
}

func newAudioShapeHandler(t *testing.T, file *models.MediaFile) *PlaybackHandler {
	t.Helper()
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true"}}
	return handler
}

// audioShapeStartRequest is the prod client's advertisement: it claims DTS
// decode and an HLS channel ceiling of 8, so every server recipe keeps 5.1.
func audioShapeStartRequest() playback.StartRequestV3 {
	request := v3HandlerStartRequest()
	request.Capabilities.CodecsAudio = []string{"aac", "dts"}
	request.Capabilities.Containers = []string{"mkv", "hls"}
	maxChannels := 8
	request.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassOriginalHTTPV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"mkv"}, VideoCodecs: []string{"h264"},
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
		},
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"}, VideoCodecs: []string{"h264"},
			MaxChannels: &maxChannels,
			Subtitles:   playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
		},
	}
	return request
}

func audioInitFailureV3() playback.FailureV3 {
	return playback.FailureV3{
		Classification: "player_failure",
		Diagnostics: map[string]string{
			"error_code":      "5001",
			"error_code_name": "ERROR_CODE_AUDIO_TRACK_INIT_FAILED",
		},
	}
}

func audioShapeReplanRequestV3(start playback.StartRequestV3, plan *playback.PlanV3, id string, failure playback.FailureV3, attempted []string) playback.ReplanRequestV3 {
	key := playback.PlanAttemptKeyV3(*plan, start.ClientPlaybackContext.Output.OutputContextID, nil)
	return playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "audio-shape-" + id, FailedPlanID: plan.PlanID, PlanAttemptID: "audio-shape-attempt-" + id,
		PlanAttemptKey: key, AttemptedPlanKeys: append(append([]string(nil), attempted...), key), AttemptCount: 1,
		PositionSeconds: 10, QualityPreference: start.QualityPreference, SelectedTracks: plan.SelectedTracks,
		Failure: failure, Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext,
	}
}

// The field failure: an audio-output init failure on a direct-play plan. The
// delivery stays enabled and the next plan is the same HLS class with a stereo
// AAC recipe instead of the 5.1 one the device cannot initialize.
func TestReplanV3AudioInitFailureRetriesStereoInsteadOfDemotingTheDelivery(t *testing.T) {
	file := audioShapeHandlerSource(t)
	handler := newAudioShapeHandler(t, file)
	start := audioShapeStartRequest()
	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}

	recovered := postPlaybackReplanV3(t, handler, started.SessionID, audioShapeReplanRequestV3(start, started.PlaybackPlan, "0001", audioInitFailureV3(), nil))
	if recovered.Terminal != nil {
		t.Fatalf("audio-init recovery returned a terminal: %#v", recovered.Terminal)
	}
	plan := recovered.PlaybackPlan
	if plan == nil || plan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("recovered plan = %#v, want the HLS remux", plan)
	}
	if plan.EffectiveRecipe.AudioCodec != "aac" || plan.EffectiveRecipe.AudioChannels == nil || *plan.EffectiveRecipe.AudioChannels != 2 {
		t.Fatalf("recovered audio = %s %v, want AAC stereo", plan.EffectiveRecipe.AudioCodec, plan.EffectiveRecipe.AudioChannels)
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	direct := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if !direct.Enabled || direct.FailureReason == demoteDeliveryReasonV3 {
		t.Fatalf("direct play was demoted on an audio-init failure: %+v", direct)
	}
	if direct.FailureReason != audioShapeRetryReasonV3 {
		t.Fatalf("direct play retry marker = %q, want %q", direct.FailureReason, audioShapeRetryReasonV3)
	}
}

// A second audio-init failure on the already-stereo retry demotes the delivery
// and reports the specific terminal instead of looping.
func TestReplanV3SecondAudioInitFailureDemotesAndNamesTheAudioOutput(t *testing.T) {
	file := audioShapeHandlerSource(t)
	handler := newAudioShapeHandler(t, file)
	start := audioShapeStartRequest()
	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil {
		t.Fatal("start returned no plan")
	}
	firstFailure := audioShapeReplanRequestV3(start, started.PlaybackPlan, "0001", audioInitFailureV3(), nil)
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, firstFailure)
	if recovered.PlaybackPlan == nil {
		t.Fatalf("first recovery = %#v", recovered)
	}

	second := audioShapeReplanRequestV3(start, recovered.PlaybackPlan, "0002", audioInitFailureV3(), firstFailure.AttemptedPlanKeys)
	second.AttemptCount = 2
	failed := postPlaybackReplanV3(t, handler, started.SessionID, second)
	if failed.Terminal == nil || failed.Terminal.Reason != playback.TerminalAudioOutputUnsupportedV3 || !failed.Terminal.Retryable {
		t.Fatalf("second failure terminal = %#v, want retryable %q", failed.Terminal, playback.TerminalAudioOutputUnsupportedV3)
	}
	if failed.Terminal.Detail == "" || len(failed.Terminal.Detail) < len("5001") {
		t.Fatalf("terminal detail = %q, want the player's failing code", failed.Terminal.Detail)
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	hls := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3]
	if hls.Enabled || hls.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("stereo retry delivery was not demoted after a second audio-init failure: %+v", hls)
	}
}

// A player failure with no audio-output evidence retires the direct route and
// keeps the HLS class eligible: the direct plan must not come back, but the
// recipes behind the packaged class are still untried. This is the direct-play
// half of the "any recognized failure retires the route that failed" rule.
func TestReplanV3PlayerFailureRetiresDirectPlayKeepsTheHLSClass(t *testing.T) {
	file := audioShapeHandlerSource(t)
	handler := newAudioShapeHandler(t, file)
	start := audioShapeStartRequest()
	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}

	failure := playback.FailureV3{Classification: "player_failure", Message: "Decoder init failed for OMX.MTK.VIDEO.DECODER.DVHE.STH"}
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, audioShapeReplanRequestV3(start, started.PlaybackPlan, "0001", failure, nil))
	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	direct := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if direct.Enabled || direct.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("a player failure did not retire the direct-play class: %+v", direct)
	}
	abandonedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, start.ClientPlaybackContext.Output.OutputContextID, nil)
	if !containsStringExactV3(record.RecoveryState.RecipeExclusions, abandonedKey) {
		t.Fatalf("the abandoned recipe was not retired durably: %v", record.RecoveryState.RecipeExclusions)
	}
	// The next plan is the ordinary surround remux: no stereo recipe is forced
	// without audio-output evidence.
	if recovered.PlaybackPlan == nil || recovered.PlaybackPlan.EffectiveRecipe.AudioChannels == nil || *recovered.PlaybackPlan.EffectiveRecipe.AudioChannels != 6 {
		t.Fatalf("recovered plan = %#v, want the ordinary 5.1 remux", recovered.PlaybackPlan)
	}
}

// A web-shaped attempt with no surround decode limits and nothing to downmix
// keeps its historical terminal: the audio-shape path cannot fire, the abort
// retires the direct route (and its recipe), and with no packaged delivery the
// exhaustion stays the generic non-retryable terminal — there is no route a
// retry could negotiate.
func TestReplanV3WebShapedPlayerFailureKeepsGenericTerminal(t *testing.T) {
	file := v3HandlerFixtureFile(t) // stereo AAC, direct play only
	handler := newAudioShapeHandler(t, file)
	start := v3HandlerStartRequest()
	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}

	failure := playback.FailureV3{Classification: "player_failure", Message: "Decoder init failed for c2.android.avc.decoder"}
	failed := postPlaybackReplanV3(t, handler, started.SessionID, audioShapeReplanRequestV3(start, started.PlaybackPlan, "0001", failure, nil))
	if failed.Terminal == nil || failed.Terminal.Reason != "adaptation_unavailable" || failed.Terminal.Retryable {
		t.Fatalf("web-shaped terminal = %#v, want the unchanged non-retryable adaptation_unavailable", failed.Terminal)
	}
	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	direct := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if direct.Enabled || direct.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("an abort did not retire the web-shaped direct-play class: %+v", direct)
	}
	abandonedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, start.ClientPlaybackContext.Output.OutputContextID, nil)
	if !containsStringExactV3(record.RecoveryState.RecipeExclusions, abandonedKey) {
		t.Fatalf("the web-shaped abandoned recipe was not retired durably: %v", record.RecoveryState.RecipeExclusions)
	}
}
