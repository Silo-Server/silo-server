package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The prod thrash this guards against: a Dolby Vision Profile 8 movie starts
// via direct play (client_dv8_base_layer), the decoder fails, a failure
// recovery moves to remux HLS — and the next track_change replan reads the
// client's still-advertised direct-play claim and flips right back, each flip
// reloading the stream and orphaning the client's subtitle track state (a
// menu that shows a selection that is not actually playing). The server's
// delivery demotion must stick across replans.
func TestHandleReplanPlaybackV3DemotedDeliveryStaysDisabledAcrossReplans(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "2", Available: true},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}

	start := v3HandlerStartRequest()
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		VideoCodecs: []string{"h264"},
	}
	// The fallback route: without an advertised HLS delivery the demotion of
	// direct play would leave no viable route and the recovery would return
	// a terminal instead of the replacement plan the thrash fix promises.
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers:        []string{"hls"},
		VideoCodecs:       []string{"h264"},
		AudioDecodeCodecs: []string{"aac"},
		Subtitles:         playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	plan := started.PlaybackPlan
	if plan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("fixture expected a direct-play start, got %s (%s)", plan.Delivery, plan.DecisionReason)
	}

	// The decoder fails: a failure recovery abandons the delivery and demotes
	// it on the durable request.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "demote-recovery-0001", FailedPlanID: plan.PlanID, PlanAttemptID: "demote-attempt-0001",
		PlanAttemptKey: plan.PlanAttemptKey, AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
		PositionSeconds: 10, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "decoder_failure"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	if recovered.Terminal != nil {
		t.Fatalf("failure recovery returned a terminal: %#v", recovered.Terminal)
	}
	recoveredPlan := recovered.PlaybackPlan
	if recoveredPlan == nil {
		t.Fatal("failure recovery returned no plan")
	}
	if playback.DeliveryClassV3(recoveredPlan.Delivery) == playback.DeliveryClassOriginalHTTPV3 {
		t.Fatalf("failure recovery returned the delivery it just abandoned: %s", recoveredPlan.Delivery)
	}

	// A later track_change replan re-sends the client's full capability
	// advertisement (Enabled direct play). The server-side demotion must
	// survive the overlay: the plan must not flip back to the failed delivery.
	trackChangeReq := recoveryReq
	trackChangeReq.Operation = playback.ReplanOperationTrackChangeV3
	trackChangeReq.ReplanRequestID = "demote-track-0002"
	trackChangeReq.FailedPlanID = recoveredPlan.PlanID
	trackChangeReq.PlanAttemptID = "demote-attempt-0002"
	trackChangeReq.PlanAttemptKey = recoveredPlan.PlanAttemptKey
	trackChangeReq.AttemptedPlanKeys = nil
	trackChangeReq.Failure = playback.FailureV3{}
	trackChangeReq.SelectedTracks = recoveredPlan.SelectedTracks
	next := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq)
	if next.Terminal != nil {
		t.Fatalf("track_change replan returned a terminal: %#v", next.Terminal)
	}
	nextPlan := next.PlaybackPlan
	if nextPlan == nil {
		t.Fatal("track_change replan returned no plan")
	}
	if playback.DeliveryClassV3(nextPlan.Delivery) == playback.DeliveryClassOriginalHTTPV3 {
		t.Fatalf("track_change replan flipped back to the demoted direct-play route: %s (%s)", nextPlan.Delivery, nextPlan.DecisionReason)
	}

	// The demotion is durable on the attempt record, not just in-flight.
	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	caps := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if caps.Enabled || caps.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("demotion did not persist on the attempt record: %+v", caps)
	}
}

// The stickiness must not depend on what the client sends this round: a
// successful intermediate replan whose capability payload carries the demoted
// class disabled (or omits it entirely) must not erase the server-owned
// marker — otherwise a later replan can re-advertise the delivery the server
// already failed.
func TestDeliveryDemotionSurvivesDisabledAndOmittedEntriesAcrossReplans(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "2", Available: true},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}

	start := v3HandlerStartRequest()
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		VideoCodecs: []string{"h264"},
	}
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers:        []string{"hls"},
		VideoCodecs:       []string{"h264"},
		AudioDecodeCodecs: []string{"aac"},
		Subtitles:         playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	plan := started.PlaybackPlan
	if plan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("fixture expected a direct-play start, got %s (%s)", plan.Delivery, plan.DecisionReason)
	}

	// The delivery fails and the server demotes it on the durable request.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "demote-omit-recovery-0001", FailedPlanID: plan.PlanID, PlanAttemptID: "demote-omit-attempt-0001",
		PlanAttemptKey: plan.PlanAttemptKey, AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
		PositionSeconds: 10, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "decoder_failure"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	if recovered.Terminal != nil || recovered.PlaybackPlan == nil {
		t.Fatalf("failure recovery: terminal=%#v plan=%v", recovered.Terminal, recovered.PlaybackPlan)
	}
	recoveredPlan := recovered.PlaybackPlan

	// Intermediate replan 1: the payload re-sends original_http DISABLED
	// (SupportedOnDevice=false, Enabled=false, no marker). The re-application
	// must still re-stamp the marker so the commit keeps the server evidence.
	// Deliveries maps are aliased by Go struct copies, so every round clones
	// the map — otherwise disabling/deleting an entry here would corrupt the
	// base context later rounds build from.
	cloneContext := func(base playback.ClientPlaybackContextV3) playback.ClientPlaybackContextV3 {
		cloned := base
		cloned.Deliveries = make(map[string]playback.DeliveryCapabilityV3, len(base.Deliveries))
		for class, capability := range base.Deliveries {
			cloned.Deliveries[class] = capability
		}
		return cloned
	}
	assertDeliveryEntry := func(t *testing.T, ctx playback.ClientPlaybackContextV3, class string, wantEnabled bool) {
		t.Helper()
		capability, ok := ctx.Deliveries[class]
		if !ok {
			t.Fatalf("outgoing payload dropped %s entirely", class)
		}
		if capability.Enabled != wantEnabled {
			t.Fatalf("outgoing payload %s Enabled = %v, want %v", class, capability.Enabled, wantEnabled)
		}
	}

	disabledContext := cloneContext(start.ClientPlaybackContext)
	disabledOriginal := disabledContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	disabledOriginal.Enabled = false
	disabledOriginal.SupportedOnDevice = false
	disabledOriginal.FailureReason = ""
	disabledOriginal.ValidatedClaims = []string{"some_client_claim"}
	disabledContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = disabledOriginal
	disabledReq := recoveryReq
	disabledReq.Operation = playback.ReplanOperationQualityChangeV3
	disabledReq.QualityPreference = "high"
	disabledReq.Failure = playback.FailureV3{}
	disabledReq.ReplanRequestID = "demote-omit-0002"
	disabledReq.FailedPlanID = recoveredPlan.PlanID
	disabledReq.PlanAttemptID = "demote-omit-attempt-0002"
	disabledReq.PlanAttemptKey = recoveredPlan.PlanAttemptKey
	disabledReq.AttemptedPlanKeys = nil
	disabledReq.ClientPlaybackContext = disabledContext
	// Assert the outgoing payload actually carries the disabled entry — the
	// map-aliasing regression this guard exists for.
	assertDeliveryEntry(t, disabledReq.ClientPlaybackContext, playback.DeliveryClassOriginalHTTPV3, false)
	intermediate := postPlaybackReplanV3(t, handler, started.SessionID, disabledReq)
	if intermediate.Terminal != nil {
		t.Fatalf("intermediate replan with the delivery disabled returned a terminal: %#v", intermediate.Terminal)
	}
	if persisted, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID); err != nil {
		t.Fatal(err)
	} else if caps := persisted.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]; caps.Enabled || caps.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("disabled-entry round erased the demotion marker: %+v", caps)
	}
	// The base context must be uncorrupted by the disabled round.
	assertDeliveryEntry(t, start.ClientPlaybackContext, playback.DeliveryClassOriginalHTTPV3, true)

	// Intermediate replan 2: the payload OMITS the demoted class entirely.
	// The re-application must synthesize the disabled entry so the marker
	// survives the commit.
	omittedContext := cloneContext(disabledContext)
	delete(omittedContext.Deliveries, playback.DeliveryClassOriginalHTTPV3)
	omittedReq := disabledReq
	omittedReq.QualityPreference = "medium"
	omittedReq.ReplanRequestID = "demote-omit-0003"
	omittedReq.FailedPlanID = intermediate.PlaybackPlan.PlanID
	omittedReq.PlanAttemptID = "demote-omit-attempt-0003"
	omittedReq.PlanAttemptKey = intermediate.PlaybackPlan.PlanAttemptKey
	omittedReq.AttemptedPlanKeys = nil
	omittedReq.ClientPlaybackContext = omittedContext
	if _, exists := omittedReq.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]; exists {
		t.Fatal("omitted round payload still carries original_http; the fixture is not omitting it")
	}
	omitted := postPlaybackReplanV3(t, handler, started.SessionID, omittedReq)
	if omitted.Terminal != nil {
		t.Fatalf("intermediate replan with the delivery omitted returned a terminal: %#v", omitted.Terminal)
	}
	if persisted, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID); err != nil {
		t.Fatal(err)
	} else if caps, ok := persisted.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]; !ok {
		t.Fatal("omitted-entry round dropped the demoted delivery from the durable record entirely")
	} else if caps.Enabled || caps.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("omitted-entry round erased the demotion marker: %+v", caps)
	}

	// Final replan: the client re-enables the delivery at full strength
	// (explicitly enabled and supported in the outgoing payload). The
	// server-side demotion must still win — the route must not come back.
	reenabledContext := cloneContext(start.ClientPlaybackContext)
	reenabledOriginal := reenabledContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	reenabledOriginal.Enabled = true
	reenabledOriginal.SupportedOnDevice = true
	reenabledOriginal.FailureReason = ""
	reenabledOriginal.ValidatedClaims = nil
	reenabledContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = reenabledOriginal
	reenabledReq := disabledReq
	reenabledReq.QualityPreference = "auto"
	reenabledReq.ReplanRequestID = "demote-omit-0003-re"
	reenabledReq.FailedPlanID = omitted.PlaybackPlan.PlanID
	reenabledReq.PlanAttemptID = "demote-omit-attempt-0004"
	reenabledReq.PlanAttemptKey = omitted.PlaybackPlan.PlanAttemptKey
	reenabledReq.AttemptedPlanKeys = nil
	reenabledReq.ClientPlaybackContext = reenabledContext
	// The transition under test: the outgoing payload really does re-enable.
	assertDeliveryEntry(t, reenabledReq.ClientPlaybackContext, playback.DeliveryClassOriginalHTTPV3, true)
	final := postPlaybackReplanV3(t, handler, started.SessionID, reenabledReq)
	if final.Terminal != nil {
		t.Fatalf("final replan returned a terminal: %#v", final.Terminal)
	}
	if final.PlaybackPlan == nil {
		t.Fatal("final replan returned no plan")
	}
	if playback.DeliveryClassV3(final.PlaybackPlan.Delivery) == playback.DeliveryClassOriginalHTTPV3 {
		t.Fatalf("final replan re-enabled the demoted delivery: %s (%s)", final.PlaybackPlan.Delivery, final.PlaybackPlan.DecisionReason)
	}
}

// A direct play that mounts and never produces a frame is the client startup
// guard's deadline failure. The guard reports `startup_timeout` with the failed
// plan's attempt key, and a startup timeout is not evidence about the delivery
// class on its own (see corroboratedStartupTimeoutV3), so the class stays
// eligible. The attempt key is what must keep the identical unplayable plan
// from being planned again: the recovery has to leave original_http and pick a
// packaged route for the same file.
func TestStartupTimeoutFailureRecoveryLeavesDirectPlay(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "2", Available: true},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}

	start := v3HandlerStartRequest()
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		VideoCodecs: []string{"h264"},
	}
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers:        []string{"hls"},
		VideoCodecs:       []string{"h264"},
		AudioDecodeCodecs: []string{"aac"},
		Subtitles:         playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	plan := started.PlaybackPlan
	if plan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("fixture expected a direct-play start, got %s (%s)", plan.Delivery, plan.DecisionReason)
	}

	// The guard's report: no frame before the deadline, and the route it was
	// waiting on is in the attempted-key history.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "startup-timeout-recovery-0001", FailedPlanID: plan.PlanID, PlanAttemptID: "startup-timeout-attempt-0001",
		PlanAttemptKey: plan.PlanAttemptKey, AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
		PositionSeconds: 0, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "startup_timeout"},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	if recovered.Terminal != nil {
		t.Fatalf("startup-timeout recovery returned a terminal: %#v", recovered.Terminal)
	}
	if recovered.PlaybackPlan == nil {
		t.Fatal("startup-timeout recovery returned no plan")
	}
	if playback.DeliveryClassV3(recovered.PlaybackPlan.Delivery) == playback.DeliveryClassOriginalHTTPV3 {
		t.Fatalf("startup-timeout recovery re-planned the identical direct-play route: %s (%s)",
			recovered.PlaybackPlan.Delivery, recovered.PlaybackPlan.DecisionReason)
	}
}
