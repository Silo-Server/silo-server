package handlers

import (
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// ladderTVSourceFile is the field source shape: an HEVC file with a 5.1 track
// the device copies over native HLS but cannot direct-play from its container.
// HEVC keeps the copy recipe reachable (the whole class is retired only because
// one copy recipe is spent) and the transcode a genuinely different recipe.
func ladderTVSourceFile(t *testing.T) *models.MediaFile {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie-hevc.mkv")
	file.CodecVideo = "hevc"
	file.CodecAudio = "aac"
	file.Resolution = "1080p"
	file.Bitrate = 12_000
	file.AudioChannels = 6
	file.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "Main", Level: 120, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 8,
		VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR,
	}}
	file.AudioTracks = []models.AudioTrack{{Language: "en", Codec: "aac", Channels: 6, Layout: "5.1", Default: true}}
	return file
}

// ladderTVHandler runs the ladder with local execution: both the copy remux and
// the H.264/AAC transcode are executable, so a retired copy recipe cannot hide
// behind a missing executor.
func ladderTVHandler(t *testing.T, file *models.MediaFile) *PlaybackHandler {
	t.Helper()
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true"}}
	return handler
}

// ladderTVStartRequest is the TV advertisement: HEVC direct play plus native
// HLS with an H.264 fallback, and no progressive delivery.
func ladderTVStartRequest() playback.StartRequestV3 {
	start := v3HandlerStartRequest()
	start.Capabilities.CodecsVideo = []string{"hevc", "h264"}
	start.Capabilities.CodecsVideoHardware = []string{"hevc", "h264"}
	start.Capabilities.Containers = []string{"mkv", "mp4"}
	start.Capabilities.MaxResolution = "1080p"
	start.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main"}, Levels: []int{120}, BitDepths: []int{8},
		MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000, Hardware: true,
	}}
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassOriginalHTTPV3: {
			Enabled: true, SupportedOnDevice: true,
			Containers: []string{"mkv"}, VideoCodecs: []string{"hevc"},
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
		},
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true,
			Containers: []string{"hls"}, VideoCodecs: []string{"hevc", "h264"},
			AudioDecodeCodecs: []string{"aac"},
			Subtitles:         playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
		},
	}
	return start
}

// ladderReplanRequest answers one failed plan with a failure recovery.
func ladderReplanRequest(start playback.StartRequestV3, plan *playback.PlanV3, id, classification string, attempted []string, attemptCount int) playback.ReplanRequestV3 {
	key := playback.PlanAttemptKeyV3(*plan, start.ClientPlaybackContext.Output.OutputContextID, nil)
	return playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "ladder-" + id, FailedPlanID: plan.PlanID, PlanAttemptID: "ladder-attempt-" + id,
		PlanAttemptKey: key, AttemptedPlanKeys: append(append([]string(nil), attempted...), key), AttemptCount: attemptCount,
		PositionSeconds: 40, QualityPreference: start.QualityPreference, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: classification},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
}

// serveLadderSegments marks the live transport as one the client read from: the
// playlist was served and a segment was asked for. That is what distinguishes an
// abort from a client that never collected a byte (see
// abandonedBeforeFirstSegmentV3).
func serveLadderSegments(t *testing.T, handler *PlaybackHandler, sessionID, streamURL string) {
	t.Helper()
	fetchPlaybackManifestV3(t, handler, sessionID, streamURL)
	if session := handler.tm.GetTranscodeSession(sessionID); session != nil {
		lease, _ := session.OpenSegment("seg_0.m4s")
		if lease != nil {
			_ = lease.Close()
		}
	}
	if handler.abandonedBeforeFirstSegmentV3(sessionID) {
		t.Fatal("the transport still looks abandoned before the first segment")
	}
}

// TestReplanV3AbortedCopyRecipeKeepsTheHLSClassAndStepsToTranscode is the field
// scenario: a TV aborts direct play, plays the HLS copy for a few seconds, then
// aborts that too. The direct abort retires original_http for the attempt — the
// client reported it could not play the source bytes — while both aborts name
// player_failure, which cannot prove the HLS delivery class unusable: the client
// parsed the plan and fetched media. The copy recipe is spent, so the ladder must
// step to the transcode behind the same HLS class instead of answering a
// non-retryable adaptation_unavailable.
func TestReplanV3AbortedCopyRecipeKeepsTheHLSClassAndStepsToTranscode(t *testing.T) {
	file := ladderTVSourceFile(t)
	handler := ladderTVHandler(t, file)
	start := ladderTVStartRequest()

	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	direct := started.PlaybackPlan
	first := ladderReplanRequest(start, direct, "0001", "player_failure", nil, 1)
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, first)
	if recovered.Terminal != nil {
		t.Fatalf("first recovery terminal = %#v, want the HLS copy plan", recovered.Terminal)
	}
	copyPlan := recovered.PlaybackPlan
	if copyPlan == nil || copyPlan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("first recovery plan = %#v, want server_remux_hls", copyPlan)
	}
	serveLadderSegments(t, handler, started.SessionID, copyPlan.Stream.URL)

	second := ladderReplanRequest(start, copyPlan, "0002", "player_failure", first.AttemptedPlanKeys, 2)
	stepped := postPlaybackReplanV3(t, handler, started.SessionID, second)
	if stepped.Terminal != nil {
		t.Fatalf("second abort terminal = %#v, want the transcode behind the same HLS class", stepped.Terminal)
	}
	transcode := stepped.PlaybackPlan
	if transcode == nil {
		t.Fatal("second abort returned no plan")
	}
	if playback.DeliveryClassV3(transcode.Delivery) != playback.DeliveryClassHLSV3 {
		t.Fatalf("second abort plan delivery = %s, want the HLS class", transcode.Delivery)
	}
	if transcode.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("second abort plan = %s (%s), want the server transcode the copy recipe shielded", transcode.Delivery, transcode.DecisionReason)
	}
	if transcode.PlanAttemptKey == copyPlan.PlanAttemptKey || transcode.PlanAttemptKey == direct.PlanAttemptKey {
		t.Fatalf("second abort replayed a retired recipe: %s", transcode.PlanAttemptKey)
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	hls := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3]
	if !hls.Enabled || hls.FailureReason == demoteDeliveryReasonV3 {
		t.Fatalf("two aborts retired the whole HLS class: %+v", hls)
	}
	directCapability := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if directCapability.Enabled || directCapability.FailureReason != demoteDeliveryReasonV3 {
		// A player_failure on original_http now retires direct play for the
		// attempt: the client reported it could not play the source bytes, so
		// the ladder starts at the packaged routes instead of offering the same
		// direct plan again. The HLS class above stays eligible.
		t.Fatalf("an aborted direct plan was not retired: %+v", directCapability)
	}
	if excluded := record.RecoveryState.RecipeExclusions; !containsStringExactV3(excluded, copyPlan.PlanAttemptKey) {
		t.Fatalf("abandoned copy recipe was not recorded durably: %v", excluded)
	}
}

// TestReplanV3TrackChangeDoesNotReplayAnAbandonedCopyRecipe is the thrash guard:
// with class demotion gone, the copy recipe must not come back through a
// same-output replan that echoes no attempted-key history. The durable recipe
// chain retires the recipe the ladder already spent, so the transcode that
// replaced it stays the answer.
func TestReplanV3TrackChangeDoesNotReplayAnAbandonedCopyRecipe(t *testing.T) {
	file := ladderTVSourceFile(t)
	handler := ladderTVHandler(t, file)
	start := ladderTVStartRequest()

	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")
	direct := started.PlaybackPlan

	first := ladderReplanRequest(start, direct, "0101", "player_failure", nil, 1)
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, first)
	if recovered.Terminal != nil || recovered.PlaybackPlan == nil {
		t.Fatalf("first recovery = %#v", recovered)
	}
	copyPlan := recovered.PlaybackPlan
	if copyPlan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("first recovery plan = %#v, want the HLS copy", copyPlan)
	}
	serveLadderSegments(t, handler, started.SessionID, copyPlan.Stream.URL)

	// The copy recipe is abandoned too, and the ladder steps to the transcode
	// behind the same class.
	second := ladderReplanRequest(start, copyPlan, "0102", "player_failure", first.AttemptedPlanKeys, 2)
	stepped := postPlaybackReplanV3(t, handler, started.SessionID, second)
	if stepped.Terminal != nil || stepped.PlaybackPlan == nil {
		t.Fatalf("second recovery = %#v", stepped)
	}
	transcode := stepped.PlaybackPlan
	if transcode.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("second recovery plan = %#v, want the HLS transcode", transcode)
	}

	// The device now reports a track change that changes nothing, the way a
	// player re-arms its pipeline. It is not a failure recovery, so it echoes no
	// attempted-key history: only the attempt's durable chain remembers that the
	// copy recipe is spent. Sticky delivery alone would pick the copy again,
	// because both recipes share the HLS class.
	trackChange := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationTrackChangeV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "ladder-track-0103", FailedPlanID: transcode.PlanID, PlanAttemptID: "ladder-attempt-0103",
		PlanAttemptKey: transcode.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 40,
		QualityPreference: start.QualityPreference, SelectedTracks: transcode.SelectedTracks,
		Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext,
	}
	next := postPlaybackReplanV3(t, handler, started.SessionID, trackChange)
	if next.Terminal != nil || next.PlaybackPlan == nil {
		t.Fatalf("track_change replan = %#v", next)
	}
	if next.PlaybackPlan.PlanAttemptKey == copyPlan.PlanAttemptKey {
		t.Fatalf("track_change replayed the recipe the client abandoned: %s", next.PlaybackPlan.PlanAttemptKey)
	}
	if next.PlaybackPlan.PlanAttemptKey == direct.PlanAttemptKey {
		t.Fatalf("track_change went back to the first aborted recipe: %s", next.PlaybackPlan.PlanAttemptKey)
	}
	if playback.DeliveryClassV3(next.PlaybackPlan.Delivery) != playback.DeliveryClassHLSV3 {
		t.Fatalf("track_change plan = %s, want the playing HLS class", next.PlaybackPlan.Delivery)
	}
}

// TestReplanV3AbortLadderTerminatesAfterEveryRecipeIsSpent walks the ladder to
// its end: every recipe the source and output allow is served once, and the
// terminal is bounded. It is the termination proof for the recipe-exclusion
// rule — the exclusion must spend the ladder, never loop. Because this chain
// began by retiring a direct route the client reported failing (player_failure),
// the exhaustion is the retryable routes_exhausted: the ladder is spent for
// this attempt, and a fresh start negotiates again.
func TestReplanV3AbortLadderTerminatesAfterEveryRecipeIsSpent(t *testing.T) {
	file := ladderTVSourceFile(t)
	handler := ladderTVHandler(t, file)
	start := ladderTVStartRequest()

	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil {
		t.Fatalf("start = %#v", started)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	served := []string{started.PlaybackPlan.PlanAttemptKey}
	plan := started.PlaybackPlan
	attempted := []string(nil)
	var terminal *playback.TerminalV3
	for attempt := 1; attempt <= 8; attempt++ {
		request := ladderReplanRequest(start, plan, fmt.Sprintf("%04d", attempt), "player_failure", attempted, attempt)
		attempted = request.AttemptedPlanKeys
		response := postPlaybackReplanV3(t, handler, started.SessionID, request)
		if response.Terminal != nil {
			terminal = response.Terminal
			break
		}
		if response.PlaybackPlan == nil {
			t.Fatalf("attempt %d returned neither a plan nor a terminal", attempt)
		}
		if containsStringExactV3(served, response.PlaybackPlan.PlanAttemptKey) {
			t.Fatalf("attempt %d re-served a recipe that already failed: %s", attempt, response.PlaybackPlan.PlanAttemptKey)
		}
		served = append(served, response.PlaybackPlan.PlanAttemptKey)
		plan = response.PlaybackPlan
		if plan.Delivery != playback.DeliveryRemuxHLSV3 {
			serveLadderSegments(t, handler, started.SessionID, plan.Stream.URL)
		}
	}
	if terminal == nil {
		t.Fatalf("the ladder never terminated after %d recipes: %v", len(served), served)
	}
	switch terminal.Reason {
	case "adaptation_exhausted", "adaptation_unavailable", playback.TerminalRoutesExhaustedV3:
	default:
		t.Fatalf("terminal = %#v, want a bounded exhaustion", terminal)
	}
	// routes_exhausted is retryable by contract: the client failed direct play
	// and every packaged recipe, and a fresh start re-negotiates.
	if terminal.Reason == playback.TerminalRoutesExhaustedV3 {
		if !terminal.Retryable {
			t.Fatalf("terminal = %#v, want a retryable exhaustion", terminal)
		}
	} else if terminal.Retryable {
		t.Fatalf("terminal = %#v, want a non-retryable exhaustion", terminal)
	}
	if len(served) < 3 {
		t.Fatalf("only %d recipes were served before the terminal (%#v); the ladder retired a class it had not finished", len(served), served)
	}
}

// TestDecodeFailureStillDemotesTheDeliveryClass keeps the decode evidence rule:
// a classification that names a recipe property retires the class as before,
// because the next recipe behind it shares the same decoders.
func TestDecodeFailureStillDemotesTheDeliveryClass(t *testing.T) {
	file := ladderTVSourceFile(t)
	handler := ladderTVHandler(t, file)
	start := ladderTVStartRequest()

	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil {
		t.Fatalf("start = %#v", started)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	response := postPlaybackReplanV3(t, handler, started.SessionID, ladderReplanRequest(start, started.PlaybackPlan, "0201", "decoder_failure", nil, 1))
	if response.Terminal != nil {
		t.Fatalf("decode recovery terminal = %#v, want the HLS plan", response.Terminal)
	}
	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	direct := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if direct.Enabled || direct.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("a decoder failure did not retire direct play: %+v", direct)
	}
}

// TestReplanV3StartupTimeoutRetiresDirectPlay is the documented gap: a
// startup_timeout names nothing about the decoder, so under the old rule a
// direct plan stayed eligible and the client was handed the same bytes that
// never produced a frame. A direct route now retires on any recognized
// playback failure, and the remaining chain is bounded and retryable.
func TestReplanV3StartupTimeoutRetiresDirectPlay(t *testing.T) {
	file := ladderTVSourceFile(t)
	handler := ladderTVHandler(t, file)
	start := ladderTVStartRequest()

	started := postPlaybackStartV3(t, handler, marshalV3StartRequest(t, start))
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v, want direct play", started.PlaybackPlan)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")
	direct := started.PlaybackPlan

	first := ladderReplanRequest(start, direct, "0401", "startup_timeout", nil, 1)
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, first)
	if recovered.Terminal != nil || recovered.PlaybackPlan == nil {
		t.Fatalf("timeout recovery = %#v", recovered)
	}
	if recovered.PlaybackPlan.Delivery == playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("a startup_timeout re-issued direct play: %s", recovered.PlaybackPlan.Delivery)
	}
	if recovered.PlaybackPlan.PlanAttemptKey == direct.PlanAttemptKey {
		t.Fatal("a startup_timeout replayed the failed direct plan")
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	capability := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	if capability.Enabled || capability.FailureReason != demoteDeliveryReasonV3 {
		t.Fatalf("direct play was not retired for the attempt: %+v", capability)
	}

	// Keep failing on whatever the server builds. The chain must terminate
	// instead of returning to the retired direct plan, and the terminal must be
	// retryable because a fresh start negotiates again.
	plan := recovered.PlaybackPlan
	attempted := first.AttemptedPlanKeys
	var terminal *playback.TerminalV3
	for attempt := 2; attempt <= 8; attempt++ {
		request := ladderReplanRequest(start, plan, fmt.Sprintf("04%02d", attempt), "startup_timeout", attempted, attempt)
		attempted = request.AttemptedPlanKeys
		response := postPlaybackReplanV3(t, handler, started.SessionID, request)
		if response.Terminal != nil {
			terminal = response.Terminal
			break
		}
		if response.PlaybackPlan == nil {
			t.Fatalf("attempt %d returned neither a plan nor a terminal", attempt)
		}
		if response.PlaybackPlan.Delivery == playback.DeliveryOriginalHTTPV3 {
			t.Fatalf("attempt %d went back to the retired direct plan", attempt)
		}
		if response.PlaybackPlan.Delivery == playback.DeliveryRemuxHLSV3 {
			serveLadderSegments(t, handler, started.SessionID, response.PlaybackPlan.Stream.URL)
		}
		plan = response.PlaybackPlan
	}
	if terminal == nil {
		t.Fatal("a repeatedly failing client never reached a terminal")
	}
	if terminal.Reason != playback.TerminalRoutesExhaustedV3 || !terminal.Retryable {
		t.Fatalf("terminal = %#v, want the retryable %s", terminal, playback.TerminalRoutesExhaustedV3)
	}
}
