package handlers

import (
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// generationFenceHandlerV3 starts a segment-delivering route (HLS) so a fence
// replan names a plan whose recipe a failure_recovery could retire.
func generationFenceHandlerV3(t *testing.T) (*PlaybackHandler, playback.StartRequestV3) {
	t.Helper()
	handler, start, _ := sessionStabilityHandlerV3(t)
	// Direct play is unavailable: this session is served by the HLS segment
	// layer, which is what mints the `sgen` token a 412 fences.
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{
		Enabled: false,
	}
	return handler, start
}

func generationFenceReplanV3(start playback.StartRequestV3, plan *playback.PlanV3, id, classification string, attempted []string, attemptCount int) playback.ReplanRequestV3 {
	key := playback.PlanAttemptKeyV3(*plan, start.ClientPlaybackContext.Output.OutputContextID, nil)
	return playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "generation-fence-" + id, FailedPlanID: plan.PlanID,
		PlanAttemptID: "generation-fence-attempt-" + id, PlanAttemptKey: key,
		AttemptedPlanKeys: append(append([]string(nil), attempted...), key), AttemptCount: attemptCount,
		PositionSeconds: 40, QualityPreference: start.QualityPreference, SelectedTracks: plan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: classification},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
}

// A segment-generation fence (412 stale_generation) is the timeline moving,
// not the recipe failing. Reporting it through failure_recovery must not spend
// the recipe: the replan re-serves the same route instead of terminalling at
// adaptation_exhausted, and the fence never enters the durable exclusion chain.
// This is the attribution the stress cells exposed: a fenced playlist reported
// as a route failure retired the only recipe and DELETEd a healthy session.
func TestReplanV3GenerationFenceNeverRetiresTheRecipe(t *testing.T) {
	handler, start := generationFenceHandlerV3(t)
	started := sessionStabilityStartV3(t, handler, start)
	plan := started.PlaybackPlan
	if plan == nil || playback.DeliveryClassV3(plan.Delivery) != playback.DeliveryClassHLSV3 {
		t.Fatalf("fixture expected a segment-delivering HLS start, got %#v", plan)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	attempted := []string{plan.PlanAttemptKey}
	for attempt := 1; attempt <= 3; attempt++ {
		request := generationFenceReplanV3(start, plan, fmt.Sprintf("%04d", attempt), "stale_generation", attempted, attempt)
		response := postPlaybackReplanV3(t, handler, started.SessionID, request)
		if response.Terminal != nil {
			t.Fatalf("fence attempt %d terminalled at %q; a fence never exhausts the ladder",
				attempt, response.Terminal.Reason)
		}
		if response.PlaybackPlan == nil {
			t.Fatalf("fence attempt %d returned no plan", attempt)
		}
		// The same route is still the answer: the generation moved, the recipe
		// did not fail.
		if response.PlaybackPlan.PlanAttemptKey != plan.PlanAttemptKey {
			t.Fatalf("fence attempt %d planned a different recipe: %s, want %s",
				attempt, response.PlaybackPlan.PlanAttemptKey, plan.PlanAttemptKey)
		}
		plan = response.PlaybackPlan
		attempted = append(attempted, plan.PlanAttemptKey)
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if containsStringExactV3(record.RecoveryState.RecipeExclusions, started.PlaybackPlan.PlanAttemptKey) {
		t.Fatalf("a fence recorded the healthy recipe durably: %v", record.RecoveryState.RecipeExclusions)
	}
}

// The fence classification must not demote the delivery either: the route is
// still viable, so a later genuine failure still sees it eligible.
func TestReplanV3GenerationFenceKeepsTheDeliveryEligible(t *testing.T) {
	handler, start := generationFenceHandlerV3(t)
	started := sessionStabilityStartV3(t, handler, start)
	plan := started.PlaybackPlan
	if plan == nil || playback.DeliveryClassV3(plan.Delivery) != playback.DeliveryClassHLSV3 {
		t.Fatalf("fixture expected a segment-delivering HLS start, got %#v", plan)
	}
	defer handler.tm.CloseTranscodeSession(started.SessionID, "")

	request := generationFenceReplanV3(start, plan, "0201", "stale_generation", []string{plan.PlanAttemptKey}, 1)
	response := postPlaybackReplanV3(t, handler, started.SessionID, request)
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("fence replan = %#v", response)
	}

	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	delivery := record.NormalizedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3]
	if !delivery.Enabled || delivery.FailureReason == demoteDeliveryReasonV3 {
		t.Fatalf("a fence demoted the delivery class: %+v", delivery)
	}
}
