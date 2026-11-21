package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// applyReplanProgressV3 records one durable progress sample so the next replan
// sees the attempt as having shown frames. The store's sequencing state is
// what executeReplanV3 reads; the session's live position is deliberately not
// involved.
func applyReplanProgressV3(t *testing.T, handler *PlaybackHandler, sessionID string) {
	t.Helper()
	store, ok := handler.PlanStoreV3.(playback.ProgressStoreV3)
	if !ok {
		t.Fatal("plan store does not implement progress")
	}
	receipt, err := store.ApplyProgress(context.Background(), sessionID, playback.ProgressSampleV3{
		Sequence: 3,
		Position: 42,
	})
	if err != nil {
		t.Fatalf("apply progress: %v", err)
	}
	if receipt.Accepted == nil || receipt.Accepted.Sequence != 3 {
		t.Fatalf("progress receipt = %#v, want sequence 3 accepted", receipt)
	}
}

func newProgressReissueHandlerV3(t *testing.T) *PlaybackHandler {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	// Video adaptation is unavailable: after the copy route is retired the
	// planner can only fall through to transcoding_disabled / routes_exhausted.
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	return handler
}

func progressReissueStartV3(t *testing.T, handler *PlaybackHandler) (playback.DecisionResponseV3, playback.StartRequestV3) {
	t.Helper()
	request := v3HandlerStartRequest()
	request.QualityPreference = "original"
	// HLS stays declared so a recovery that exhausts the copy route reaches
	// the transcoding-disabled branch instead of client_hls_unsupported. Its
	// declared video codec is intentionally not the source's, so the HLS copy
	// route is unavailable and only video adaptation remains.
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
	}
	started := startV3PlaybackForHandlerTest(t, handler, request)
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want original HTTP", started.PlaybackPlan)
	}
	return started, request
}

func progressReissueRequestV3(started playback.DecisionResponseV3, startRequest playback.StartRequestV3, requestID string, attemptCount int, attemptedKeys []string) playback.ReplanRequestV3 {
	return playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       requestID,
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         requestID,
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     attemptedKeys,
		AttemptCount:          attemptCount,
		QualityPreference:     "original",
		PositionSeconds:       42,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: connectivityTransportStallV3},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
}

func applyReplanFirstFrameV3(t *testing.T, handler *PlaybackHandler, sessionID, attemptID, planID, planAttemptKey string) {
	t.Helper()
	_, err := handler.PlanStoreV3.RecordRouteEvent(context.Background(), playback.RouteEventRecordV3{
		RouteEventV3: playback.RouteEventV3{
			ProtocolVersion:   playback.ProtocolV3,
			PlaybackAttemptID: attemptID,
			SessionID:         sessionID,
			PlanID:            planID,
			PlanAttemptKey:    planAttemptKey,
			Event:             playback.RouteEventFirstFrameV3,
		},
	})
	if err != nil {
		t.Fatalf("record first frame event: %v", err)
	}
}

// A recovery after frames reissues the recipe that was playing, with a fresh
// plan identity so the client does not mistake it for a cached failed plan. The
// next failure of that reissue retires the route as before.
func TestHandleReplanPlaybackV3ReissuesRecipeAfterProgress(t *testing.T) {
	handler := newProgressReissueHandlerV3(t)
	started, startRequest := progressReissueStartV3(t, handler)
	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	first := postPlaybackReplanV3(t, handler, started.SessionID, progressReissueRequestV3(started, startRequest, "replan-progress-reissue-0001", 1, []string{started.PlaybackPlan.PlanAttemptKey}))
	if first.Terminal != nil || first.PlaybackPlan == nil {
		t.Fatalf("reissue response = %#v terminal = %#v", first, first.Terminal)
	}
	if first.PlaybackPlan.Delivery != started.PlaybackPlan.Delivery ||
		!reflect.DeepEqual(first.PlaybackPlan.EffectiveRecipe, started.PlaybackPlan.EffectiveRecipe) {
		t.Fatalf("reissued route = %s / %#v, want the recipe that played", first.PlaybackPlan.Delivery, first.PlaybackPlan.EffectiveRecipe)
	}
	if first.PlaybackPlan.PlanID == started.PlaybackPlan.PlanID {
		t.Fatalf("reissued plan id = %q, want a fresh generation id", first.PlaybackPlan.PlanID)
	}
	if first.PlaybackPlan.PlanAttemptKey != started.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("reissued attempt key = %q, want canonical recipe key %q", first.PlaybackPlan.PlanAttemptKey, started.PlaybackPlan.PlanAttemptKey)
	}

	// The reissued transport is a second transport; its failure is the second
	// recovery and must retire the route rather than reissue it forever.
	second := postPlaybackReplanV3(t, handler, started.SessionID, progressReissueRequestV3(first, startRequest, "replan-progress-reissue-0002", 2, []string{
		started.PlaybackPlan.PlanAttemptKey,
		first.PlaybackPlan.PlanAttemptKey,
	}))
	if second.Terminal == nil || second.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !second.Terminal.Retryable {
		t.Fatalf("second recovery = %#v terminal = %#v, want retryable routes_exhausted", second, second.Terminal)
	}
}

// Plan A renders and records progress. A track change installs Plan B.
// Plan B fails before its first frame: it must not be reissued using Plan A's progress.
func TestHandleReplanPlaybackV3SuccessorTransportFailsBeforeFirstFrameNotReissued(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
	}
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	// User track change to audio track 1 installs Plan B.
	audioIdx1 := 1
	trackChangeReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-track-change-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-tc-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       10,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx1}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	trackResp := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq)
	if trackResp.Outcome != playback.OutcomePlayableV3 || trackResp.PlaybackPlan == nil {
		t.Fatalf("track change failed: %#v", trackResp)
	}

	planBKey := trackResp.PlaybackPlan.PlanAttemptKey
	if planBKey == started.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("planBKey %q must differ from planAKey %q", planBKey, started.PlaybackPlan.PlanAttemptKey)
	}

	// Plan B fails before rendering a first frame.
	replanReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-b-failed-pre-frame",
		FailedPlanID:          trackResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-b-fail",
		PlanAttemptKey:        planBKey,
		AttemptedPlanKeys:     []string{started.PlaybackPlan.PlanAttemptKey, planBKey},
		AttemptCount:          1,
		PositionSeconds:       10,
		Failure:               playback.FailureV3{Classification: connectivityTransportStallV3},
		SelectedTracks:        trackResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}

	resp := postPlaybackReplanV3(t, handler, started.SessionID, replanReq)
	if resp.Terminal == nil || resp.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !resp.Terminal.Retryable {
		t.Fatalf("Plan B failed before first frame, want routes_exhausted terminal, got: %#v", resp)
	}
}

// Recipe A renders. The viewer changes tracks to Recipe B, which renders.
// The viewer returns to Recipe A, opening a new transport.
// If that new A fails before its first frame, it must not qualify for reissue using the earlier A transport.
func TestHandleReplanPlaybackV3ReopenedRecipeFailsBeforeFirstFrameNotReissued(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
	}
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	// 1. Recipe A starts. Its first-frame event is delayed and NOT persisted yet.
	delayedPlanIDA := started.PlaybackPlan.PlanID
	delayedPlanKeyA := started.PlaybackPlan.PlanAttemptKey

	// 2. Track change to audio track 1 installs Recipe B.
	audioIdx1 := 1
	trackChangeReq1 := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-tc-to-b",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-tc-b",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       10,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx1}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	respB := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq1)
	if respB.Outcome != playback.OutcomePlayableV3 || respB.PlaybackPlan == nil {
		t.Fatalf("track change to B failed: %#v", respB)
	}
	// Recipe B renders first frame.
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, respB.PlaybackPlan.PlanID, respB.PlaybackPlan.PlanAttemptKey)

	// 3. User returns to Recipe A (audio track 0). Reopening Recipe A installs a new transport generation.
	audioIdx0 := 0
	trackChangeReq2 := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-tc-return-to-a",
		FailedPlanID:          respB.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-tc-return-a",
		PlanAttemptKey:        respB.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       15,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:0", Index: &audioIdx0}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	respA2 := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq2)
	if respA2.Outcome != playback.OutcomePlayableV3 || respA2.PlaybackPlan == nil {
		t.Fatalf("return to Recipe A failed: %#v", respA2)
	}

	// 4. Now persist the delayed original first-frame event from Recipe A's first installation.
	// Its plan ID matches the original A installation, but NOT the new reopened A generation.
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, delayedPlanIDA, delayedPlanKeyA)

	// Verify the attempt's CurrentTransportRendered flag is still false because the plan IDs differ.
	rec, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CurrentTransportRendered {
		t.Fatalf("delayed first frame event from earlier installation falsely marked reopened transport as rendered")
	}

	// 5. That new A transport fails before its first frame.
	failA2Req := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-a2-fail-pre-frame",
		FailedPlanID:          respA2.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-a2-fail",
		PlanAttemptKey:        respA2.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{respB.PlaybackPlan.PlanAttemptKey, respA2.PlaybackPlan.PlanAttemptKey},
		AttemptCount:          1,
		PositionSeconds:       15,
		Failure:               playback.FailureV3{Classification: connectivityTransportStallV3},
		SelectedTracks:        respA2.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}

	resp := postPlaybackReplanV3(t, handler, started.SessionID, failA2Req)
	// Must NOT reissue because the new Recipe A transport never rendered a frame.
	if resp.Terminal == nil || resp.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !resp.Terminal.Retryable {
		t.Fatalf("reopened Recipe A transport failed pre-first-frame, want routes_exhausted terminal, got: %#v", resp)
	}
}

// Capability or execution failures (e.g. browser_engine_unsupported_source) must not
// be reissued as transient connectivity losses even after earlier progress.
func TestHandleReplanPlaybackV3UnsupportedSourceNotTreatedAsConnectivity(t *testing.T) {
	handler := newProgressReissueHandlerV3(t)
	started, startRequest := progressReissueStartV3(t, handler)
	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	req := progressReissueRequestV3(started, startRequest, "replan-unsupported-source", 1, []string{started.PlaybackPlan.PlanAttemptKey})
	req.Failure = playback.FailureV3{Classification: "browser_engine_unsupported_source"}

	resp := postPlaybackReplanV3(t, handler, started.SessionID, req)
	if resp.Terminal == nil || resp.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !resp.Terminal.Retryable {
		t.Fatalf("unsupported source must not be reissued, got: %#v", resp)
	}
}

// A client sending repeated count-1 requests cannot bypass the server-side reissue bound.
func TestHandleReplanPlaybackV3RepeatedAttemptCountOneRefused(t *testing.T) {
	handler := newProgressReissueHandlerV3(t)
	started, startRequest := progressReissueStartV3(t, handler)
	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	// First recovery reissues.
	resp1 := postPlaybackReplanV3(t, handler, started.SessionID, progressReissueRequestV3(started, startRequest, "replan-reissue-001", 1, []string{started.PlaybackPlan.PlanAttemptKey}))
	if resp1.Outcome != playback.OutcomePlayableV3 || resp1.PlaybackPlan == nil {
		t.Fatalf("first recovery expected playable, got: %#v", resp1)
	}

	// Second recovery sending a new replan request ID with forged AttemptCount = 1 is refused by server state.
	req2 := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-reissue-002-forged-count-1",
		FailedPlanID:          resp1.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-forged-count-1",
		PlanAttemptKey:        resp1.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{started.PlaybackPlan.PlanAttemptKey, resp1.PlaybackPlan.PlanAttemptKey},
		AttemptCount:          1,
		PositionSeconds:       42,
		Failure:               playback.FailureV3{Classification: connectivityTransportStallV3},
		SelectedTracks:        resp1.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	resp2 := postPlaybackReplanV3(t, handler, started.SessionID, req2)
	if resp2.Terminal == nil || resp2.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !resp2.Terminal.Retryable {
		t.Fatalf("repeated count-1 recovery must be refused by server state, got: %#v", resp2)
	}
}

// Without a durable progress sample the route never proved it could reach this
// client. The ordinary failure path retires it even when the client reports a
// transport stall.
func TestHandleReplanPlaybackV3RetiresRecipeBeforeFirstFrame(t *testing.T) {
	handler := newProgressReissueHandlerV3(t)
	started, startRequest := progressReissueStartV3(t, handler)

	response := postPlaybackReplanV3(t, handler, started.SessionID, progressReissueRequestV3(started, startRequest, "replan-no-progress-0001", 1, []string{started.PlaybackPlan.PlanAttemptKey}))
	if response.Terminal == nil || response.Terminal.Reason != playback.TerminalRoutesExhaustedV3 || !response.Terminal.Retryable {
		t.Fatalf("pre-first-frame recovery = %#v terminal = %#v, want routes_exhausted", response, response.Terminal)
	}
}

func newVirtualSameCandidateRecoveryHandlerV3(t *testing.T) (*PlaybackHandler, *models.MediaFile, map[int]*models.MediaFile) {
	t.Helper()
	complete := v3HandlerFixtureFile(t)
	complete.FilePath = "virtual://movie/tt-recovery-carry"
	complete.VirtualOwnerInstallationID = 5
	files := map[int]*models.MediaFile{complete.ID: complete}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		return "http://127.0.0.1:8080/stream?path=" + path, nil
	})
	handler.VirtualCandidateFileLookup = func(_ context.Context, path, _ string, _ string, _ int) (*models.MediaFile, error) {
		candidate := *files[complete.ID]
		candidate.FilePath = path
		candidate.VideoTracks = append([]models.VideoTrack(nil), candidate.VideoTracks...)
		candidate.AudioTracks = append([]models.AudioTrack(nil), candidate.AudioTracks...)
		return &candidate, nil
	}
	return handler, complete, files
}

func startVirtualSameCandidateRecoveryV3(t *testing.T, handler *PlaybackHandler, complete *models.MediaFile) (playback.DecisionResponseV3, playback.StartRequestV3) {
	t.Helper()
	startRequest := v3HandlerStartRequest()
	startRequest.FileID = complete.ID
	startRequest.QualityPreference = "auto"
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("virtual start response = %#v", started)
	}
	if started.PlaybackPlan.InventoryStatus != "declared" {
		t.Fatalf("start inventory status = %q, want declared", started.PlaybackPlan.InventoryStatus)
	}
	return started, startRequest
}

// A same-candidate failure recovery must keep the video descriptor the start
// plan validated when the rehydrated catalog row lost it. The top-up is
// in-memory: the catalog row stays unprobed and the plan stays declared.
func TestHandleReplanPlaybackV3CarriesDescriptorForSameCandidateRecovery(t *testing.T) {
	handler, complete, files := newVirtualSameCandidateRecoveryHandlerV3(t)
	started, startRequest := startVirtualSameCandidateRecoveryV3(t, handler, complete)

	// The row still names the pinned candidate but lost the probe's frame rate
	// and bitrate, exactly as a fresh listing row does.
	degraded := &models.MediaFile{
		ID:                         complete.ID,
		ContentID:                  complete.ContentID,
		FilePath:                   complete.FilePath,
		Container:                  "virtual",
		CodecVideo:                 complete.CodecVideo,
		CodecAudio:                 complete.CodecAudio,
		Resolution:                 complete.Resolution,
		Duration:                   complete.Duration,
		VirtualOwnerInstallationID: complete.VirtualOwnerInstallationID,
		VideoTracks: []models.VideoTrack{{
			Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, BitDepth: 8,
			VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR,
		}},
		AudioTracks: complete.AudioTracks,
	}
	files[complete.ID] = degraded

	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	response := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     "attempt-handler-0001",
		ReplanRequestID:       "replan-recovery-carry-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-recovery-carry-0001",
		PlanAttemptKey:        failedKey,
		AttemptedPlanKeys:     []string{failedKey},
		AttemptCount:          1,
		QualityPreference:     "auto",
		PositionSeconds:       12,
		Failure:               playback.FailureV3{Classification: "browser_engine_unsupported_source"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("same-candidate recovery = %#v terminal = %#v", response, response.Terminal)
	}
	if response.PlaybackPlan.Source.FrameRate <= 0 || response.PlaybackPlan.Source.BitrateKbps <= 0 {
		t.Fatalf("source descriptor = %#v, want the running plan's frame rate and bitrate", response.PlaybackPlan.Source)
	}
	if response.PlaybackPlan.InventoryStatus != "declared" {
		t.Fatalf("inventory status = %q, want declared for an unprobed carry", response.PlaybackPlan.InventoryStatus)
	}
	stored := files[complete.ID]
	if stored.ProbeUpdatedAt != nil {
		t.Fatal("descriptor carry stamped the catalog row as probed")
	}
	if stored.Bitrate != 0 || len(stored.VideoTracks) != 1 || stored.VideoTracks[0].FrameRate != "" || stored.VideoTracks[0].Bitrate != 0 {
		t.Fatalf("descriptor carry wrote to the catalog row: %#v", stored)
	}
}

func TestSameCandidateFailureDescriptorCarryV3Fences(t *testing.T) {
	newSession := func() *playback.Session {
		return &playback.Session{
			VirtualSourceURI:                 "virtual://movie/item?result=candidate-a",
			VirtualSourceOwnerInstallationID: 7,
		}
	}
	newRecord := func() *playback.AttemptRecordV3 {
		return &playback.AttemptRecordV3{CurrentPlan: playback.PlanV3{
			EffectiveVirtualURI: "virtual://movie/item?result=candidate-a",
		}}
	}
	newResolved := func() resolvedVirtualPlaybackSource {
		return resolvedVirtualPlaybackSource{
			URI:     "virtual://movie/item?result=candidate-a",
			OwnerID: 7,
			File:    &models.MediaFile{ID: 42, FilePath: "virtual://movie/item?result=candidate-a"},
		}
	}

	tests := []struct {
		name       string
		mutate     func(*playback.Session, *playback.AttemptRecordV3, *resolvedVirtualPlaybackSource)
		provenance bool
		rotation   bool
		wantCarry  bool
	}{
		{name: "same candidate and installation", provenance: true, wantCarry: true},
		{name: "no session provenance", provenance: false},
		{name: "decode rotation", provenance: true, rotation: true},
		{
			name:       "rotated candidate",
			provenance: true,
			mutate: func(_ *playback.Session, _ *playback.AttemptRecordV3, resolved *resolvedVirtualPlaybackSource) {
				resolved.URI = "virtual://movie/item?result=candidate-b"
			},
		},
		{
			name:       "changed installation",
			provenance: true,
			mutate: func(_ *playback.Session, _ *playback.AttemptRecordV3, resolved *resolvedVirtualPlaybackSource) {
				resolved.OwnerID = 8
			},
		},
		{
			name:       "plan no longer names the pin",
			provenance: true,
			mutate: func(_ *playback.Session, record *playback.AttemptRecordV3, _ *resolvedVirtualPlaybackSource) {
				record.CurrentPlan.EffectiveVirtualURI = "virtual://movie/item?result=candidate-b"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session, record, candidate := newSession(), newRecord(), newResolved()
			if tc.mutate != nil {
				tc.mutate(session, record, &candidate)
			}
			got := sameCandidateFailureDescriptorCarryV3(playback.ReplanOperationFailureRecoveryV3, session, record, tc.provenance, candidate, tc.rotation)
			if got != tc.wantCarry {
				t.Fatalf("carry = %v, want %v", got, tc.wantCarry)
			}
		})
	}
}

// Server's recomputed PlanAttemptKeyV3 on a replanned CurrentPlan must equal the published canonical PlanAttemptKey.
func TestHandleReplanPlaybackV3RecomputedCurrentKeyMatchesPublishedKey(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	audioIdx1 := 1
	trackChangeReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-key-match-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-km-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       10,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx1}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	resp := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq)
	if resp.Outcome != playback.OutcomePlayableV3 || resp.PlaybackPlan == nil {
		t.Fatalf("track change failed: %#v", resp)
	}

	outputContextID := startRequest.ClientPlaybackContext.Output.OutputContextID
	recomputed := playback.PlanAttemptKeyV3(*resp.PlaybackPlan, outputContextID, nil)
	if recomputed != resp.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("recomputed key %q does not match published canonical key %q", recomputed, resp.PlaybackPlan.PlanAttemptKey)
	}

	// Stored attempt CurrentPlan also recomputes to the same canonical key.
	rec, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	storedRecomputed := playback.PlanAttemptKeyV3(rec.CurrentPlan, outputContextID, nil)
	if storedRecomputed != resp.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("stored attempt key %q does not match published canonical key %q", storedRecomputed, resp.PlaybackPlan.PlanAttemptKey)
	}
}

// Incomplete or stale client history must not bypass server recipe exclusion on failure recovery.
func TestHandleReplanPlaybackV3ReplanFailsWithIncompleteHistory(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	audioIdx1 := 1
	trackChangeReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-incomplete-hist-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-ih-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       10,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx1}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	resp := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq)
	if resp.Outcome != playback.OutcomePlayableV3 || resp.PlaybackPlan == nil {
		t.Fatalf("track change failed: %#v", resp)
	}

	// Client sends failure recovery with EMPTY AttemptedPlanKeys and a stale PlanAttemptKey.
	// Server's own recomputed currentKey must exclude the failed recipe.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-recovery-empty-hist",
		FailedPlanID:          resp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-rec-empty",
		PlanAttemptKey:        "v3:stale00000000000",
		AttemptedPlanKeys:     []string{started.PlaybackPlan.PlanAttemptKey}, // omits current plan's key
		AttemptCount:          1,
		PositionSeconds:       10,
		Failure:               playback.FailureV3{Classification: "player_failure"},
		SelectedTracks:        resp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	failResp := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	// Server must have excluded the failed recipe despite incomplete client history.
	if failResp.PlaybackPlan != nil && failResp.PlaybackPlan.PlanAttemptKey == resp.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("incomplete history must not reselect failed recipe, got: %#v", failResp)
	}
}

// End-to-end lifecycle regression covering: first frame -> successful seek reanchor -> connectivity failure.
// Preserves rendered evidence when the existing transport is actually retained across seek.
func TestHandleReplanPlaybackV3FirstFrameSeekReissueLifecycle(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.ExternalSubtitles = []models.ExternalSubtitle{
		{Path: writePlaybackTestMediaFile(t, "movie.de.srt"), Language: "de", Format: "srt"},
	}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.Capabilities.Containers = []string{"m3u8"}
	startRequest.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		},
	}
	german := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", german)
	startRequest.SubtitleTrackIndex = &german
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("start plan = %#v, want copy HLS", started.PlaybackPlan)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	// Verify attempt has CurrentTransportRendered = true
	rec, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.CurrentTransportRendered {
		t.Fatal("expected CurrentTransportRendered to be true after first_frame event")
	}

	// 1. Viewer seeks (seek_reanchor within active window retains the HLS transport).
	seekReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-seek-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       20,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	seekResp := postPlaybackReplanV3(t, handler, started.SessionID, seekReq)
	if seekResp.Outcome != playback.OutcomePlayableV3 || seekResp.PlaybackPlan == nil {
		t.Fatalf("seek reanchor failed: %#v", seekResp)
	}

	// Verify rendered evidence was preserved across the retained transport.
	recAfterSeek, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !recAfterSeek.CurrentTransportRendered {
		t.Fatal("expected CurrentTransportRendered to be preserved across retained seek transport")
	}

	// 2. Network connectivity failure occurs on the seeked stream.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-recovery-after-seek",
		FailedPlanID:          seekResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-rec-seek",
		PlanAttemptKey:        seekResp.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{seekResp.PlaybackPlan.PlanAttemptKey},
		AttemptCount:          1,
		PositionSeconds:       20,
		Failure:               playback.FailureV3{Classification: "transport_stall"},
		SelectedTracks:        seekResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	reissueResp := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	// Because rendered evidence was preserved, the working recipe is reissued!
	if reissueResp.Outcome != playback.OutcomePlayableV3 || reissueResp.PlaybackPlan == nil {
		t.Fatalf("expected playable reissue after seek network failure, got: %#v", reissueResp)
	}
	if reissueResp.PlaybackPlan.PlanAttemptKey != seekResp.PlaybackPlan.PlanAttemptKey {
		t.Fatalf("reissued attempt key %q does not match seek attempt key %q", reissueResp.PlaybackPlan.PlanAttemptKey, seekResp.PlaybackPlan.PlanAttemptKey)
	}
}

// When a replan rebuilds the transport rather than retaining it, rendered evidence is not preserved.
// The rebuilt transport starts unrendered, rejects stale first-frame events, and is not reissued on pre-first-frame failure.
func TestHandleReplanPlaybackV3RebuiltTransportStartsUnrendered(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	applyReplanProgressV3(t, handler, started.SessionID)
	// First frame is delayed and NOT sent yet.
	origPlanID := started.PlaybackPlan.PlanID
	origPlanKey := started.PlaybackPlan.PlanAttemptKey

	// 1. Viewer changes track (installs a newly planned replacement transport).
	audioIdx := 1
	tcReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-tc-rebuild-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-tc-rb-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       20,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	tcResp := postPlaybackReplanV3(t, handler, started.SessionID, tcReq)
	if tcResp.Outcome != playback.OutcomePlayableV3 || tcResp.PlaybackPlan == nil {
		t.Fatalf("track change failed: %#v", tcResp)
	}

	// Rebuilt transport must start unrendered.
	recAfterRebuild, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recAfterRebuild.CurrentTransportRendered {
		t.Fatal("expected rebuilt transport to start unrendered (CurrentTransportRendered = false)")
	}

	// 2. Delayed first-frame event from original transport arrives carrying old origPlanID.
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, origPlanID, origPlanKey)

	// Rebuilt transport must still be unrendered because plan IDs differ.
	recAfterDelayed, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recAfterDelayed.CurrentTransportRendered {
		t.Fatal("stale first-frame event falsely marked rebuilt transport as rendered")
	}

	// 3. Rebuilt transport fails before its first frame with connectivity loss.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-fail-after-rebuilt",
		FailedPlanID:          tcResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-fail-rb",
		PlanAttemptKey:        tcResp.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{tcResp.PlaybackPlan.PlanAttemptKey},
		AttemptCount:          1,
		PositionSeconds:       20,
		Failure:               playback.FailureV3{Classification: "transport_stall"},
		SelectedTracks:        tcResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	failResp := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	// Must NOT reissue because the rebuilt transport never rendered a frame!
	if failResp.Terminal == nil {
		t.Fatalf("unrendered rebuilt transport must not be reissued, got: %#v", failResp)
	}
}

// When a seek rebuilds the transport rather than retaining it (e.g. progressive remux seek),
// rendered evidence is not preserved. The rebuilt seek transport starts unrendered,
// rejects stale first-frame events from the pre-seek installation, and is not reissued on pre-first-frame failure.
func TestHandleReplanPlaybackV3RebuiltSeekTransportStartsUnrendered(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	stubCopySeekAnchorV3(handler)

	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassProgressiveV3: {
			Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		},
	}
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("start plan = %#v, want progressive remux", started.PlaybackPlan)
	}

	applyReplanProgressV3(t, handler, started.SessionID)
	origPlanID := started.PlaybackPlan.PlanID
	origPlanKey := started.PlaybackPlan.PlanAttemptKey

	// 1. Viewer seeks with progressive remux transport (must rebuild transport).
	seekReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-seek-rebuild-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-rb-001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       20,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	seekResp := postPlaybackReplanV3(t, handler, started.SessionID, seekReq)
	if seekResp.Outcome != playback.OutcomePlayableV3 || seekResp.PlaybackPlan == nil {
		t.Fatalf("seek reanchor failed: %#v", seekResp)
	}
	if seekResp.PlaybackPlan.PlanID == origPlanID {
		t.Fatalf("rebuilt seek transport must receive a distinct generation plan ID, got: %q", seekResp.PlaybackPlan.PlanID)
	}

	// Rebuilt transport must start unrendered.
	recAfterSeek, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recAfterSeek.CurrentTransportRendered {
		t.Fatal("expected rebuilt seek transport to start unrendered (CurrentTransportRendered = false)")
	}

	// 2. Delayed first-frame event from original transport arrives carrying old origPlanID.
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, origPlanID, origPlanKey)

	// Rebuilt transport must still be unrendered because plan IDs differ.
	recAfterDelayed, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recAfterDelayed.CurrentTransportRendered {
		t.Fatal("stale first-frame event falsely marked rebuilt seek transport as rendered")
	}

	// 3. Rebuilt transport fails before its first frame with connectivity loss.
	recoveryReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-fail-after-rebuilt-seek",
		FailedPlanID:          seekResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-fail-rb",
		PlanAttemptKey:        seekResp.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{seekResp.PlaybackPlan.PlanAttemptKey},
		AttemptCount:          1,
		PositionSeconds:       20,
		Failure:               playback.FailureV3{Classification: "transport_stall"},
		SelectedTracks:        seekResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	failResp := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	// Must NOT reissue because the rebuilt transport never rendered a frame!
	if failResp.Terminal == nil {
		t.Fatalf("unrendered rebuilt seek transport must not be reissued, got: %#v", failResp)
	}
}

// Delayed failure from an initial plan arriving after a same-recipe reissue must fail with 409 stale_playback_plan
// and must NOT modify the current reissued plan.
func TestHandleReplanPlaybackV3DelayedPredecessorFailureFailsStale(t *testing.T) {
	handler := newProgressReissueHandlerV3(t)
	started, startRequest := progressReissueStartV3(t, handler)
	applyReplanProgressV3(t, handler, started.SessionID)
	applyReplanFirstFrameV3(t, handler, started.SessionID, startRequest.PlaybackAttemptID, started.PlaybackPlan.PlanID, started.PlaybackPlan.PlanAttemptKey)

	initialPlanID := started.PlaybackPlan.PlanID

	// 1. Same-recipe reissue succeeds after connectivity loss.
	recoveryReq := progressReissueRequestV3(started, startRequest, "replan-initial-reissue", 1, []string{started.PlaybackPlan.PlanAttemptKey})
	reissuedResp := postPlaybackReplanV3(t, handler, started.SessionID, recoveryReq)
	if reissuedResp.Outcome != playback.OutcomePlayableV3 || reissuedResp.PlaybackPlan == nil {
		t.Fatalf("expected playable reissue, got: %#v", reissuedResp)
	}
	currentGenerationPlanID := reissuedResp.PlaybackPlan.PlanID
	if currentGenerationPlanID == initialPlanID {
		t.Fatalf("reissued plan ID %q must differ from initial plan ID %q", currentGenerationPlanID, initialPlanID)
	}

	// 2. Delayed failure from the initial installation arrives with FailedPlanID = initialPlanID and a new request ID.
	delayedReq := progressReissueRequestV3(started, startRequest, "replan-delayed-old-fail", 1, []string{started.PlaybackPlan.PlanAttemptKey})
	delayedReq.FailedPlanID = initialPlanID

	body, err := json.Marshal(delayedReq)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", started.SessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)

	// Server must reject with 409 StatusConflict and stale_playback_plan!
	if rr.Code != http.StatusConflict {
		t.Fatalf("replan status = %d, want 409 conflict, body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "stale_playback_plan") {
		t.Fatalf("expected error stale_playback_plan, got: %s", rr.Body.String())
	}

	// 3. Verify current plan is completely unchanged.
	rec, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CurrentPlanID != currentGenerationPlanID {
		t.Fatalf("current plan ID was modified: %q != %q", rec.CurrentPlanID, currentGenerationPlanID)
	}
}

// Ordinary rebuilding replan and rebuilt seek must bind the committed frozen recipe to the final installed plan ID,
// so that ValidFor remains true and a subsequent seek reanchor succeeds.
func TestHandleReplanPlaybackV3RebuiltPlanFrozenRecipeValidForSubsequentSeek(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "fra"})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfigNoTranscode(t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "original"
	orig := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	orig.ValidatedClaims = []string{playback.ClaimClientSelectedAudioTrackV3}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = orig
	started := startV3PlaybackForHandlerTest(t, handler, startRequest)
	if started.PlaybackPlan == nil {
		t.Fatalf("start plan = %#v", started)
	}

	// 1. Ordinary rebuilding replan (track change installs a new transport).
	audioIdx1 := 1
	trackChangeReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-tc-rebuild-001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-tc-rb",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       10,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: "file:42:audio:1", Index: &audioIdx1}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	replanResp := postPlaybackReplanV3(t, handler, started.SessionID, trackChangeReq)
	if replanResp.Outcome != playback.OutcomePlayableV3 || replanResp.PlaybackPlan == nil {
		t.Fatalf("track change failed: %#v", replanResp)
	}

	// Assert updated attempt has FrozenRecipe.ValidFor(CurrentPlan) == true!
	recAfterReplan, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !recAfterReplan.FrozenRecipe.ValidFor(recAfterReplan.CurrentPlan) {
		t.Fatalf("frozen recipe PlanID %q is not valid for CurrentPlan %q", recAfterReplan.FrozenRecipe.PlanID, recAfterReplan.CurrentPlan.PlanID)
	}

	// 2. Subsequent seek reanchor succeeds!
	seekReq := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-seek-after-tc",
		FailedPlanID:          replanResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-after-tc",
		PlanAttemptKey:        replanResp.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       25,
		SelectedTracks:        replanResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	seekResp := postPlaybackReplanV3(t, handler, started.SessionID, seekReq)
	if seekResp.Outcome != playback.OutcomePlayableV3 || seekResp.PlaybackPlan == nil {
		t.Fatalf("subsequent seek failed: %#v", seekResp)
	}

	// Assert updated attempt after seek reanchor still has FrozenRecipe.ValidFor(CurrentPlan) == true!
	recAfterSeek, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !recAfterSeek.FrozenRecipe.ValidFor(recAfterSeek.CurrentPlan) {
		t.Fatalf("frozen recipe PlanID %q after seek is not valid for CurrentPlan %q", recAfterSeek.FrozenRecipe.PlanID, recAfterSeek.CurrentPlan.PlanID)
	}

	// 3. Second seek reanchor after the first seek also succeeds!
	seekReq2 := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-seek-2-after-tc",
		FailedPlanID:          seekResp.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-2-after-tc",
		PlanAttemptKey:        seekResp.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       30,
		SelectedTracks:        seekResp.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	seekResp2 := postPlaybackReplanV3(t, handler, started.SessionID, seekReq2)
	if seekResp2.Outcome != playback.OutcomePlayableV3 || seekResp2.PlaybackPlan == nil {
		t.Fatalf("second seek failed: %#v", seekResp2)
	}
}
